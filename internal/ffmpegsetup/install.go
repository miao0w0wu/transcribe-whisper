// Package ffmpegsetup 负责把 FFmpeg 下载并解压到应用目录下。
//
// 设计前提：本应用不再自动发现系统里的 FFmpeg，而是要求用户明确指定。
// 为了降低门槛，提供「一键下载完整版」：下载官方推荐的 gyan.dev 完整构建
// （自带 libass，可压制字幕），解压到数据目录下的 ffmpeg/。
package ffmpegsetup

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 下载源。按顺序尝试，前一个失败自动降级到下一个。
//
// 两个源都提供 Windows x64 完整构建（含 libass / subtitles 滤镜）：
//   - gyan.dev 的 release-essentials：体积小（约 115MB），日常够用；
//   - BtbN 的 win64-gpl：约 200MB，包含更多编码器，作为兜底。
var defaultSources = []Source{
	{
		ID:      "gyan",
		Label:   "gyan.dev 完整版",
		URL:     "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
		Note:    "官方推荐的 Windows 构建，体积较小，自带 libass",
		License: "GPL",
	},
	{
		ID:      "btbn",
		Label:   "BtbN win64-gpl",
		URL:     "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip",
		Note:    "备选源，包含更多编码器，体积更大",
		License: "GPL",
	},
}

// Source 描述一个可下载的 FFmpeg 发行包。
type Source struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	URL     string `json:"url"`
	Note    string `json:"note"`
	License string `json:"license"`
}

// DefaultSources 返回内置下载源列表（按优先级排序）。
func DefaultSources() []Source {
	return append([]Source(nil), defaultSources...)
}

// Progress 是下载 / 解压进度回调，ratio 取 0..1，message 为中文说明。
type Progress func(ratio float64, message string)

// InstallOptions 是安装参数。
type InstallOptions struct {
	// Dir 是安装目录，最终 ffmpeg.exe 会落在 <Dir>/bin/ffmpeg.exe。
	// 为空时使用 <dataDir>/ffmpeg。
	Dir string
	// SourceID 指定下载源；为空时按内置顺序尝试。
	SourceID string
	// HTTPClient 可注入自定义客户端（测试用）。
	HTTPClient *http.Client
}

// Result 是一次安装的结果。
type Result struct {
	// FFmpegPath 是安装好的 ffmpeg.exe 绝对路径。
	FFmpegPath string
	// FFprobePath 是 ffprobe.exe 绝对路径（可能为空）。
	FFprobePath string
	// Dir 是安装目录。
	Dir string
	// Source 是实际使用的下载源 ID。
	Source string
}

// 下载与解压的默认参数。
const (
	// downloadTimeout 是整体下载超时。整包 100~200MB，给足余量。
	downloadTimeout = 30 * time.Minute
	// maxDownloadBytes 是解压前的体积上限，防止异常响应写满磁盘。
	maxDownloadBytes = 1024 * 1024 * 1024 // 1GB
	// progressInterval 是进度回调的最小间隔。
	progressInterval = 200 * time.Millisecond
	// downloadBufferSize 是下载缓冲区大小。
	downloadBufferSize = 256 * 1024
)

// ErrNoSources 表示没有可用的下载源。
var ErrNoSources = errors.New("没有可用的 FFmpeg 下载源")

// Install 下载并解压 FFmpeg 到指定目录。
//
// 流程：
//  1. 逐个尝试下载源，把 zip 存到临时文件；
//  2. 从 zip 里挑出 bin/ffmpeg.exe 与 bin/ffprobe.exe（发行包的顶层目录名
//     每次都不同，所以按文件名匹配而不是固定路径）；
//  3. 解压到 <Dir>/bin/，覆盖旧文件；
//  4. 返回实际路径。
//
// 失败时会清理临时文件，不会留下半个安装。
func Install(ctx context.Context, opts InstallOptions, progress Progress) (*Result, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, errors.New("未指定 FFmpeg 安装目录")
	}
	absDir, err := filepath.Abs(dir)
	if err == nil {
		dir = absDir
	}

	sources := DefaultSources()
	if id := strings.TrimSpace(opts.SourceID); id != "" {
		filtered := make([]Source, 0, 1)
		for _, s := range sources {
			if strings.EqualFold(s.ID, id) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("未知的下载源：%s", id)
		}
		sources = filtered
	}
	if len(sources) == 0 {
		return nil, ErrNoSources
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 FFmpeg 安装目录失败：%v", err)
	}

	var lastErr error
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report := func(ratio float64, msg string) {
			if progress == nil {
				return
			}
			// 多个源时按源数量分摊进度，让用户知道是在重试。
			scale := 1.0 / float64(len(sources))
			progress((float64(index)+ratio)*scale, msg)
		}
		result, err := installFrom(ctx, client, source, dir, report)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("下载 FFmpeg 失败：%w", lastErr)
}

// installFrom 用单个源安装。
func installFrom(ctx context.Context, client *http.Client, source Source, dir string, report Progress) (*Result, error) {
	report(0, "正在连接 "+source.Label+" …")

	archivePath, err := downloadArchive(ctx, client, source, dir, report)
	if err != nil {
		return nil, err
	}
	defer os.Remove(archivePath)

	report(0.85, "正在解压 …")
	ffmpegPath, ffprobePath, err := extractBinaries(archivePath, dir)
	if err != nil {
		return nil, err
	}
	report(1, "FFmpeg 安装完成")

	return &Result{
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
		Dir:         dir,
		Source:      source.ID,
	}, nil
}

// downloadArchive 把发行包下载到安装目录旁边的临时文件，返回其路径。
func downloadArchive(ctx context.Context, client *http.Client, source Source, dir string, report Progress) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求失败：%v", err)
	}
	req.Header.Set("User-Agent", "transcribe-whisper/1.0 (+ffmpeg-setup)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接 %s 失败：%v", source.Label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s 返回 HTTP %d", source.Label, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dir, "ffmpeg-*.zip.part")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败：%v", err)
	}
	tmpPath := tmp.Name()
	// 失败时由调用方删除；这里保证句柄关闭。
	defer tmp.Close()

	total := resp.ContentLength
	if total > maxDownloadBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("发行包体积异常（%d 字节），已中止", total)
	}

	buf := make([]byte, downloadBufferSize)
	var written int64
	lastReport := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				_ = os.Remove(tmpPath)
				return "", fmt.Errorf("写入临时文件失败：%v", werr)
			}
			written += int64(n)
			if written > maxDownloadBytes {
				_ = os.Remove(tmpPath)
				return "", fmt.Errorf("下载体积超过上限（%d 字节），已中止", maxDownloadBytes)
			}
			if time.Since(lastReport) >= progressInterval {
				lastReport = time.Now()
				ratio := 0.0
				if total > 0 {
					ratio = float64(written) / float64(total) * 0.85
				}
				report(ratio, fmt.Sprintf("已下载 %.1f / %.1f MB", float64(written)/1048576, float64(total)/1048576))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("下载中断：%v", readErr)
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("刷新临时文件失败：%v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("关闭临时文件失败：%v", err)
	}
	return tmpPath, nil
}

// extractBinaries 从 zip 里挑出 ffmpeg.exe 与 ffprobe.exe，解压到 <dir>/bin/。
//
// 发行包的顶层目录名带版本号（例如 ffmpeg-8.0-essentials_build/），
// 因此按文件名匹配，不能写死路径。解压时只取这两个文件，其余（文档、
// 示例、presets）不落盘，省空间也避免误导。
func extractBinaries(archivePath, dir string) (ffmpegPath, ffprobePath string, err error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("打开压缩包失败：%v", err)
	}
	defer reader.Close()

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建 bin 目录失败：%v", err)
	}

	// 同名的 .exe 可能有多个（32/64 位目录），优先取路径里含 x64/win64 的。
	type candidate struct {
		file *zip.File
		rank int
	}
	var ffmpegCandidates, ffprobeCandidates []candidate
	rankOf := func(name string) int {
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "win64"), strings.Contains(lower, "x64"), strings.Contains(lower, "amd64"):
			return 0
		case strings.Contains(lower, "win32"), strings.Contains(lower, "x86"):
			return 2
		default:
			return 1
		}
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(filepath.Base(file.Name))
		switch base {
		case "ffmpeg.exe", "ffmpeg":
			ffmpegCandidates = append(ffmpegCandidates, candidate{file: file, rank: rankOf(file.Name)})
		case "ffprobe.exe", "ffprobe":
			ffprobeCandidates = append(ffprobeCandidates, candidate{file: file, rank: rankOf(file.Name)})
		}
	}
	if len(ffmpegCandidates) == 0 {
		return "", "", errors.New("压缩包里没有找到 ffmpeg 可执行文件，下载源可能已变更")
	}
	sort.SliceStable(ffmpegCandidates, func(i, j int) bool { return ffmpegCandidates[i].rank < ffmpegCandidates[j].rank })
	sort.SliceStable(ffprobeCandidates, func(i, j int) bool { return ffprobeCandidates[i].rank < ffprobeCandidates[j].rank })

	ffmpegPath = filepath.Join(binDir, filepath.Base(ffmpegCandidates[0].file.Name))
	if err := extractOne(ffmpegCandidates[0].file, ffmpegPath); err != nil {
		return "", "", err
	}
	if len(ffprobeCandidates) > 0 {
		ffprobePath = filepath.Join(binDir, filepath.Base(ffprobeCandidates[0].file.Name))
		if err := extractOne(ffprobeCandidates[0].file, ffprobePath); err != nil {
			return "", "", err
		}
	}
	return ffmpegPath, ffprobePath, nil
}

// extractOne 解压单个文件到目标路径。
func extractOne(file *zip.File, dst string) error {
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("读取压缩包内 %s 失败：%v", file.Name, err)
	}
	defer src.Close()

	// 先写临时文件再改名，避免中途失败留下半截可执行文件。
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建 %s 失败：%v", dst, err)
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("写入 %s 失败：%v", dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭 %s 失败：%v", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("重命名 %s 失败：%v", dst, err)
	}
	return nil
}
