package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TransformersEngine 是基于 Hugging Face transformers 的识别引擎。
//
// 与 faster-whisper 共用同一套 JSON Lines 子进程协议，区别在于加载的是
// safetensors + 自定义建模代码的模型（例如 cohere-transcribe-03-2026），
// 走 AutoProcessor + AutoModelForSpeechSeq2Seq，而不是 CTranslate2。
type TransformersEngine struct {
	// PythonPath 指定 python.exe；为空时按环境变量与 PATH 查找。
	PythonPath string
	// ScriptPath 指定外部工作脚本；为空时使用内嵌脚本。
	ScriptPath string
	// ScriptDir 是内嵌脚本的释放目录；为空时使用用户缓存目录。
	ScriptDir string
	// ProbeTimeout 是可用性检查超时；为零时使用 60 秒。
	// transformers 首次导入比 faster-whisper 重，这里默认放宽。
	ProbeTimeout time.Duration

	mu       sync.Mutex
	cacheKey string
	cacheOK  bool
	cacheMsg string
}

var _ Engine = (*TransformersEngine)(nil)

// NewTransformersEngine 创建默认配置的 transformers 引擎。
func NewTransformersEngine() *TransformersEngine {
	return &TransformersEngine{}
}

// Name 返回引擎标识。
func (e *TransformersEngine) Name() string { return "transformers" }

// probeCommand 是可用性检查用的导入语句。
//
// 一次性检查 torch 与 transformers 两个依赖，避免分两次启动解释器。
const transformersProbeScript = "import torch, transformers; print(transformers.__version__)"

// Available 检查 Python、脚本与 torch/transformers 是否就绪。
func (e *TransformersEngine) Available(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "transformers 引擎未初始化"
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
	script, err := resolveScript(ctx, e.ScriptPath, transformersScriptName, scriptDir)
	if err != nil {
		return false, err.Error()
	}

	key := "transformers|" + python + "|" + script
	e.mu.Lock()
	if e.cacheKey == key {
		ok, msg := e.cacheOK, e.cacheMsg
		e.mu.Unlock()
		return ok, msg
	}
	e.mu.Unlock()

	timeout := e.ProbeTimeout
	if timeout <= 0 {
		timeout = transformersProbeTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, python, "-c", transformersProbeScript)
	prepareCommand(cmd)
	out, runErr := cmd.CombinedOutput()

	ok := runErr == nil
	msg := ""
	switch {
	case ok:
		version := strings.TrimSpace(string(out))
		if version == "" {
			msg = "transformers 已就绪（" + python + "）"
		} else {
			msg = "transformers " + version + " 已就绪（" + python + "）"
		}
	case checkCtx.Err() != nil:
		msg = fmt.Sprintf("检查 transformers 超时（超过 %s），首次导入 torch 可能较慢，请稍后重试", timeout)
	default:
		msg = fmt.Sprintf("Python 环境中未安装 torch 或 transformers：%s（解释器：%s）。请执行 pip install torch \"transformers>=5.4.0\"",
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
func (e *TransformersEngine) RefreshAvailability(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "transformers 引擎未初始化"
	}
	e.mu.Lock()
	e.cacheKey = ""
	e.cacheOK = false
	e.cacheMsg = ""
	e.mu.Unlock()
	return e.Available(ctx)
}

// CheckAudio 校验待识别音频文件。
func (e *TransformersEngine) CheckAudio(audioPath string) error {
	path := strings.TrimSpace(audioPath)
	if path == "" {
		return errors.New("未指定音频文件路径")
	}
	info, err := statFile(path)
	if err != nil {
		return err
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
func (e *TransformersEngine) Transcribe(ctx context.Context, req Request, progress Progress) (*Result, error) {
	if e == nil {
		return nil, errors.New("transformers 引擎未初始化")
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
	script, err := resolveScript(ctx, firstNonEmpty(req.ScriptPath, e.ScriptPath), transformersScriptName, scriptDir)
	if err != nil {
		return nil, err
	}

	// cohere 这类模型没有内建的 whiper 式模型名，ModelName 只是给错误信息用的。
	payload := buildPayload(req, "cohere-transcribe")
	if payload.ModelPath != "" {
		if abs, absErr := filepath.Abs(payload.ModelPath); absErr == nil {
			payload.ModelPath = abs
		}
	}
	if abs, absErr := filepath.Abs(payload.AudioPath); absErr == nil {
		payload.AudioPath = abs
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

	notifyProgress(progress, -1, "正在启动 transformers ...")
	return runWorker(runCtx, python, script, raw, e.Name(), progress)
}
