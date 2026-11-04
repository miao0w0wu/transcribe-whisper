package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FasterWhisperEngine 是基于 faster-whisper Python 包的识别引擎。
//
// 依赖：Python 3.9+ 与 faster_whisper；通过 JSON Lines 子进程协议通信。
type FasterWhisperEngine struct {
	// PythonPath 指定 python.exe；为空时按环境变量与 PATH 查找。
	PythonPath string
	// ScriptPath 指定外部工作脚本；为空时使用内嵌脚本。
	ScriptPath string
	// ScriptDir 是内嵌脚本的释放目录；为空时使用用户缓存目录。
	ScriptDir string
	// ProbeTimeout 是可用性检查超时；为零时使用 60 秒。
	ProbeTimeout time.Duration

	mu       sync.Mutex
	cacheKey string
	cacheOK  bool
	cacheMsg string
}

var _ Engine = (*FasterWhisperEngine)(nil)

// NewFasterWhisperEngine 创建默认配置的 faster-whisper 引擎。
func NewFasterWhisperEngine() *FasterWhisperEngine {
	return &FasterWhisperEngine{}
}

// Name 返回引擎标识。
func (e *FasterWhisperEngine) Name() string { return "faster-whisper" }

// Available 检查 Python、脚本与 faster_whisper 是否就绪。
//
// 结果会缓存，避免每帧都执行一次 python -c；需要强制复查时调用 RefreshAvailability。
func (e *FasterWhisperEngine) Available(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "faster-whisper 引擎未初始化"
	}
	if ctx == nil {
		ctx = context.Background()
	}

	python, err := resolvePython(e.PythonPath)
	if err != nil {
		return false, err.Error()
	}
	scriptDir := e.ScriptDir
	if strings.TrimSpace(scriptDir) == "" {
		scriptDir = scriptCacheDir()
	}
	script, err := resolveScript(ctx, e.ScriptPath, fasterWhisperScriptName, scriptDir)
	if err != nil {
		return false, err.Error()
	}

	key := python + "|" + script
	e.mu.Lock()
	if e.cacheKey == key {
		ok, msg := e.cacheOK, e.cacheMsg
		e.mu.Unlock()
		return ok, msg
	}
	e.mu.Unlock()

	timeout := e.ProbeTimeout
	if timeout <= 0 {
		timeout = probeTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, python, "-c", "import faster_whisper")
	prepareCommand(cmd)
	out, runErr := cmd.CombinedOutput()

	ok := runErr == nil
	msg := ""
	switch {
	case ok:
		msg = "faster-whisper 已就绪（" + python + "）"
	case checkCtx.Err() != nil:
		msg = fmt.Sprintf("检查 faster-whisper 超时（超过 %s），首次导入可能较慢，请稍后重试", timeout)
	default:
		msg = fmt.Sprintf("Python 环境中未安装 faster-whisper：%s（解释器：%s）。请执行 pip install faster-whisper",
			truncateRunes(strings.TrimSpace(string(out)), 300), python)
	}

	e.mu.Lock()
	e.cacheKey = key
	e.cacheOK = ok
	e.cacheMsg = msg
	e.mu.Unlock()
	return ok, msg
}

// RefreshAvailability 清除缓存后重新检查。
func (e *FasterWhisperEngine) RefreshAvailability(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "faster-whisper 引擎未初始化"
	}
	e.mu.Lock()
	e.cacheKey = ""
	e.cacheOK = false
	e.cacheMsg = ""
	e.mu.Unlock()
	return e.Available(ctx)
}

// CheckAudio 校验待识别音频文件，返回明确的中文错误。
func (e *FasterWhisperEngine) CheckAudio(audioPath string) error {
	path := strings.TrimSpace(audioPath)
	if path == "" {
		return errors.New("未指定音频文件路径")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("音频文件不存在：%s", path)
		}
		return fmt.Errorf("无法访问音频文件 %s：%v", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("音频路径是目录而不是文件：%s", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("音频文件为空（0 字节）：%s", path)
	}
	return nil
}

// Transcribe 执行识别，逐行解析 JSON Lines 并回调 progress（可为 nil）。
func (e *FasterWhisperEngine) Transcribe(ctx context.Context, req Request, progress Progress) (*Result, error) {
	if e == nil {
		return nil, errors.New("faster-whisper 引擎未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.CheckAudio(req.AudioPath); err != nil {
		return nil, err
	}

	python, err := resolvePython(firstNonEmpty(req.PythonPath, e.PythonPath))
	if err != nil {
		return nil, err
	}
	scriptDir := e.ScriptDir
	if strings.TrimSpace(scriptDir) == "" {
		scriptDir = scriptCacheDir()
	}
	script, err := resolveScript(ctx, firstNonEmpty(req.ScriptPath, e.ScriptPath), fasterWhisperScriptName, scriptDir)
	if err != nil {
		return nil, err
	}

	payload := buildPayload(req, "small")
	// 模型路径统一成绝对路径，避免子进程工作目录不同导致找不到模型。
	if payload.ModelPath != "" {
		if abs, absErr := filepath.Abs(payload.ModelPath); absErr == nil {
			payload.ModelPath = abs
		}
	}
	if payload.AudioPath != "" {
		if abs, absErr := filepath.Abs(payload.AudioPath); absErr == nil {
			payload.AudioPath = abs
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化识别参数失败：%v", err)
	}

	timeout := req.SegmentTimeout
	if timeout <= 0 {
		timeout = defaultSegmentTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	notifyProgress(progress, -1, "正在启动 faster-whisper ...")
	return runWorker(runCtx, python, script, raw, e.Name(), progress)
}
