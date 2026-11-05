package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcribe-whisper/internal/media"
)

// 应用已改为「FFmpeg 完全手动指定」，本文件验证新的行为契约：
//   - 未配置时明确判定为未安装（不再自动发现）；
//   - 配置了可用路径时正常识别能力；
//   - 配置了错误路径时如实报告，不静默降级。

func TestResolveFFmpegUnconfiguredReturnsNil(t *testing.T) {
	// 关键契约：不配置就是 nil，绝不偷偷从 PATH 里找一个。
	for _, configured := range []string{"", "   "} {
		if got := resolveFFmpeg(context.Background(), configured); got != nil {
			t.Fatalf("未配置时应返回 nil，实际 %v", got)
		}
	}
}

func TestResolveFFmpegInvalidPathReturnsNil(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "not-exist", "ffmpeg.exe")
	if got := resolveFFmpeg(context.Background(), bogus); got != nil {
		t.Fatalf("不存在的路径应返回 nil，实际 %v", got)
	}
}

func TestResolveFFmpegUsesConfiguredPath(t *testing.T) {
	ffmpegPath := strings.TrimSpace(os.Getenv("TRANSCRIBE_TEST_FFMPEG"))
	if ffmpegPath == "" {
		t.Skip("未设置 TRANSCRIBE_TEST_FFMPEG，跳过需要真实 FFmpeg 的用例")
	}
	got := resolveFFmpeg(context.Background(), ffmpegPath)
	if got == nil {
		t.Fatalf("配置的 FFmpeg 应可用：%s", ffmpegPath)
	}
	caps := got.Available(context.Background())
	if !caps.OK {
		t.Fatalf("FFmpeg 无法执行：%s", caps.Detail)
	}
	if !strings.EqualFold(caps.Path, ffmpegPath) && !strings.Contains(strings.ToLower(caps.Path), "ffmpeg") {
		t.Fatalf("返回路径异常：%s", caps.Path)
	}
}

func TestInspectFFmpegUnconfigured(t *testing.T) {
	status := inspectFFmpeg(context.Background(), "")
	if status.Configured {
		t.Fatal("未配置时 Configured 应为 false")
	}
	if status.OK {
		t.Fatal("未配置时 OK 应为 false")
	}
	if !strings.Contains(status.Detail, "尚未指定") {
		t.Fatalf("应给出中文提示，实际：%s", status.Detail)
	}
}

func TestInspectFFmpegInvalidPath(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "ffmpeg.exe")
	status := inspectFFmpeg(context.Background(), bogus)
	if !status.Configured {
		t.Fatal("填了路径就应视为已配置")
	}
	if status.OK {
		t.Fatal("不可用的路径不应标记为可用")
	}
	if !strings.Contains(status.Detail, "不可用") {
		t.Fatalf("应说明路径不可用，实际：%s", status.Detail)
	}
}

func TestInspectFFmpegReal(t *testing.T) {
	ffmpegPath := strings.TrimSpace(os.Getenv("TRANSCRIBE_TEST_FFMPEG"))
	if ffmpegPath == "" {
		t.Skip("未设置 TRANSCRIBE_TEST_FFMPEG，跳过需要真实 FFmpeg 的用例")
	}
	status := inspectFFmpeg(context.Background(), ffmpegPath)
	if !status.Configured || !status.OK {
		t.Fatalf("真实 FFmpeg 应可用：%+v", status)
	}
	if status.Version == "" {
		t.Log("该构建未报告版本号")
	}
	t.Logf("FFmpeg 状态：version=%s canBurn=%v detail=%s", status.Version, status.CanBurn, status.Detail)
}

func TestInspectFFmpegIsPureReadOnly(t *testing.T) {
	// inspectFFmpeg 不该修改任何全局状态：连续调用结果应一致。
	first := inspectFFmpeg(context.Background(), "")
	second := inspectFFmpeg(context.Background(), "")
	if first != second {
		t.Fatalf("两次探测结果不一致：%+v vs %+v", first, second)
	}
}

// 编译期确认 media 包仍被本文件引用（避免导入漂移）。
var _ = media.ErrFFmpegNotFound
