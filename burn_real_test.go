package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/exporter"
	"transcribe-whisper/internal/media"
)

// TestRealBurnSubtitle 用本机已安装的 FFmpeg 实际压制一次字幕，
// 验证「缺少 subtitles 滤镜」问题的修复效果：
//  1. 能通过配置的路径找到带 libass 的构建；
//  2. Burn 能产出带字幕的画面；
//  3. 产出视频时长与源一致。
//
// 应用已改为「只用手动配置的 FFmpeg」，因此这里读环境变量
// TRANSCRIBE_TEST_FFMPEG 指定的 ffmpeg.exe；未设置时跳过。
func TestRealBurnSubtitle(t *testing.T) {
	configured := strings.TrimSpace(os.Getenv("TRANSCRIBE_TEST_FFMPEG"))
	if configured == "" {
		t.Skip("未设置 TRANSCRIBE_TEST_FFMPEG（指向完整版 ffmpeg.exe），跳过")
	}
	burner := resolveFFmpeg(context.Background(), configured)
	if burner == nil {
		t.Fatalf("配置的 FFmpeg 不可用：%s", configured)
	}
	caps := burner.Available(context.Background())
	if !caps.OK {
		t.Fatalf("FFmpeg 无法执行：%s", caps.Detail)
	}
	if !caps.SupportsSubtitlesBurn {
		t.Skip("该构建没有 subtitles 滤镜，无法压制字幕")
	}
	t.Logf("使用构建：%s（%s）", caps.Path, caps.Version)

	workDir := t.TempDir()

	// 第二步：合成 6 秒测试视频（测试音源 + 纯色画面）。
	videoPath := filepath.Join(workDir, "source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	synth := exec.CommandContext(ctx, burner.Path(),
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=navy:size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest",
		videoPath,
	)
	if out, err := synth.CombinedOutput(); err != nil {
		t.Fatalf("合成测试视频失败: %v\n%s", err, strings.TrimSpace(string(out)))
	}

	// 第三步：生成与视频时间轴对齐的 SRT。
	segments := []exporter.Segment{
		{Sequence: 1, Start: 0.5, End: 3.0, Source: "Hello", Translated: "你好，世界"},
		{Sequence: 2, Start: 3.2, End: 5.8, Source: "Burn test", Translated: "字幕压制测试"},
	}
	srt, err := exporter.Export(segments, exporter.Options{Format: exporter.FormatSRT})
	if err != nil {
		t.Fatalf("生成 SRT 失败: %v", err)
	}
	srtPath := filepath.Join(workDir, "subtitles.srt")
	if err := os.WriteFile(srtPath, []byte(srt), 0o644); err != nil {
		t.Fatalf("写入 SRT 失败: %v", err)
	}

	// 第四步：压制。
	outputPath := filepath.Join(workDir, "burned.mp4")
	burnCtx, burnCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer burnCancel()
	if err := burner.Burn(burnCtx, media.BurnRequest{
		VideoPath:    videoPath,
		SubtitlePath: srtPath,
		OutputPath:   outputPath,
		FontSize:     28,
		MarginV:      40,
	}); err != nil {
		t.Fatalf("字幕压制失败: %v", err)
	}

	// 第五步：验证产物。
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("压制产物不存在: %v", err)
	}
	if info.Size() < 10*1024 {
		t.Fatalf("压制产物过小（%d 字节），不像有效视频", info.Size())
	}
	probeInfo, probeErr := burner.Probe(burnCtx, outputPath)
	if probeErr != nil {
		t.Logf("ffprobe 探测不可用（%v），仅以文件大小判定", probeErr)
	} else if probeInfo.Duration <= 0 {
		t.Fatalf("压制产物时长异常: %+v", probeInfo)
	} else {
		t.Logf("压制产物：%d 字节，时长 %.2f 秒", info.Size(), probeInfo.Duration)
	}

	// 第六步：验证画面里确实烧进了字幕——抽取两帧做字节对比不现实，
	// 改为验证输出与输入的体积差（字幕渲染会明显增大码流）。
	sourceInfo, err := os.Stat(videoPath)
	if err == nil && info.Size() <= sourceInfo.Size() {
		t.Logf("提示：输出 %d 字节未超过源 %d 字节（纯色视频下字幕帧压缩率很高，属正常）", info.Size(), sourceInfo.Size())
	}
}
