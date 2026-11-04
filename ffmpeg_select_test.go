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

// 本机已知有两套 FFmpeg：WhisperJAV 的精简构建（无 subtitles）和
// jasna 工具包里的 gyan full 构建（有 subtitles）。自动发现必须选中后者。
func TestSelectFFmpegPrefersSubtitlesCapableBuild(t *testing.T) {
	selected := selectFFmpeg(context.Background(), "")
	if selected == nil {
		t.Skip("本机没有探测到任何可用 FFmpeg，跳过")
	}
	caps := selected.Available(context.Background())
	if !caps.OK {
		t.Fatalf("选中的构建应可用: %+v", caps)
	}
	if !caps.SupportsSubtitlesBurn {
		// quick 只覆盖 PATH 与常见目录；此时深度扫描必须能救回来。
		upgraded := deepScanFFmpeg(context.Background(), caps.Path)
		if upgraded == nil {
			t.Fatalf("quick 与深度扫描都没找到支持压制的构建（quick 选中 %s）", caps.Path)
		}
		ucaps := upgraded.Available(context.Background())
		if !ucaps.SupportsSubtitlesBurn {
			t.Fatalf("深度扫描结果应支持压制: %+v", ucaps)
		}
		t.Logf("quick 选中的是精简构建，深度扫描升级为 %s", upgraded.Path())
	}
}

// 深度扫描有硬性时限，不能无限期占用启动时间。
func TestDeepScanRespectsDeadline(t *testing.T) {
	deadline := time.Now().Add(2 * time.Second)
	candidates := deepScanCandidates(deadline)
	if time.Since(deadline) > 1500*time.Millisecond {
		t.Fatalf("扫描应在 deadline 附近收敛，实际超时 %v", time.Since(deadline))
	}
	for _, c := range candidates {
		if !strings.EqualFold(filepath.Base(c), "ffmpeg.exe") {
			t.Fatalf("候选应都是 ffmpeg.exe: %s", c)
		}
	}
	t.Logf("深度扫描发现 %d 个 ffmpeg.exe", len(candidates))
}

// 升级路径：当前构建无压制能力 + 未配置路径 → 扫描升级并回写设置。
func TestUpgradeBurnCapabilityUpgradesAndPersists(t *testing.T) {
	app := NewApp()
	defer app.shutdown(context.Background())

	settings := app.currentSettings()
	settings.FFmpegPath = "" // 模拟用户未配置
	upgraded := upgradeBurnCapability(context.Background(), app.media, &settings)
	if upgraded == nil {
		t.Skip("当前构建已支持压制或深度扫描无收获，无需升级")
	}
	if settings.FFmpegPath == "" {
		t.Fatal("升级后应把新路径写回设置以便持久化")
	}
	caps := upgraded.Available(context.Background())
	if !caps.SupportsSubtitlesBurn {
		t.Fatalf("升级结果应支持压制: %+v", caps)
	}
}

// 用户显式配置过路径时，升级逻辑必须完全让位。
func TestUpgradeBurnCapabilitySkipsWhenConfigured(t *testing.T) {
	app := NewApp()
	defer app.shutdown(context.Background())

	weak := repository.SettingsRecord{FFmpegPath: `D:\somewhere\ffmpeg.exe`}
	if upgraded := upgradeBurnCapability(context.Background(), app.media, &weak); upgraded != nil {
		t.Fatalf("用户已配置路径时不应升级: %s", upgraded.Path())
	}
}

// 用户显式配置的路径必须原样胜出，即使它能力较弱——这是用户的选择。
func TestSelectFFmpegRespectsConfiguredPath(t *testing.T) {
	// 用一个肯定存在的候选（PATH 上的 ffmpeg），验证配置优先于自动发现。
	configured := ""
	if matches, err := lookAllFFmpeg(); err == nil && matches != "" {
		configured = matches
	} else {
		t.Skip("本机 PATH 上没有 ffmpeg，跳过")
	}
	selected := selectFFmpeg(context.Background(), configured)
	if selected == nil {
		t.Fatal("显式配置时应返回该路径的构建")
	}
	if !strings.EqualFold(selected.Path(), configured) && !strings.EqualFold(filepath.Base(selected.Path()), filepath.Base(configured)) {
		t.Fatalf("应使用用户配置的路径 %s，实际 %s", configured, selected.Path())
	}
}

// 候选列表：显式配置时只有一条；自动发现时至少包含 PATH 上的 ffmpeg。
func TestFFmpegCandidates(t *testing.T) {
	explicit := ffmpegCandidates(`D:\custom\ffmpeg.exe`)
	if len(explicit) != 1 || explicit[0] != `D:\custom\ffmpeg.exe` {
		t.Fatalf("显式配置时只应有一条候选: %v", explicit)
	}

	auto := ffmpegCandidates("")
	found := false
	for _, c := range auto {
		if strings.EqualFold(filepath.Base(c), "ffmpeg.exe") {
			found = true
		}
	}
	if !found {
		t.Fatalf("自动发现应包含 PATH 上的 ffmpeg: %v", auto)
	}
	// 去重：同一路径（大小写不同）不应重复。
	seen := make(map[string]int)
	for _, c := range auto {
		seen[strings.ToLower(c)]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Fatalf("候选列表应去重: %s 出现 %d 次", path, count)
		}
	}
}

// GetEnvironment 报告的路径应与装配时选中的构建一致，界面才不会显示错误信息。
func TestGetEnvironmentReportsSelectedFFmpeg(t *testing.T) {
	app := NewApp()
	defer app.shutdown(context.Background())

	status := app.GetEnvironment()
	if app.media == nil {
		if status.FFmpegOK {
			t.Fatal("装配失败时不应报告 FFmpeg 可用")
		}
		return
	}
	if !status.FFmpegOK {
		t.Fatalf("装配成功的构建应报告可用: %+v", status)
	}
	if !strings.EqualFold(status.FFmpegPath, app.media.Path()) {
		t.Fatalf("环境报告的路径 %s 应与选中的 %s 一致", status.FFmpegPath, app.media.Path())
	}
	// 自动发现未配置时，选中的路径应回写进设置，设置页才能展示真实值。
	if settings := app.currentSettings(); settings.FFmpegPath == "" {
		t.Fatal("自动发现的 FFmpeg 路径应写回设置")
	}
}

// 辅助：拿到 PATH 上第一个 ffmpeg（测试用）。
func lookAllFFmpeg() (string, error) {
	return lookPathFFmpeg()
}

func lookPathFFmpeg() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "ffmpeg.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
