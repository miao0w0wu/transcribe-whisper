package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"transcribe-whisper/internal/ffmpegsetup"
)

// eventFFmpegProgress 是 FFmpeg 下载进度事件名。
const eventFFmpegProgress = "ffmpeg:progress"

// ffmpegDownloadMu 保护「同一时刻只允许一个下载」的判断。
var ffmpegDownloadMu sync.Mutex

// ffmpegDownloading 表示当前是否有下载在进行。
var ffmpegDownloading bool

// GetFFmpegStatus 返回 FFmpeg 的安装状态，供界面决定是否提示用户下载。
//
// 判断依据是用户配置了什么，不做任何自动发现：未配置就是未安装。
func (a *App) GetFFmpegStatus() FFmpegStatus {
	settings := a.currentSettings()
	a.mu.RLock()
	dataDir := a.dataDir
	ffmpeg := a.media
	a.mu.RUnlock()

	status := FFmpegStatus{
		Configured: strings.TrimSpace(settings.FFmpegPath) != "",
		Path:       strings.TrimSpace(settings.FFmpegPath),
		DefaultDir: defaultFFmpegDir(dataDir),
		DataDir:    dataDir,
		Sources:    ffmpegSources(),
	}
	ffmpegDownloadMu.Lock()
	status.Downloading = ffmpegDownloading
	ffmpegDownloadMu.Unlock()

	if !status.Configured {
		status.Detail = "尚未安装 FFmpeg。音频提取、语音识别与字幕压制都依赖它，请先下载完整版或选择本机已有的 ffmpeg.exe。"
		return status
	}
	if ffmpeg == nil {
		status.Detail = "配置的 FFmpeg 路径不可用，请重新选择或重新下载。"
		return status
	}
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), 20*time.Second)
	defer cancel()
	caps := ffmpeg.Available(ctx)
	status.OK = caps.OK
	status.Version = caps.Version
	status.CanBurn = caps.SupportsSubtitlesBurn
	status.Path = caps.Path
	switch {
	case !caps.OK:
		status.Detail = "配置的 FFmpeg 无法执行，请重新选择或重新下载。"
	case !caps.SupportsSubtitlesBurn:
		status.Detail = "FFmpeg 可用，但缺少 subtitles 滤镜（未编译 libass），字幕压制不可用。建议下载完整版。"
	default:
		status.Detail = caps.Detail
	}
	return status
}

// ffmpegSources 把下载源转成前端结构。
func ffmpegSources() []FFmpegSource {
	sources := ffmpegsetup.DefaultSources()
	out := make([]FFmpegSource, 0, len(sources))
	for _, s := range sources {
		out = append(out, FFmpegSource{ID: s.ID, Label: s.Label, URL: s.URL, Note: s.Note, License: s.License})
	}
	return out
}

// PickFFmpegDirectory 弹出目录选择框，返回用户选中的目录。
//
// 会尝试把自己定位到默认安装目录，方便用户确认位置。
func (a *App) PickFFmpegDirectory() (string, error) {
	if a.ctx == nil {
		return "", errors.New("当前环境不支持目录对话框，请直接填写本机绝对路径")
	}
	a.mu.RLock()
	dataDir := a.dataDir
	a.mu.RUnlock()
	options := wailsruntime.OpenDialogOptions{Title: "选择 FFmpeg 安装目录"}
	if dir := defaultFFmpegDir(dataDir); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			options.DefaultDirectory = dir
		}
	}
	path, err := wailsruntime.OpenDirectoryDialog(a.ctx, options)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

// PickFFmpegFile 弹出文件选择框，返回 ffmpeg.exe 的绝对路径。
func (a *App) PickFFmpegFile() (string, error) {
	if a.ctx == nil {
		return "", errors.New("当前环境不支持文件对话框，请直接填写本机绝对路径")
	}
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择 ffmpeg.exe",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "可执行文件", Pattern: "ffmpeg.exe;ffmpeg"},
			{DisplayName: "全部文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

// DownloadFFmpeg 下载并安装 FFmpeg，进度通过 ffmpeg:progress 推送。
//
// 安装完成后自动写回设置并重建媒体依赖，用户不需要再手动保存。
func (a *App) DownloadFFmpeg(input DownloadFFmpegInput) (*FFmpegStatus, error) {
	ffmpegDownloadMu.Lock()
	if ffmpegDownloading {
		ffmpegDownloadMu.Unlock()
		return nil, errors.New("FFmpeg 正在下载中，请等待完成")
	}
	ffmpegDownloading = true
	ffmpegDownloadMu.Unlock()

	// 无论成功失败都要复位标志。
	release := func() {
		ffmpegDownloadMu.Lock()
		ffmpegDownloading = false
		ffmpegDownloadMu.Unlock()
	}

	a.mu.RLock()
	dataDir := a.dataDir
	a.mu.RUnlock()

	dir := strings.TrimSpace(input.Dir)
	if dir == "" {
		dir = defaultFFmpegDir(dataDir)
	}
	if dir == "" {
		release()
		return nil, errors.New("无法确定 FFmpeg 安装目录")
	}

	ctx := context.WithoutCancel(a.contextOrBackground())
	go func() {
		defer release()
		result, err := ffmpegsetup.Install(ctx, ffmpegsetup.InstallOptions{
			Dir:      dir,
			SourceID: strings.TrimSpace(input.SourceID),
		}, func(ratio float64, message string) {
			a.emitFFmpegProgress(FFmpegProgressPayload{
				Progress: int(ratio * 100),
				Message:  message,
			})
		})
		if err != nil {
			a.emitFFmpegProgress(FFmpegProgressPayload{Error: err.Error()})
			return
		}
		// 安装成功后写回设置并立刻生效。
		if applyErr := a.applyFFmpegPath(result.FFmpegPath); applyErr != nil {
			a.emitFFmpegProgress(FFmpegProgressPayload{Error: "FFmpeg 已解压但无法启用：" + applyErr.Error()})
			return
		}
		a.emitFFmpegProgress(FFmpegProgressPayload{Progress: 100, Done: true, Message: "FFmpeg 安装完成", Path: result.FFmpegPath})
	}()

	return &FFmpegStatus{Downloading: true, DefaultDir: dir}, nil
}

// SetFFmpegPath 手动指定 ffmpeg.exe 路径并立即生效。
//
// 传目录时会自动在目录里找 ffmpeg.exe（含 bin/ 子目录），省得用户点到文件。
func (a *App) SetFFmpegPath(path string) (*FFmpegStatus, error) {
	resolved, err := resolveFFmpegInput(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	if err := a.applyFFmpegPath(resolved); err != nil {
		return nil, err
	}
	status := a.GetFFmpegStatus()
	return &status, nil
}

// ClearFFmpegPath 清除已配置的 FFmpeg 路径（回到「未安装」状态）。
func (a *App) ClearFFmpegPath() (*FFmpegStatus, error) {
	if err := a.applyFFmpegPath(""); err != nil {
		return nil, err
	}
	status := a.GetFFmpegStatus()
	return &status, nil
}

// resolveFFmpegInput 把用户输入（文件或目录）解析成 ffmpeg.exe 绝对路径。
func resolveFFmpegInput(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("请先选择 ffmpeg.exe 或它所在的目录")
	}
	abs, err := filepath.Abs(value)
	if err == nil {
		value = abs
	}
	info, statErr := os.Stat(value)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", fmt.Errorf("路径不存在：%s", value)
		}
		return "", fmt.Errorf("无法访问路径 %s：%v", value, statErr)
	}
	if !info.IsDir() {
		// 直接给了文件：必须是 ffmpeg 可执行文件。
		base := strings.ToLower(filepath.Base(value))
		if base != "ffmpeg.exe" && base != "ffmpeg" {
			return "", fmt.Errorf("请选择 ffmpeg.exe，当前选的是 %s", filepath.Base(value))
		}
		return value, nil
	}

	// 给了目录：依次找 ffmpeg.exe / bin/ffmpeg.exe。
	for _, candidate := range []string{
		filepath.Join(value, "ffmpeg.exe"),
		filepath.Join(value, "ffmpeg"),
		filepath.Join(value, "bin", "ffmpeg.exe"),
		filepath.Join(value, "bin", "ffmpeg"),
	} {
		if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("目录里没有找到 ffmpeg.exe：%s（可尝试选择它的 bin 子目录）", value)
}

// applyFFmpegPath 把新的 FFmpeg 路径写进设置、重建依赖并同步给队列。
func (a *App) applyFFmpegPath(path string) error {
	store, err := a.storeHandle()
	if err != nil {
		return err
	}
	ctx := a.contextOrBackground()

	ffmpeg, status := applyFFmpegSettings(ctx, path)
	if strings.TrimSpace(path) != "" && ffmpeg == nil {
		return errors.New(status.Detail)
	}

	current, err := store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	current.FFmpegPath = strings.TrimSpace(path)
	if err := store.SaveSettings(ctx, current); err != nil {
		return err
	}

	a.mu.Lock()
	a.settings = current
	a.media = ffmpeg
	queue := a.queue
	a.mu.Unlock()

	// 队列持有的是装配时的引用，路径变了要同步过去，否则现有任务仍用旧的。
	if queue != nil {
		queue.SetMedia(ffmpeg)
	}
	return nil
}

// emitFFmpegProgress 推送 FFmpeg 下载进度事件。
func (a *App) emitFFmpegProgress(payload FFmpegProgressPayload) {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, eventFFmpegProgress, payload)
}
