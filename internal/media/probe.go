package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// defaultSampleRate 是语音识别推荐采样率。
	defaultSampleRate = 16000
	// defaultChannels 是语音识别推荐声道数（单声道）。
	defaultChannels = 1
	// stderrLimit 是附加到错误信息中的 stderr 最大字符数。
	stderrLimit = 500
	// capsProbeTimeout 是能力探测命令的超时时间。
	capsProbeTimeout = 30 * time.Second
)

// ErrFFmpegNotFound 表示系统中找不到可用的 ffmpeg 可执行文件。
var ErrFFmpegNotFound = errors.New("未找到 ffmpeg 可执行文件，请先安装 FFmpeg 或在设置中指定路径")

// FFmpeg 封装本机 FFmpeg / FFprobe 的调用。
//
// 该类型可安全并发使用；内部只缓存探测到的一次能力结果。
type FFmpeg struct {
	ffmpegPath  string
	ffprobePath string

	mu       sync.Mutex
	caps     *Capabilities
	probeErr error
}

var _ Burner = (*FFmpeg)(nil)

// New 创建 FFmpeg 封装。ffmpegPath 为空时在 PATH 中查找。
func New(ffmpegPath string) (*FFmpeg, error) {
	path := strings.TrimSpace(ffmpegPath)
	if path == "" {
		found, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil, fmt.Errorf("%w（查找命令：ffmpeg，原因：%v）", ErrFFmpegNotFound, err)
		}
		path = found
	} else {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("指定的 ffmpeg 路径不存在：%s", path)
		}
		return nil, fmt.Errorf("无法访问 ffmpeg 路径 %s：%v", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("指定的 ffmpeg 路径是目录而不是可执行文件：%s", path)
	}

	return &FFmpeg{ffmpegPath: path, ffprobePath: locateFFprobe(path)}, nil
}

// Path 返回当前使用的 ffmpeg 可执行文件路径。
func (f *FFmpeg) Path() string {
	if f == nil {
		return ""
	}
	return f.ffmpegPath
}

// FFprobePath 返回探测到的 ffprobe 路径；为空表示不可用。
func (f *FFmpeg) FFprobePath() string {
	if f == nil {
		return ""
	}
	return f.ffprobePath
}

// locateFFprobe 优先在 ffmpeg 同目录查找 ffprobe（带/不带 .exe 都试），其次在 PATH 中查找。
func locateFFprobe(ffmpegPath string) string {
	if dir := filepath.Dir(ffmpegPath); dir != "" {
		for _, name := range []string{"ffprobe", "ffprobe.exe", "ffprobe.cmd", "ffprobe.bat"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	if found, err := exec.LookPath("ffprobe"); err == nil {
		return found
	}
	return ""
}

// Available 汇报本机 FFmpeg 能力。
//
// 无论成功与否都会返回一个结果结构（OK 字段表示是否可用），便于前端展示诊断信息。
func (f *FFmpeg) Available(ctx context.Context) Capabilities {
	if f == nil {
		return Capabilities{OK: false, Detail: ErrFFmpegNotFound.Error()}
	}

	f.mu.Lock()
	if f.caps != nil {
		caps := *f.caps
		f.mu.Unlock()
		return caps
	}
	if f.probeErr != nil {
		err := f.probeErr
		f.mu.Unlock()
		return Capabilities{Path: f.ffmpegPath, OK: false, Detail: err.Error()}
	}
	f.mu.Unlock()

	caps, err := f.detectCapabilities(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.probeErr = err
	} else {
		f.caps = &caps
	}
	return caps
}

// RefreshCapabilities 清除缓存后重新探测，用于用户切换 ffmpeg 路径或重装之后。
func (f *FFmpeg) RefreshCapabilities(ctx context.Context) Capabilities {
	if f == nil {
		return Capabilities{OK: false, Detail: ErrFFmpegNotFound.Error()}
	}
	f.mu.Lock()
	f.caps = nil
	f.probeErr = nil
	f.mu.Unlock()
	return f.Available(ctx)
}

func (f *FFmpeg) detectCapabilities(ctx context.Context) (Capabilities, error) {
	caps := Capabilities{Path: f.ffmpegPath}

	callCtx, cancel := context.WithTimeout(ctx, capsProbeTimeout)
	defer cancel()

	versionOut, err := f.run(callCtx, "-hide_banner", "-version")
	if err != nil {
		caps.OK = false
		caps.Detail = fmt.Sprintf("FFmpeg 无法执行，请确认安装完整：%v", err)
		return caps, errors.New(caps.Detail)
	}
	version, build := parseFFmpegVersion(string(versionOut))
	caps.Version = version

	filtersOut, filterErr := f.run(callCtx, "-hide_banner", "-filters")
	if filterErr == nil {
		caps.SupportsSubtitlesBurn = hasFilter(string(filtersOut), "subtitles")
		caps.SupportsDrawText = hasFilter(string(filtersOut), "drawtext")
	}

	caps.OK = true
	switch {
	case filterErr != nil:
		caps.Detail = "已检测到 FFmpeg，但读取滤镜列表失败，无法判断字幕压制能力：" + filterErr.Error()
	case caps.SupportsSubtitlesBurn:
		caps.Detail = "FFmpeg 可用，支持 subtitles 滤镜，可以压制字幕"
	default:
		caps.Detail = "FFmpeg 可用，但缺少 subtitles 滤镜（未编译 libass），无法压制字幕"
	}
	if !caps.SupportsDrawText {
		caps.Detail = strings.TrimRight(caps.Detail, "。") + "；同时缺少 drawtext 滤镜"
	}
	if build != "" {
		caps.Detail += "（" + build + "）"
	}
	return caps, nil
}

var ffmpegVersionPattern = regexp.MustCompile(`(?m)^ffmpeg version (\S+)`)

// parseFFmpegVersion 从版本输出中提取版本号与构建标记。
func parseFFmpegVersion(output string) (version, build string) {
	if m := ffmpegVersionPattern.FindStringSubmatch(output); len(m) == 2 {
		version = m[1]
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "built with ") || strings.HasPrefix(line, "configuration:") {
			if build != "" {
				build += "；"
			}
			build += line
		}
	}
	build = truncateChars(build, 200)
	return version, build
}

var filterColumnPattern = regexp.MustCompile(`^([\s.TSACXBD|]{3,})\s+(\S+)\s`)

// hasFilter 判断 -filters 输出中是否存在指定滤镜。
//
// 输出形如「T.C drawtext  V->V  Draw text ...」，中间列是滤镜名。
func hasFilter(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, " \r")
		if line == "" {
			continue
		}
		m := filterColumnPattern.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		if strings.EqualFold(m[2], name) {
			return true
		}
	}
	return false
}

// ffprobeOutput 是 ffprobe JSON 输出的最小子集。
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	CodecType  string            `json:"codec_type"`
	CodecName  string            `json:"codec_name"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	SampleRate string            `json:"sample_rate"`
	Channels   int               `json:"channels"`
	Duration   string            `json:"duration"`
	Dispos     map[string]int    `json:"disposition"`
	Tags       map[string]string `json:"tags"`
}

type ffprobeFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
	Size       string `json:"size"`
}

// Probe 读取媒体元数据。
//
// 找不到 ffprobe 时不会失败返回：会返回 Info{Duration: 0} 以及一个说明原因的 error，
// 调用方据 Info.HasVideo / HasAudio 判断文件是否至少能被 ffmpeg 识别。
func (f *FFmpeg) Probe(ctx context.Context, inputPath string) (Info, error) {
	info := Info{Path: inputPath}
	if f == nil {
		return info, ErrFFmpegNotFound
	}
	path := strings.TrimSpace(inputPath)
	if path == "" {
		return info, errors.New("媒体文件路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		info.Path = abs
	}
	stat, statErr := os.Stat(info.Path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return info, fmt.Errorf("媒体文件不存在：%s", info.Path)
		}
		return info, fmt.Errorf("无法访问媒体文件 %s：%v", info.Path, statErr)
	}
	if stat.IsDir() {
		return info, fmt.Errorf("指定的路径是目录而不是媒体文件：%s", info.Path)
	}
	if stat.Size() == 0 {
		return info, fmt.Errorf("媒体文件为空（0 字节），无法读取元数据：%s", info.Path)
	}

	if f.ffprobePath == "" {
		basics, probeErr := f.probeBasic(info.Path)
		if probeErr != nil {
			return info, fmt.Errorf("未找到 ffprobe，且用 ffmpeg 读取媒体信息失败：%v", probeErr)
		}
		return basics, fmt.Errorf("未找到 ffprobe，无法读取完整媒体元数据（时长等信息缺失）：%s", info.Path)
	}

	out, err := f.runFFprobe(ctx, f.ffprobePath,
		"-hide_banner", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", info.Path)
	if err != nil {
		return info, fmt.Errorf("使用 ffprobe 读取 %s 失败：%v", info.Path, err)
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
		return info, fmt.Errorf("解析 ffprobe 输出失败：%v，原始输出片段：%s", err, truncateChars(string(out), stderrLimit))
	}

	info.Duration = firstPositiveFloat(parsed.Format.Duration)
	info.BitRate = firstPositiveInt(parsed.Format.BitRate)
	info.FormatName = strings.TrimSpace(parsed.Format.FormatName)

	for _, stream := range parsed.Streams {
		switch strings.ToLower(stream.CodecType) {
		case "video":
			// 跳过封面图、缩略图等附加画面流。
			if isAttachedPicture(stream) || stream.Width <= 0 || stream.Height <= 0 {
				continue
			}
			if !info.HasVideo {
				info.HasVideo = true
				info.VideoCodec = stream.CodecName
				info.Width = stream.Width
				info.Height = stream.Height
			}
		case "audio":
			if !info.HasAudio {
				info.HasAudio = true
				info.AudioCodec = stream.CodecName
				info.SampleRate = atoiSafe(stream.SampleRate)
				info.Channels = stream.Channels
			}
		}
	}

	if info.Duration <= 0 {
		// 某些容器（例如分段 TS）在 format 层没有总时长，回退到流级别。
		for _, stream := range parsed.Streams {
			if d := firstPositiveFloat(stream.Duration); d > info.Duration {
				info.Duration = d
			}
		}
	}
	if info.FormatName == "" && len(parsed.Streams) == 0 {
		return info, fmt.Errorf("该文件似乎不是有效的音视频文件（ffprobe 未识别出任何流）：%s", info.Path)
	}
	return info, nil
}

func isAttachedPicture(stream ffprobeStream) bool {
	if stream.Dispos != nil && stream.Dispos["attached_pic"] == 1 {
		return true
	}
	if stream.Tags != nil {
		if strings.EqualFold(stream.Tags["comment"], "Cover") || strings.EqualFold(stream.Tags["title"], "Cover") {
			return true
		}
	}
	return false
}

// probeBasic 在缺少 ffprobe 时用 ffmpeg -i 的 stderr 粗略判断音视频流是否存在。
func (f *FFmpeg) probeBasic(path string) (Info, error) {
	info := Info{Path: path}
	out, err := f.run(context.Background(), "-hide_banner", "-nostdin", "-i", path)
	text := string(out)
	// ffmpeg 在没有指定输出时必然以非 0 退出，这里只关心它打印的流信息。
	info.HasVideo = strings.Contains(text, "Video: ")
	info.HasAudio = strings.Contains(text, "Audio: ")
	if err != nil && !info.HasVideo && !info.HasAudio {
		return info, err
	}
	return info, nil
}

// Extract 从媒体文件提取单声道 WAV 音频，默认 16000Hz / 1 声道。
func (f *FFmpeg) Extract(ctx context.Context, req ExtractRequest) error {
	if f == nil {
		return ErrFFmpegNotFound
	}
	input := strings.TrimSpace(req.InputPath)
	output := strings.TrimSpace(req.OutputPath)
	if input == "" || output == "" {
		return errors.New("音频提取失败：输入路径与输出路径都不能为空")
	}
	if stat, err := os.Stat(input); err != nil || stat.IsDir() {
		if err == nil {
			return fmt.Errorf("音频提取失败：输入路径是目录而不是文件：%s", input)
		}
		if os.IsNotExist(err) {
			return fmt.Errorf("音频提取失败：输入文件不存在：%s", input)
		}
		return fmt.Errorf("音频提取失败：无法访问输入文件 %s：%v", input, err)
	}

	sampleRate := req.SampleRate
	if sampleRate <= 0 {
		sampleRate = defaultSampleRate
	}
	channels := req.Channels
	if channels <= 0 {
		channels = defaultChannels
	}

	if dir := filepath.Dir(output); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建输出目录 %s 失败：%v", dir, err)
		}
	}

	if _, err := f.run(ctx, extractArgs(input, output, sampleRate, channels)...); err != nil {
		return fmt.Errorf("音频提取失败：%v", err)
	}
	if stat, err := os.Stat(output); err != nil || stat.Size() == 0 {
		return fmt.Errorf("音频提取失败：FFmpeg 未生成有效输出文件 %s（请确认源文件含有音频轨）", output)
	}
	return nil
}

// extractArgs 拼装提取音频的参数，单独抽出便于单元测试。
func extractArgs(input, output string, sampleRate, channels int) []string {
	return []string{
		"-y",
		"-hide_banner",
		"-nostdin",
		"-i", input,
		"-vn",
		"-ac", strconv.Itoa(channels),
		"-ar", strconv.Itoa(sampleRate),
		"-acodec", "pcm_s16le",
		output,
	}
}

// run 执行 ffmpeg，返回合并后的输出（stdout + stderr）。
func (f *FFmpeg) run(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, f.ffmpegPath, args...)
	prepareCommand(cmd)

	// 注意：stdout 与 stderr 必须用两个独立的 Buffer。
	// os/exec 发现两者指向同一个 Writer 时会共用一根管道，
	// 对于 cmd.exe 内置命令（如 type）会导致内容丢失。
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := append(append([]byte{}, stdout.Bytes()...), stderr.Bytes()...)
	if err == nil {
		return out, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, fmt.Errorf("操作已取消：%v", ctxErr)
	}
	return out, fmt.Errorf("FFmpeg 执行失败（%s）：%s", err.Error(), summarizeFFmpegError(out))
}

// runFFprobe 执行 ffprobe，只返回 stdout。
func (f *FFmpeg) runFFprobe(ctx context.Context, ffprobePath string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	prepareCommand(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("操作已取消：%v", ctxErr)
	}
	return nil, fmt.Errorf("FFprobe 执行失败（%s）：%s", err.Error(), summarizeFFmpegError(stderr.Bytes()))
}

// summarizeFFmpegError 提取 ffmpeg 输出中最有用的错误行并截断。
func summarizeFFmpegError(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "未获得任何输出信息"
	}
	lines := strings.Split(text, "\n")
	picked := make([]string, 0, 3)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Error") || strings.HasPrefix(line, "Conversion failed") ||
			strings.HasPrefix(line, "Invalid") || strings.HasPrefix(line, "Unknown") ||
			strings.Contains(line, "No such file") || strings.Contains(line, "not found") ||
			strings.Contains(line, "Permission denied") {
			picked = append(picked, line)
		}
	}
	if len(picked) == 0 {
		// 没有显式错误行时取输出末尾，那里通常是真正的失败原因。
		start := len(lines) - 3
		if start < 0 {
			start = 0
		}
		for _, line := range lines[start:] {
			if line = strings.TrimSpace(line); line != "" {
				picked = append(picked, line)
			}
		}
	}
	return truncateChars(strings.Join(picked, " | "), stderrLimit)
}

// truncateChars 按「字符」截断，避免切断中文 UTF-8 序列。
func truncateChars(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f)
	}
	return 0
}

func firstPositiveFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return v
}

func firstPositiveInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
		return v
	}
	if f := firstPositiveFloat(s); f > 0 {
		return int64(f)
	}
	return 0
}
