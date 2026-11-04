package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrSubtitlesUnsupported 表示当前 FFmpeg 不具备字幕压制能力。
var ErrSubtitlesUnsupported = errors.New("当前 FFmpeg 未编译 libass，无法压制字幕（缺少 subtitles 滤镜）")

// ErrNoDrawTextFilter 表示 drawtext 滤镜同样不可用，无法走降级绘制路径。
var ErrNoDrawTextFilter = errors.New("当前 FFmpeg 缺少 drawtext 滤镜，无法用 ASS 文本或 drawtext 降级绘制字幕")

// prepareCommand 让子进程在隐藏窗口中运行，并保证取消时能杀掉整个进程树。
func prepareCommand(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
	}
	// 取消时先温和终止，超时后再强杀，避免子进程残留。
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
}

// Burn 把字幕压制进视频（满足 Burner 接口）。
//
// 优先使用 subtitles 滤镜；当前 FFmpeg 缺少该滤镜时返回明确的中文错误，
// 不会假装成功。
func (f *FFmpeg) Burn(ctx context.Context, req BurnRequest) error {
	if f == nil {
		return ErrFFmpegNotFound
	}
	video := strings.TrimSpace(req.VideoPath)
	subtitle := strings.TrimSpace(req.SubtitlePath)
	output := strings.TrimSpace(req.OutputPath)
	if video == "" || subtitle == "" || output == "" {
		return errors.New("字幕压制失败：视频、字幕与输出路径都不能为空")
	}

	for _, item := range []struct{ label, path string }{
		{"视频", video},
		{"字幕", subtitle},
	} {
		stat, err := os.Stat(item.path)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("字幕压制失败：%s文件不存在：%s", item.label, item.path)
			}
			return fmt.Errorf("字幕压制失败：无法访问%s文件 %s：%v", item.label, item.path, err)
		}
		if stat.IsDir() {
			return fmt.Errorf("字幕压制失败：%s路径是目录而不是文件：%s", item.label, item.path)
		}
	}

	videoAbs, err := filepath.Abs(video)
	if err != nil {
		videoAbs = video
	}
	subtitleAbs, err := filepath.Abs(subtitle)
	if err != nil {
		subtitleAbs = subtitle
	}
	if dir := filepath.Dir(output); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建输出目录 %s 失败：%v", dir, err)
		}
	}

	caps := f.Available(ctx)
	if !caps.SupportsSubtitlesBurn {
		if !caps.SupportsDrawText {
			return fmt.Errorf("%w；%w（FFmpeg：%s）", ErrSubtitlesUnsupported, ErrNoDrawTextFilter, caps.Path)
		}
		return fmt.Errorf("%w（FFmpeg：%s）。请更换带 libass 的 FFmpeg 版本后重试", ErrSubtitlesUnsupported, caps.Path)
	}

	args := burnArgs(videoAbs, subtitleAbs, output, req)
	if _, err := f.run(ctx, args...); err != nil {
		return fmt.Errorf("字幕压制失败：%v", err)
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Size() == 0 {
		return fmt.Errorf("字幕压制失败：FFmpeg 未生成有效输出文件 %s", output)
	}
	return nil
}

// burnArgs 拼装字幕压制参数，单独抽出便于单元测试。
func burnArgs(video, subtitle, output string, req BurnRequest) []string {
	filter := "subtitles='" + escapeFilterPath(subtitle) + "'"

	// 字体、字号、边距通过 force_style 传递给 libass。
	styles := make([]string, 0, 3)
	if name := strings.TrimSpace(req.FontName); name != "" {
		styles = append(styles, "FontName="+escapeStyleValue(name))
	}
	if req.FontSize > 0 {
		styles = append(styles, "FontSize="+strconv.Itoa(req.FontSize))
	}
	if req.MarginV >= 0 {
		styles = append(styles, "MarginV="+strconv.Itoa(req.MarginV))
	}
	if len(styles) > 0 {
		filter += ":force_style='" + strings.Join(styles, ",") + "'"
	}

	return []string{
		"-y",
		"-hide_banner",
		"-nostdin",
		"-i", video,
		"-vf", filter,
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", "20",
		"-c:a", "copy",
		"-movflags", "+faststart",
		output,
	}
}

// escapeFilterPath 把 Windows 路径转成 subtitles 滤镜可接受的写法：
// 反斜杠换成正斜杠，再按 FFmpeg 滤镜规则转义特殊字符。
func escapeFilterPath(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	return escapeFilterValue(p)
}

// escapeFilterValue 按 FFmpeg 滤镜参数的两级转义规则处理：
// 第一级（选项值）转义 \、'、:、,、[、]、;，第二级（滤镜图）再转义 \ 与 '。
func escapeFilterValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '\'', ':', ',', '[', ']', ';':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	once := b.String()
	var out strings.Builder
	out.Grow(len(once) + 8)
	for _, r := range once {
		if r == '\\' || r == '\'' {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// escapeStyleValue 处理 force_style 内部的取值，避免逗号和冒号破坏样式串。
func escapeStyleValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "")
	s = strings.ReplaceAll(s, ",", " ")
	s = strings.ReplaceAll(s, ":", " ")
	s = strings.ReplaceAll(s, "'", "")
	return strings.TrimSpace(s)
}
