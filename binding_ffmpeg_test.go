package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/repository"
)

// 本文件验证「FFmpeg 完全手动指定」的 Binding 契约：
//   - 新装环境必须报告未安装，并给出默认安装目录；
//   - 手动指定有效路径后立刻生效；
//   - 指定无效路径时如实报错，不静默接受；
//   - 清除后回到未安装状态。

func TestBindingFFmpegStatusUnconfigured(t *testing.T) {
	app := newFrontendApp(t)
	status := app.GetFFmpegStatus()
	if status.Configured {
		t.Fatalf("新装环境不应视为已配置: %+v", status)
	}
	if status.OK || status.CanBurn {
		t.Fatalf("未配置时不应报告可用: %+v", status)
	}
	if status.Detail == "" {
		t.Fatal("未安装时必须给中文提示")
	}
	if status.DefaultDir == "" {
		t.Fatal("必须给出默认安装目录")
	}
	// 默认目录应在应用数据目录下。
	if !strings.HasPrefix(status.DefaultDir, status.DataDir) {
		t.Fatalf("默认目录应在数据目录内：dir=%s data=%s", status.DefaultDir, status.DataDir)
	}
	if filepath.Base(status.DefaultDir) != "ffmpeg" {
		t.Fatalf("默认目录应名为 ffmpeg，实际 %s", status.DefaultDir)
	}
	// 应提供下载源。
	if len(status.Sources) == 0 {
		t.Fatal("应提供至少一个下载源")
	}
	if status.Sources[0].ID == "" || status.Sources[0].URL == "" {
		t.Fatalf("下载源信息不完整: %+v", status.Sources[0])
	}
}

func TestBindingFFmpegRejectsBadPaths(t *testing.T) {
	app := newFrontendApp(t)
	cases := []struct {
		name    string
		input   string
		wantSub string
	}{
		{"空路径", "   ", "请先选择"},
		{"不存在", filepath.Join(t.TempDir(), "nope", "ffmpeg.exe"), "不存在"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.SetFFmpegPath(tc.input)
			if err == nil {
				t.Fatal("应返回错误")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误信息应包含 %q，实际：%v", tc.wantSub, err)
			}
		})
	}
}

func TestBindingFFmpegRejectsWrongFileName(t *testing.T) {
	app := newFrontendApp(t)
	// 选了一个不是 ffmpeg 的文件。
	file := filepath.Join(t.TempDir(), "notepad.exe")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	_, err := app.SetFFmpegPath(file)
	if err == nil || !strings.Contains(err.Error(), "请选择 ffmpeg.exe") {
		t.Fatalf("应拒绝非 ffmpeg 文件，实际：%v", err)
	}
}

func TestBindingFFmpegRejectsDirWithoutBinary(t *testing.T) {
	app := newFrontendApp(t)
	dir := t.TempDir()
	_, err := app.SetFFmpegPath(dir)
	if err == nil || !strings.Contains(err.Error(), "没有找到 ffmpeg.exe") {
		t.Fatalf("空目录应被拒绝，实际：%v", err)
	}
}

// resolveFFmpegInput 应能从目录里找到 bin/ffmpeg.exe。
func TestResolveFFmpegInputFindsInBinSubdir(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	target := filepath.Join(binDir, "ffmpeg.exe")
	if err := os.WriteFile(target, []byte("fake"), 0o644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 传上层目录，应自动定位到 bin/ffmpeg.exe。
	got, err := resolveFFmpegInput(root)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.EqualFold(got, target) {
		t.Fatalf("应解析为 %q，实际 %q", target, got)
	}
}

func TestBindingFFmpegSetAndClearWithRealBinary(t *testing.T) {
	ffmpegPath := strings.TrimSpace(os.Getenv("TRANSCRIBE_TEST_FFMPEG"))
	if ffmpegPath == "" {
		t.Skip("未设置 TRANSCRIBE_TEST_FFMPEG，跳过")
	}
	app := newFrontendApp(t)

	status, err := app.SetFFmpegPath(ffmpegPath)
	if err != nil {
		t.Fatalf("设置 FFmpeg 失败: %v", err)
	}
	if !status.Configured || !status.OK {
		t.Fatalf("设置后应可用: %+v", status)
	}
	// 应该已经持久化。
	if app.currentSettings().FFmpegPath == "" {
		t.Fatal("路径应写入设置")
	}
	// 环境探测也应看到它。
	if env := app.GetEnvironment(); !env.FFmpegOK {
		t.Fatalf("环境探测应报告可用: %+v", env)
	}

	// 清除后回到未安装。
	cleared, err := app.ClearFFmpegPath()
	if err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if cleared.Configured || cleared.OK {
		t.Fatalf("清除后应回到未安装: %+v", cleared)
	}
	if app.currentSettings().FFmpegPath != "" {
		t.Fatal("清除后设置里不应保留路径")
	}
}

func TestBindingFFmpegDownloadRejectsConcurrent(t *testing.T) {
	app := newFrontendApp(t)
	// 模拟已有下载在进行。
	ffmpegDownloadMu.Lock()
	ffmpegDownloading = true
	ffmpegDownloadMu.Unlock()
	t.Cleanup(func() {
		ffmpegDownloadMu.Lock()
		ffmpegDownloading = false
		ffmpegDownloadMu.Unlock()
	})

	_, err := app.DownloadFFmpeg(DownloadFFmpegInput{})
	if err == nil || !strings.Contains(err.Error(), "正在下载") {
		t.Fatalf("并发下载应被拒绝，实际：%v", err)
	}
}

// 未配置 FFmpeg 时，任务应给出明确的中文错误而不是静默失败。
func TestBindingTaskFailsClearlyWithoutFFmpeg(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "nofmpeg.mp4")
	task, err := app.CreateTask(CreateTaskInput{SourceFile: source, Model: "tiny"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.StartTask(task.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	// 等任务以失败结束。
	deadline := 10
	for i := 0; i < deadline*10; i++ {
		current, err := app.store.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("读取任务失败: %v", err)
		}
		if current.Status == repository.TaskStatusFailed {
			if !strings.Contains(current.Error, "FFmpeg") && !strings.Contains(current.Error, "ffmpeg") {
				t.Fatalf("失败原因应提到 FFmpeg，实际：%s", current.Error)
			}
			return
		}
		if current.Status == repository.TaskStatusCompleted {
			t.Fatal("没有 FFmpeg 时任务不应成功")
		}
		sleepMillis(50)
	}
	t.Fatal("任务未在预期时间内失败")
}

// sleepMillis 是 time.Sleep 的轻量封装，让轮询逻辑读起来更清楚。
func sleepMillis(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
