package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/repository"
)

// 用户显式配置的 FFmpeg 永远最优先；这条注释同样适用于自动发现失败时的回落逻辑。
const configuredFFmpegWins = true

// ffmpegSearchDirs 是自动发现 FFmpeg 时的常见安装位置。
//
// 只在用户没有显式配置 FFmpegPath 时使用；列表只提供候选，实际选择
// 由能力探测决定（subtitles 滤镜优先），而不是按目录顺序。
var ffmpegSearchDirs = []string{
	`C:\ffmpeg\bin`,
	`C:\Program Files\ffmpeg\bin`,
	`C:\Program Files (x86)\ffmpeg\bin`,
	`D:\ffmpeg\bin`,
	filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"),
	`C:\ProgramData\chocolatey\bin`,
	filepath.Join(os.Getenv("USERPROFILE"), "scoop", "shims"),
}

// selectFFmpeg 在候选路径里挑出能力最强的 FFmpeg 构建。
//
// 规则：
//  1. 用户在设置里显式指定的路径永远最优先（尊重用户选择，不做能力否决）；
//  2. 否则探测 PATH 与常见安装目录里的全部 ffmpeg.exe，
//     支持 subtitles（字幕压制）的构建胜出；
//  3. 没有任何构建支持压制时，选第一个可执行的；全都没有则返回 nil。
//
// 探测整体有超时，坏掉的构建不会拖慢应用启动。
func selectFFmpeg(ctx context.Context, configured string) *media.FFmpeg {
	candidates := ffmpegCandidates(configured)
	if len(candidates) == 0 {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var fallback *media.FFmpeg
	for _, candidate := range candidates {
		if probeCtx.Err() != nil {
			break
		}
		ffmpeg, err := media.New(candidate)
		if err != nil {
			continue
		}
		caps := ffmpeg.Available(probeCtx)
		if !caps.OK {
			continue
		}
		if fallback == nil {
			fallback = ffmpeg
		}
		if caps.SupportsSubtitlesBurn {
			// 第一个支持字幕压制的构建即最优，停止扫描。
			return ffmpeg
		}
	}
	return fallback
}

// ffmpegCandidates 汇总待探测的 ffmpeg 路径：用户配置（若指定）之外，
// 覆盖 PATH 上所有同名可执行文件与常见安装位置，按序去重。
func ffmpegCandidates(configured string) []string {
	ordered := make([]string, 0, 8)
	seen := make(map[string]bool)

	push := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		key := strings.ToLower(path)
		if seen[key] {
			return
		}
		seen[key] = true
		ordered = append(ordered, path)
	}

	// 用户显式配置时不做任何自动发现：配置什么就用什么。
	if strings.TrimSpace(configured) != "" {
		push(configured)
		return ordered
	}

	// LookPath 拿 PATH 上第一个命中（保底，也兼容类 Unix 无 .exe 后缀的命名）。
	if matches, err := exec.LookPath("ffmpeg"); err == nil {
		push(matches)
	}
	// PATH 目录逐个展开：同一台机器可能有多个 FFmpeg（例如精简构建与完整构建并存）。
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		push(filepath.Join(dir, "ffmpeg.exe"))
		push(filepath.Join(dir, "ffmpeg"))
	}
	for _, dir := range ffmpegSearchDirs {
		push(filepath.Join(dir, "ffmpeg.exe"))
	}
	return ordered
}

// deepScanFFmpeg 在磁盘上做一次限深扫描，寻找带 subtitles 滤镜的 FFmpeg 构建。
//
// 使用场景：PATH 与常见安装位置里的构建都是精简版（没有 libass）时，
// 很多便携工具包会自带完整构建（例如各类视频工具的 tools 目录），
// 把它们找出来复用，可以避免要求用户手动改设置。
//
// 安全阀：
//   - deadline 到达立即返回（扫描只做元数据遍历，通常数秒内完成）；
//   - 最多探测 maxProbe 个候选（能力探测要起子进程，不能无上限）；
//   - 找到第一个支持字幕压制的构建即返回。
//
// 没找到时返回 nil；调用方继续用 quick 选择的结果。
func deepScanFFmpeg(ctx context.Context, skip string) *media.FFmpeg {
	deadline := time.Now().Add(8 * time.Second)
	candidates := deepScanCandidates(deadline)
	if len(candidates) == 0 {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	for _, candidate := range candidates {
		if strings.EqualFold(candidate, skip) {
			continue
		}
		if probeCtx.Err() != nil || time.Now().After(deadline) {
			break
		}
		ffmpeg, err := media.New(candidate)
		if err != nil {
			continue
		}
		caps := ffmpeg.Available(probeCtx)
		if caps.OK && caps.SupportsSubtitlesBurn {
			return ffmpeg
		}
	}
	return nil
}

// deepScanCandidates 枚举盘符上的 ffmpeg.exe：限深遍历所有已存在的盘符根，
// 目录数有上限防失控。返回按发现顺序排列的绝对路径。
func deepScanCandidates(deadline time.Time) []string {
	const (
		// maxDepth 是允许「遍历内容」的最大目录深度（盘根下第一层为 1）。
		// 便携工具常放在 盘/工具目录/软件版本/tools 这样的第四层，
		// ffmpeg.exe 是该层目录里的文件，因此需要遍历到第 5 层入口。
		maxDepth      = 5
		maxDirs       = 60000
		maxCandidates = 24
	)

	// 跳过的目录：系统目录没有用户程序；AppData 由 quick 列表覆盖；
	// conda/Python 的固定结构体量大且自带的是精简构建，扫描纯属浪费配额。
	skipNames := map[string]bool{
		"Windows":                   true,
		"Program Files":             true,
		"Program Files (x86)":       true,
		"$Recycle.Bin":              true,
		"System Volume Information": true,
		"node_modules":              true,
		"AppData":                   true,
		"envs":                      true,
		"site-packages":             true,
		// Python / conda 运行时结构：体量巨大且不会包含完整版 FFmpeg。
		"Library":     true,
		"Scripts":     true,
		"Lib":         true,
		"DLLs":        true,
		"Include":     true,
		"Share":       true,
		"__pycache__": true,
		"mingw64":     true,
		"mingw32":     true,
		".git":        true,
	}

	var roots []string
	for _, drive := range []string{"C:", "D:", "E:", "F:"} {
		if info, err := os.Stat(drive + `\`); err == nil && info.IsDir() {
			roots = append(roots, drive+`\`)
		}
	}

	found := make([]string, 0, maxCandidates)
	visited := 0

	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		// 返回 true 表示应停止整个扫描。
		if time.Now().After(deadline) || visited >= maxDirs || len(found) >= maxCandidates {
			return true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		visited++
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				// depth 是当前目录深度：只有当「子目录」会超过 maxDepth 时才剪枝，
				// 保证第 maxDepth 层目录里的文件仍会被检查。
				if depth+1 > maxDepth || skipNames[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
					continue
				}
				if walk(filepath.Join(dir, name), depth+1) {
					return true
				}
				continue
			}
			if strings.EqualFold(name, "ffmpeg.exe") {
				found = append(found, filepath.Join(dir, name))
				if len(found) >= maxCandidates {
					return true
				}
			}
		}
		return false
	}

	for _, root := range roots {
		if walk(root, 1) {
			break
		}
	}
	return found
}

// upgradeBurnCapability 在当前 FFmpeg 缺少字幕压制能力时做一次深度扫描升级。
//
// 仅在用户没有手动配置 FFmpegPath 时执行，发现更强构建后会写回设置持久化，
// 下次启动直接命中，不再重复扫描。返回升级后的构建；未升级返回 nil。
func upgradeBurnCapability(ctx context.Context, current *media.FFmpeg, settings *repository.SettingsRecord) *media.FFmpeg {
	if settings == nil || strings.TrimSpace(settings.FFmpegPath) != "" {
		// 用户已显式配置：尊重用户选择，不扫描、不覆盖。
		return nil
	}
	if current != nil {
		if caps := current.Available(ctx); caps.OK && caps.SupportsSubtitlesBurn {
			return nil // 已经具备压制能力。
		}
	}

	upgraded := deepScanFFmpeg(ctx, currentPath(current))
	if upgraded == nil {
		return nil
	}

	settings.FFmpegPath = upgraded.Path()
	log.Printf("已自动切换到支持字幕压制的 FFmpeg 构建：%s", upgraded.Path())
	return upgraded
}

// currentPath 安全读取当前构建路径。
func currentPath(current *media.FFmpeg) string {
	if current == nil {
		return ""
	}
	return current.Path()
}
