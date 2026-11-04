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

// FunASREngine 是基于 FunASR（AutoModel）的识别引擎。
//
// 与 faster-whisper 共用同一套 JSON Lines 子进程协议；
// 本机未安装 funasr 时 Available 返回 false，Transcribe 返回中文错误说明。
type FunASREngine struct {
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

var _ Engine = (*FunASREngine)(nil)

// NewFunASREngine 创建默认配置的 FunASR 引擎。
func NewFunASREngine() *FunASREngine {
	return &FunASREngine{}
}

// Name 返回引擎标识。
func (e *FunASREngine) Name() string { return "funasr" }

// Available 检查 Python、脚本与 funasr 是否就绪。
func (e *FunASREngine) Available(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "FunASR 引擎未初始化"
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
	script, err := resolveScript(ctx, e.ScriptPath, funASRScriptName, scriptDir)
	if err != nil {
		return false, err.Error()
	}

	key := "funasr|" + python + "|" + script
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

	cmd := exec.CommandContext(checkCtx, python, "-c", "import funasr")
	prepareCommand(cmd)
	out, runErr := cmd.CombinedOutput()

	ok := runErr == nil
	msg := ""
	switch {
	case ok:
		msg = "FunASR 已就绪（" + python + "）"
	case checkCtx.Err() != nil:
		msg = fmt.Sprintf("检查 FunASR 超时（超过 %s），请稍后重试", timeout)
	default:
		msg = fmt.Sprintf("Python 环境中未安装 FunASR：%s（解释器：%s）。本机未安装时请改用 faster-whisper，或执行 pip install funasr",
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
func (e *FunASREngine) RefreshAvailability(ctx context.Context) (bool, string) {
	if e == nil {
		return false, "FunASR 引擎未初始化"
	}
	e.mu.Lock()
	e.cacheKey = ""
	e.cacheOK = false
	e.cacheMsg = ""
	e.mu.Unlock()
	return e.Available(ctx)
}

// CheckAudio 校验待识别音频文件。
func (e *FunASREngine) CheckAudio(audioPath string) error {
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
func (e *FunASREngine) Transcribe(ctx context.Context, req Request, progress Progress) (*Result, error) {
	if e == nil {
		return nil, errors.New("FunASR 引擎未初始化")
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
	script, err := resolveScript(ctx, firstNonEmpty(req.ScriptPath, e.ScriptPath), funASRScriptName, scriptDir)
	if err != nil {
		return nil, err
	}

	payload := buildPayload(req, "paraformer-zh")
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

	notifyProgress(progress, -1, "正在启动 FunASR ...")
	return runWorker(runCtx, python, script, raw, e.Name(), progress)
}
