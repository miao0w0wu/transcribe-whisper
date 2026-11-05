package asr

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// fasterWhisperScriptName 是 faster-whisper 工作脚本的文件名。
	fasterWhisperScriptName = "transcribe_worker.py"
	// funASRScriptName 是 FunASR 工作脚本的文件名。
	funASRScriptName = "funasr_worker.py"
	// transformersScriptName 是 transformers 工作脚本的文件名。
	transformersScriptName = "transformers_worker.py"
	// stderrLimit 是附加到错误信息中的 stderr 最大字符数。
	stderrLimit = 2000
	// defaultSegmentTimeout 是单次识别的默认超时时间（秒）。
	defaultSegmentTimeout = 6 * 60 * 60
	// probeTimeout 是引擎可用性检查的默认超时时间。
	probeTimeout = 60 * time.Second
	// transformersProbeTimeout 是 transformers 引擎可用性检查的超时。
	// 首次 import torch 明显比 faster-whisper 慢，需要更宽的窗口。
	transformersProbeTimeout = 120 * time.Second
)

// engineScripts 把 Python 工作脚本嵌入二进制，避免安装时漏拷脚本。
//
//go:embed assets/*.py
var engineScripts embed.FS

// embeddedScript 返回嵌入的工作脚本内容。
func embeddedScript(name string) ([]byte, error) {
	data, err := engineScripts.ReadFile("assets/" + name)
	if err != nil {
		return nil, fmt.Errorf("读取内嵌脚本 %s 失败：%v", name, err)
	}
	return data, nil
}

// resolveScript 返回可用的脚本路径。
//
// 优先使用调用方显式指定的 scriptPath；否则把内嵌脚本释放到缓存目录并复用。
func resolveScript(ctx context.Context, scriptPath, name string, dir string) (string, error) {
	if p := strings.TrimSpace(scriptPath); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("识别脚本不存在：%s", abs)
			}
			return "", fmt.Errorf("无法访问识别脚本 %s：%v", abs, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("识别脚本路径是目录而不是文件：%s", abs)
		}
		return abs, nil
	}

	data, err := embeddedScript(name)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	if existing, err := os.ReadFile(target); err == nil && string(existing) == string(data) {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建脚本缓存目录 %s 失败：%v", dir, err)
	}
	// 先写临时文件再改名，避免并发读取到半截脚本。
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("释放识别脚本到 %s 失败：%v", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("写入识别脚本 %s 失败：%v", target, err)
	}
	_ = ctx
	return target, nil
}

// resolvePython 返回可用的 Python 解释器路径。
//
// 优先级：调用方指定 > 环境变量 > PATH 中的 python。
func resolvePython(pythonPath string) (string, error) {
	if p := strings.TrimSpace(pythonPath); p != "" {
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			if err != nil && os.IsNotExist(err) {
				return "", fmt.Errorf("Python 解释器不存在：%s", p)
			}
			if err != nil {
				return "", fmt.Errorf("无法访问 Python 解释器 %s：%v", p, err)
			}
			return "", fmt.Errorf("Python 解释器路径是目录：%s", p)
		}
		return p, nil
	}
	for _, env := range []string{"TRANSCRIBE_PYTHON", "WHISPER_PYTHON"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if info, err := os.Stat(v); err == nil && !info.IsDir() {
				return v, nil
			}
		}
	}
	for _, name := range []string{"python", "python3"} {
		if found, err := exec.LookPath(name); err == nil {
			return found, nil
		}
	}
	return "", fmt.Errorf("未找到 Python 解释器，请在设置中指定 python.exe 路径")
}

// scriptCacheDir 返回用于释放内嵌脚本的缓存目录。
//
// 目录名带 Go 侧版本号，升级后不会命中旧脚本。
func scriptCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "transcribe-whisper", "scripts", "v1")
}

// requestPayload 是传给 Python 工作脚本的 JSON 入参，字段与 Request 对齐。
type requestPayload struct {
	AudioPath     string `json:"audio_path"`
	ModelPath     string `json:"model_path"`
	ModelName     string `json:"model_name"`
	Language      string `json:"language"`
	Device        string `json:"device"`
	ComputeType   string `json:"compute_type"`
	BeamSize      int    `json:"beam_size"`
	VadFilter     bool   `json:"vad_filter"`
	InitialPrompt string `json:"initial_prompt"`
	DownloadRoot  string `json:"download_root"`

	// 以下是提高识别准确性的可调参数。
	//
	// 除 ConditionOnPreviousText 外都带 omitempty：零值不出现在 JSON 里，
	// 脚本侧用 request.get(key) 为 None 来区分「未设置」与「显式设为 0」，
	// 从而正确套用自己的默认值。
	//
	// ConditionOnPreviousText 必须始终发送：本应用把它默认关掉以减少重复幻觉，
	// 若用 omitempty，false 会被吞掉，脚本就会回落到 faster-whisper 的 True 默认。
	VadThreshold              float64 `json:"vad_threshold,omitempty"`
	VadMinSilenceMs           int     `json:"vad_min_silence_ms,omitempty"`
	VadPaddingMs              int     `json:"vad_padding_ms,omitempty"`
	ConditionOnPreviousText   bool    `json:"condition_on_previous_text"`
	NoSpeechThreshold         float64 `json:"no_speech_threshold,omitempty"`
	CompressionRatioThreshold float64 `json:"compression_ratio_threshold,omitempty"`
}

// buildPayload 把 Request 转成脚本入参，并把 auto 之类的占位值规范化。
func buildPayload(req Request, defaultModel string) requestPayload {
	lang := strings.TrimSpace(req.Language)
	if strings.EqualFold(lang, "auto") {
		lang = ""
	}
	device := strings.TrimSpace(req.Device)
	if device == "" {
		device = "auto"
	}
	compute := strings.TrimSpace(req.ComputeType)
	if compute == "" {
		compute = "auto"
	}
	modelName := strings.TrimSpace(req.ModelName)
	if modelName == "" && strings.TrimSpace(req.ModelPath) == "" {
		modelName = defaultModel
	}
	beam := req.BeamSize
	if beam <= 0 {
		beam = 5
	}
	// 已下载到本地的模型优先，避免脚本再自己去联网下载一份。
	downloadRoot := strings.TrimSpace(req.DownloadRoot)
	if downloadRoot == "" {
		downloadRoot = scriptCacheDir()
	}
	return requestPayload{
		AudioPath:     strings.TrimSpace(req.AudioPath),
		ModelPath:     strings.TrimSpace(req.ModelPath),
		ModelName:     modelName,
		Language:      lang,
		Device:        device,
		ComputeType:   compute,
		BeamSize:      beam,
		VadFilter:     req.VadFilter,
		InitialPrompt: req.InitialPrompt,
		DownloadRoot:  downloadRoot,

		VadThreshold:              clampUnitFloat(req.VadThreshold),
		VadMinSilenceMs:           clampIntRange(req.VadMinSilenceMs, 0, 2000),
		VadPaddingMs:              clampIntRange(req.VadPaddingMs, 0, 5000),
		ConditionOnPreviousText:   req.ConditionOnPreviousText,
		NoSpeechThreshold:         clampUnitFloat(req.NoSpeechThreshold),
		CompressionRatioThreshold: clampFloatRange(req.CompressionRatioThreshold, 0, 10),
	}
}

// clampUnitFloat 把数值限制在 0..1；非正数表示「未设置」，返回 0。
func clampUnitFloat(v float64) float64 {
	return clampFloatRange(v, 0, 1)
}

// clampFloatRange 把数值限制在 [lo, hi]；输入非正数时按「未设置」返回 0。
//
// 之所以把「未设置」也映射到 0：脚本侧把 0 解释为「用自身默认值」，
// 这样 Go 侧零值请求不会把阈值意外压到下限。
func clampFloatRange(v, lo, hi float64) float64 {
	if v <= 0 {
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampIntRange 把数值限制在 [lo, hi]；非正数表示「未设置」，返回 0。
func clampIntRange(v, lo, hi int) int {
	if v <= 0 {
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// workerEvent 是 Python 工作脚本输出的单行事件。
type workerEvent struct {
	Type          string  `json:"type"`
	Ratio         float64 `json:"ratio"`
	Message       string  `json:"message"`
	ID            string  `json:"id"`
	Sequence      int     `json:"sequence"`
	Start         float64 `json:"start"`
	End           float64 `json:"end"`
	Text          string  `json:"text"`
	Language      string  `json:"language"`
	Confidence    float64 `json:"confidence"`
	LanguageScore float64 `json:"language_score"`
	Duration      float64 `json:"duration"`
	Model         string  `json:"model"`
	Engine        string  `json:"engine"`
	// RatioSet 用于区分脚本未给 ratio 的情况。
	RatioSet bool `json:"-"`
}

// UnmarshalJSON 记录 ratio 是否显式出现。
func (e *workerEvent) UnmarshalJSON(data []byte) error {
	type alias workerEvent
	var aux struct {
		*alias
		Ratio *float64 `json:"ratio"`
	}
	aux.alias = (*alias)(e)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Ratio != nil {
		e.Ratio = *aux.Ratio
		e.RatioSet = true
	}
	return nil
}

// runWorker 启动 Python 工作脚本，逐行解析 JSON 事件并回调 progress。
//
// 脚本通过 stdin 接收一行 JSON 入参，stdout 只输出协议事件，stderr 作为日志收集。
func runWorker(ctx context.Context, python, script string, payload []byte, engineName string, progress Progress) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, python, "-u", script)
	prepareCommand(cmd)
	cmd.Dir = filepath.Dir(script)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建识别进程输入管道失败：%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建识别进程输出管道失败：%v", err)
	}
	var stderrBuf safeBuffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动识别进程失败（%s）：%v", python, err)
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := stdin.Write(append(payload, '\n'))
		if closeErr := stdin.Close(); err == nil {
			err = closeErr
		}
		writeErr <- err
	}()

	result, scanErr := consumeEvents(stdout, engineName, progress)

	// 读完之后再等待，避免管道未读完就 Wait 造成死锁。
	waitErr := cmd.Wait()
	<-writeErr

	// 取消优先：ctx 结束时无论脚本是否正常收尾都报取消。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("识别已取消：%v", ctxErr)
	}
	if scanErr != nil {
		if errorsIsProtocol(scanErr) {
			return nil, scanErr
		}
		return nil, withStderr(scanErr, stderrBuf.String())
	}
	if waitErr != nil {
		return nil, withStderr(fmt.Errorf("识别进程异常退出：%v", waitErr), stderrBuf.String())
	}
	if result == nil {
		return nil, withStderr(fmt.Errorf("识别进程未返回结果（可能未输出 done 事件）"), stderrBuf.String())
	}
	if len(result.Segments) == 0 {
		// 没有识别结果不是错误，但要给出明确说明方便排查。
		notifyProgress(progress, 1, "识别完成，但没有识别到语音内容")
	}
	return result, nil
}

// protocolError 表示工作脚本返回的 error 事件。
type protocolError struct {
	Message string
	Stderr  string
}

func (e *protocolError) Error() string { return e.Message }

func errorsIsProtocol(err error) bool {
	var target *protocolError
	return errors.As(err, &target)
}

// consumeEvents 逐行解析 stdout 的 JSON 事件。
func consumeEvents(r io.Reader, engineName string, progress Progress) (*Result, error) {
	scanner := bufio.NewScanner(r)
	// 单行最多 16MB，避免极长字幕行触发默认 64KB 上限。
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var result *Result
	segments := make([]Segment, 0, 64)
	syntheticSeq := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event workerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// 脚本保证 stdout 只有 JSON，出现非 JSON 行说明脚本被污染，
			// 这里跳过但保留行内容方便排查。
			continue
		}
		switch strings.ToLower(event.Type) {
		case "progress":
			ratio := event.Ratio
			if !event.RatioSet {
				ratio = -1
			}
			notifyProgress(progress, clampRatio(ratio), event.Message)
		case "segment":
			syntheticSeq++
			seq := event.Sequence
			if seq <= 0 {
				seq = syntheticSeq
			}
			segments = append(segments, Segment{
				ID:         firstNonEmpty(event.ID, strconv.Itoa(seq)),
				Sequence:   seq,
				Start:      event.Start,
				End:        event.End,
				Text:       strings.TrimSpace(event.Text),
				Language:   event.Language,
				Confidence: event.Confidence,
			})
		case "done":
			result = &Result{
				Language:      event.Language,
				LanguageScore: event.LanguageScore,
				Duration:      event.Duration,
				Model:         event.Model,
				Engine:        firstNonEmpty(event.Engine, engineName),
			}
		case "error":
			msg := strings.TrimSpace(event.Message)
			if msg == "" {
				msg = "识别进程返回了未知错误"
			}
			return nil, &protocolError{Message: msg}
		default:
			// 未知事件类型忽略，方便脚本后续扩展。
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取识别进程输出失败：%v", err)
	}
	if result == nil {
		return nil, nil
	}
	result.Segments = normalizeSegments(segments)
	if result.Engine == "" {
		result.Engine = engineName
	}
	return result, nil
}

// normalizeSegments 按 sequence 排序并规范化时间与文本。
func normalizeSegments(segments []Segment) []Segment {
	out := make([]Segment, 0, len(segments))
	for _, seg := range segments {
		if seg.Start < 0 {
			seg.Start = 0
		}
		if seg.End < 0 {
			seg.End = 0
		}
		if seg.End < seg.Start {
			seg.End = seg.Start
		}
		if seg.Confidence < 0 {
			seg.Confidence = 0
		}
		out = append(out, seg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		return out[i].Start < out[j].Start
	})
	return out
}

// clampRatio 把进度比例限制在 -1..1 之间。
func clampRatio(r float64) float64 {
	if r < -1 {
		return -1
	}
	if r > 1 {
		return 1
	}
	return r
}

// notifyProgress 安全地回调进度：progress 可为 nil，回调 panic 不会影响识别流程。
func notifyProgress(progress Progress, ratio float64, message string) {
	if progress == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	progress(ratio, message)
}

// withStderr 在错误信息后附加脚本的 stderr 日志（截断）。
func withStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%v。识别进程日志：%s", err, truncateRunes(stderr, stderrLimit))
}

// truncateRunes 按字符截断，避免截断中文。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if limit <= 0 || len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// statFile 是 os.Stat 的中文错误包装，两个引擎共用。
func statFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("音频文件不存在：%s", path)
		}
		return nil, fmt.Errorf("无法访问音频文件 %s：%v", path, err)
	}
	return info, nil
}

// prepareCommand 让子进程在隐藏窗口中运行，并在取消时及时终止。
func prepareCommand(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 10 * time.Second
}

// safeBuffer 是可并发写入的缓冲区，用于收集子进程 stderr。
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 只保留有限长度，避免日志刷爆内存。
	if b.buf.Len() < stderrLimit*4 {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
