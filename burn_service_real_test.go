package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/service"
)

// TestRealBurnThroughService 验证「任务字幕 → service → 真实 FFmpeg → MP4」整条链路。
//
// 与 TestRealBurnSubtitle 的区别：那条只测 media.Burn 本身，这条穿过
// service.Queue.BurnSubtitles，覆盖字幕文本生成、输出路径校验与依赖装配。
func TestRealBurnThroughService(t *testing.T) {
	// 第一步：用用户配置的 FFmpeg（应用已改为只用手动配置，没有自动发现）。
	configured := strings.TrimSpace(os.Getenv("TRANSCRIBE_TEST_FFMPEG"))
	if configured == "" {
		t.Skip("未设置 TRANSCRIBE_TEST_FFMPEG（指向完整版 ffmpeg.exe），跳过")
	}
	burner := resolveFFmpeg(context.Background(), configured)
	if burner == nil {
		t.Fatalf("配置的 FFmpeg 不可用：%s", configured)
	}
	if caps := burner.Available(context.Background()); !caps.OK || !caps.SupportsSubtitlesBurn {
		t.Skipf("该构建不支持字幕压制：%s", caps.Detail)
	}

	workDir := t.TempDir()
	store, err := repository.Open(filepath.Join(workDir, "burn.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 第二步：合成 6 秒源视频。
	sourcePath := filepath.Join(workDir, "source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	synth := exec.CommandContext(ctx, burner.Path(),
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest",
		sourcePath,
	)
	if out, err := synth.CombinedOutput(); err != nil {
		t.Fatalf("合成测试视频失败: %v\n%s", err, strings.TrimSpace(string(out)))
	}

	// 第三步：写一条带两条字幕的任务。
	now := time.Now().UTC()
	rec := &repository.TaskRecord{
		ID:             "real-burn-task",
		Name:           "burn-demo",
		SourceFile:     sourcePath,
		SourceLanguage: "en",
		TargetLanguage: "zh-CN",
		Status:         repository.TaskStatusCompleted,
		Stage:          "completed",
		Progress:       100,
		SubtitleCount:  2,
		Engine:         "faster-whisper",
		Model:          "tiny",
		Provider:       "ollama",
		OutputFormat:   "srt",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateTask(ctx, rec); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	segments := []repository.SegmentRecord{
		{ID: "real-burn-1", TaskID: rec.ID, Sequence: 1, Start: 0.5, End: 3, SourceText: "Hello world", Translated: "你好，世界"},
		{ID: "real-burn-2", TaskID: rec.ID, Sequence: 2, Start: 3.2, End: 5.8, SourceText: "Burn through service", Translated: ""},
	}
	if err := store.UpsertSegments(ctx, rec.ID, segments); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}

	// 第四步：穿过 service 层压制（Burner 用真实 FFmpeg）。
	queue := service.NewQueue(service.Deps{
		Store:   store,
		Media:   burner,
		Burner:  burner,
		WorkDir: filepath.Join(workDir, "work"),
	})
	t.Cleanup(queue.Close)

	outputPath := filepath.Join(workDir, "out.mp4")
	got, err := queue.BurnSubtitles(ctx, rec.ID, outputPath, service.BurnOptions{FontSize: 30, MarginV: 40})
	if err != nil {
		t.Fatalf("service 层压制失败: %v", err)
	}
	if got == "" {
		t.Fatal("压制成功时应返回产物路径")
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("压制产物不存在: %v", err)
	}
	if info.Size() < 10*1024 {
		t.Fatalf("压制产物过小（%d 字节）", info.Size())
	}
	probeInfo, probeErr := burner.Probe(ctx, got)
	if probeErr != nil {
		t.Logf("ffprobe 不可用（%v），仅以文件大小判定", probeErr)
	} else if probeInfo.Duration <= 0 {
		t.Fatalf("产物时长异常: %+v", probeInfo)
	} else {
		t.Logf("service 层压制产物：%d 字节，时长 %.2f 秒", info.Size(), probeInfo.Duration)
	}

	// 第五步：确认中间字幕文件用的是译文优先 + 原文回退策略。
	srtPath := filepath.Join(workDir, "work", rec.ID, "burn.srt")
	raw, readErr := os.ReadFile(srtPath)
	if readErr != nil {
		t.Logf("中间字幕文件不可读（%v），跳过文本校验", readErr)
		return
	}
	text := string(raw)
	if !strings.Contains(text, "你好，世界") {
		t.Fatalf("压制字幕应使用译文: %s", text)
	}
	if !strings.Contains(text, "Burn through service") {
		t.Fatalf("缺译文的条目应回退原文: %s", text)
	}

	// 顺带确认 media 包的类型断言仍然成立。
	var _ media.Burner = burner
}
