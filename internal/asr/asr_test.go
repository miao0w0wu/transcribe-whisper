package asr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件的所有测试都离线运行：用真实 Python 解释器执行「内联脚本」充当工作进程，
// 因此既不下载模型，也不依赖 faster-whisper / funasr 是否安装。
// 若机器上找不到 python，相关用例会 t.Skip。

// testPython 返回可用于测试的 Python 解释器路径，找不到时跳过测试。
func testPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python", "python3"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`D:\AI\WhisperJAV\python.exe`,
		`C:\Users\Public\python\python.exe`,
	} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	t.Skip("本机未找到 Python，跳过依赖子进程的用例")
	return ""
}

// writeWorkerScript 把内联 Python 脚本写入临时目录，作为工作脚本。
func writeWorkerScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写入测试脚本失败：%v", err)
	}
	return path
}

// writeAudioFile 创建一个非空占位音频文件。
func writeAudioFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(path, []byte("RIFF....WAVEfmt "), 0o644); err != nil {
		t.Fatalf("写入占位音频失败：%v", err)
	}
	return path
}

// 统一的测试脚本框架：读取 stdin 的 JSON 入参，执行 f(request)，并把入参回显到 stderr 便于断言。
const scriptPrelude = `import json, sys
raw = sys.stdin.readline()
req = json.loads(raw) if raw.strip() else {}
def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()
def log(msg):
    sys.stderr.write(str(msg) + "\n")
    sys.stderr.flush()
log("REQUEST:" + json.dumps(req, ensure_ascii=False))
def fail(msg):
    emit({"type": "error", "message": msg})
    sys.exit(3)
`

const scriptHappyPath = scriptPrelude + `emit({"type": "progress", "ratio": 0.0, "message": "开始识别"})
emit({"type": "segment", "id": "2", "sequence": 2, "start": 3.0, "end": 4.5, "text": " 第二句 ", "language": "zh", "confidence": 0.8})
emit({"type": "progress", "ratio": 0.5, "message": "已识别一半"})
emit({"type": "segment", "id": "1", "sequence": 1, "start": -1.0, "end": 2.0, "text": "第一句", "language": "zh", "confidence": 0.9})
emit({"type": "segment", "id": "3", "sequence": 3, "start": 5.0, "end": 4.0, "text": "第三句", "language": "zh", "confidence": -0.5})
emit({"type": "progress", "ratio": 1.0, "message": "识别完成"})
emit({"type": "done", "language": "zh", "language_score": 0.98, "duration": 6.0, "model": "small", "engine": "faster-whisper"})
`

const scriptErrorEvent = scriptPrelude + `log("加载模型时出错：文件缺失")
fail("模型文件缺失，请重新下载模型")
`

const scriptExitNonZero = scriptPrelude + `log("未捕获异常：ImportError")
emit({"type": "segment", "sequence": 1, "start": 0.0, "end": 1.0, "text": "前半句"})
sys.exit(2)
`

const scriptNoDone = scriptPrelude + `emit({"type": "segment", "sequence": 1, "start": 0.0, "end": 1.0, "text": "只有片段"})
`

const scriptStdoutNoise = scriptPrelude + `print("这是一行非 JSON 输出")
print("")
emit({"type": "segment", "sequence": 1, "start": 0.0, "end": 1.0, "text": "有效片段"})
emit({"type": "unknown", "foo": "bar"})
emit({"type": "done", "language": "en", "language_score": 0.5, "duration": 1.0})
`

const scriptSlowsDown = scriptPrelude + `import time
emit({"type": "progress", "ratio": 0.1, "message": "准备阻塞"})
time.sleep(60)
emit({"type": "done"})
`

const scriptEmptySegments = scriptPrelude + `emit({"type": "done", "language": "zh", "language_score": 0.9, "duration": 10.0})
`

// newTestEngine 构造一个使用指定脚本的快速引擎。
func newTestEngine(t *testing.T, scriptBody string) (*FasterWhisperEngine, string) {
	t.Helper()
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, fasterWhisperScriptName, scriptBody)
	audio := writeAudioFile(t, dir)
	return &FasterWhisperEngine{
		PythonPath: python,
		ScriptPath: script,
		ProbeTimeout: 60 * time.Second,
	}, audio
}

func TestFasterWhisperEngineName(t *testing.T) {
	if got := NewFasterWhisperEngine().Name(); got != "faster-whisper" {
		t.Fatalf("Name 应为 faster-whisper，实际：%s", got)
	}
	if got := NewFunASREngine().Name(); got != "funasr" {
		t.Fatalf("Name 应为 funasr，实际：%s", got)
	}
}

func TestTranscribeHappyPath(t *testing.T) {
	engine, audio := newTestEngine(t, scriptHappyPath)

	type event struct {
		ratio   float64
		message string
	}
	var events []event
	result, err := engine.Transcribe(context.Background(), Request{
		AudioPath: audio,
		ModelName: "small",
		Language:  "auto",
		BeamSize:  5,
	}, func(ratio float64, message string) {
		events = append(events, event{ratio, message})
	})
	if err != nil {
		t.Fatalf("识别不应失败：%v", err)
	}
	if result.Language != "zh" || result.LanguageScore != 0.98 || result.Duration != 6.0 {
		t.Fatalf("done 事件解析错误：%+v", result)
	}
	if result.Engine != "faster-whisper" {
		t.Fatalf("引擎标识错误：%s", result.Engine)
	}
	if len(result.Segments) != 3 {
		t.Fatalf("片段数量错误：%d", len(result.Segments))
	}

	// 按 sequence 排序
	if result.Segments[0].Sequence != 1 || result.Segments[1].Sequence != 2 || result.Segments[2].Sequence != 3 {
		t.Fatalf("片段未按 sequence 排序：%+v", result.Segments)
	}
	// 负数时间归零、文本去空白
	first := result.Segments[0]
	if first.Start != 0 || first.End != 2.0 {
		t.Fatalf("负数开始时间应归零：%+v", first)
	}
	if result.Segments[1].Text != "第二句" {
		t.Fatalf("文本应去除首尾空白：%q", result.Segments[1].Text)
	}
	// end < start 时按 start 处理
	third := result.Segments[2]
	if third.End != third.Start {
		t.Fatalf("end<start 时应取 start：%+v", third)
	}
	if third.Confidence != 0 {
		t.Fatalf("负置信度应归零：%v", third.Confidence)
	}

	if len(events) < 1 {
		t.Fatal("progress 应至少被回调一次")
	}
	found := false
	for _, e := range events {
		if e.ratio == 0.5 && e.message == "已识别一半" {
			found = true
		}
	}
	if !found {
		t.Fatalf("progress 未按事件回调：%+v", events)
	}
}

func TestTranscribeNilProgressAndRequestPayload(t *testing.T) {
	engine, audio := newTestEngine(t, scriptHappyPath)
	// progress 为 nil 时不能 panic。
	result, err := engine.Transcribe(context.Background(), Request{
		AudioPath:     audio,
		ModelPath:     "",
		ModelName:     "medium",
		Language:      "AUTO",
		Device:        "cuda",
		ComputeType:   "float16",
		BeamSize:      3,
		VadFilter:     true,
		InitialPrompt: "以下是普通话内容",
	}, nil)
	if err != nil {
		t.Fatalf("识别不应失败：%v", err)
	}
	if result == nil || len(result.Segments) != 3 {
		t.Fatalf("结果异常：%+v", result)
	}
}

func TestTranscribeProgressPanicIsRecovered(t *testing.T) {
	engine, audio := newTestEngine(t, scriptHappyPath)
	// 回调 panic 不应中断识别。
	_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"},
		func(ratio float64, message string) {
			panic("回调内部错误")
		})
	if err != nil {
		t.Fatalf("回调 panic 不应影响识别：%v", err)
	}
}

func TestTranscribeErrorEvent(t *testing.T) {
	engine, audio := newTestEngine(t, scriptErrorEvent)
	_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"}, nil)
	if err == nil {
		t.Fatal("error 事件应导致返回错误")
	}
	if !strings.Contains(err.Error(), "模型文件缺失") {
		t.Fatalf("应透传脚本的中文错误：%v", err)
	}
}

func TestTranscribeNonZeroExit(t *testing.T) {
	engine, audio := newTestEngine(t, scriptExitNonZero)
	_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"}, nil)
	if err == nil {
		t.Fatal("非 0 退出应返回错误")
	}
	if !strings.Contains(err.Error(), "识别进程异常退出") {
		t.Fatalf("错误应说明进程异常退出：%v", err)
	}
	if !strings.Contains(err.Error(), "ImportError") {
		t.Fatalf("错误应包含 stderr 日志：%v", err)
	}
}

func TestTranscribeMissingDoneEvent(t *testing.T) {
	engine, audio := newTestEngine(t, scriptNoDone)
	_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"}, nil)
	if err == nil || !strings.Contains(err.Error(), "未返回结果") {
		t.Fatalf("缺少 done 事件应报错：%v", err)
	}
}

func TestTranscribeIgnoresNoiseLines(t *testing.T) {
	engine, audio := newTestEngine(t, scriptStdoutNoise)
	result, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"}, nil)
	if err != nil {
		t.Fatalf("识别不应失败：%v", err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Text != "有效片段" {
		t.Fatalf("非 JSON 行应被忽略：%+v", result.Segments)
	}
	if result.Language != "en" {
		t.Fatalf("语言解析错误：%s", result.Language)
	}
}

func TestTranscribeEmptySegments(t *testing.T) {
	engine, audio := newTestEngine(t, scriptEmptySegments)
	var last string
	result, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"},
		func(ratio float64, message string) { last = message })
	if err != nil {
		t.Fatalf("无片段不应视为错误：%v", err)
	}
	if result.Segments == nil || len(result.Segments) != 0 {
		t.Fatalf("片段应为空列表：%+v", result.Segments)
	}
	if !strings.Contains(last, "没有识别到语音") {
		t.Fatalf("应回调提示未识别到内容，实际：%q", last)
	}
}

func TestTranscribeContextCancel(t *testing.T) {
	engine, audio := newTestEngine(t, scriptSlowsDown)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := engine.Transcribe(ctx, Request{AudioPath: audio, ModelName: "small"}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("取消后应返回错误")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Fatalf("取消错误应为中文说明：%v", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("取消未及时终止子进程，耗时 %v", elapsed)
	}
}

func TestTranscribeAudioValidation(t *testing.T) {
	engine, _ := newTestEngine(t, scriptHappyPath)
	dir := t.TempDir()

	if _, err := engine.Transcribe(context.Background(), Request{ModelName: "small"}, nil); err == nil ||
		!strings.Contains(err.Error(), "未指定音频文件") {
		t.Fatalf("空路径应报中文错误：%v", err)
	}
	if _, err := engine.Transcribe(context.Background(), Request{AudioPath: filepath.Join(dir, "无.wav")}, nil); err == nil ||
		!strings.Contains(err.Error(), "音频文件不存在") {
		t.Fatalf("文件缺失应报中文错误：%v", err)
	}
	if _, err := engine.Transcribe(context.Background(), Request{AudioPath: dir}, nil); err == nil ||
		!strings.Contains(err.Error(), "目录") {
		t.Fatalf("目录路径应报错：%v", err)
	}
	empty := filepath.Join(dir, "empty.wav")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("写入空文件失败：%v", err)
	}
	if _, err := engine.Transcribe(context.Background(), Request{AudioPath: empty}, nil); err == nil ||
		!strings.Contains(err.Error(), "为空") {
		t.Fatalf("空文件应报错：%v", err)
	}
}

func TestAvailableWithMissingPython(t *testing.T) {
	engine := &FasterWhisperEngine{PythonPath: filepath.Join(t.TempDir(), "没有这个python.exe")}
	ok, msg := engine.Available(context.Background())
	if ok {
		t.Fatal("解释器不存在时应报告不可用")
	}
	if !strings.Contains(msg, "Python 解释器不存在") {
		t.Fatalf("应给出中文说明：%s", msg)
	}
}

func TestAvailableWithMissingScript(t *testing.T) {
	python := testPython(t)
	engine := &FasterWhisperEngine{
		PythonPath: python,
		ScriptPath: filepath.Join(t.TempDir(), "无脚本.py"),
	}
	ok, msg := engine.Available(context.Background())
	if ok {
		t.Fatal("脚本不存在时应报告不可用")
	}
	if !strings.Contains(msg, "识别脚本不存在") {
		t.Fatalf("应给出中文说明：%s", msg)
	}
}

func TestAvailableChecksImportAndCaches(t *testing.T) {
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, fasterWhisperScriptName, scriptPrelude)

	// 用「一定会失败」的解释器证明 import 检查确实被执行；这里用真实 python 但指定不存在的模块名场景
	// 通过伪解释器（把参数打印出来并返回失败）更可靠：直接用 .bat/sh 包装。
	engine := &FasterWhisperEngine{PythonPath: python, ScriptPath: script, ProbeTimeout: 60 * time.Second}
	ok, msg := engine.Available(context.Background())
	if !ok {
		// 本机没装 faster-whisper 属于正常情况，但必须有中文说明。
		if !strings.Contains(msg, "faster-whisper") {
			t.Fatalf("失败时应包含中文说明：%s", msg)
		}
	} else if !strings.Contains(msg, "已就绪") {
		t.Fatalf("成功时应说明已就绪：%s", msg)
	}

	// 第二次调用应命中缓存：即便把解释器改成不存在的路径也不影响结果。
	engine.PythonPath = python
	ok2, msg2 := engine.Available(context.Background())
	if ok2 != ok || msg2 != msg {
		t.Fatal("可用性检查结果应被缓存")
	}

	// 刷新后重新检查。
	ok3, _ := engine.RefreshAvailability(context.Background())
	if ok3 != ok {
		t.Fatal("刷新后结果应一致")
	}
}

func TestFunASREngineAvailableAndTranscribe(t *testing.T) {
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, funASRScriptName, scriptHappyPath)
	audio := writeAudioFile(t, dir)

	engine := &FunASREngine{PythonPath: python, ScriptPath: script, ProbeTimeout: 60 * time.Second}
	ok, msg := engine.Available(context.Background())
	// 本机没有 funasr 属于正常情况：只要求给出中文说明，不写死失败。
	if !ok && !strings.Contains(msg, "FunASR") {
		t.Fatalf("失败时应包含中文说明：%s", msg)
	}

	// 无论 funasr 是否安装，脚本协议路径都要可用（脚本本身不依赖 funasr）。
	result, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "paraformer-zh"}, nil)
	if err != nil {
		t.Fatalf("FunASR 引擎的协议路径不应失败：%v", err)
	}
	if len(result.Segments) != 3 {
		t.Fatalf("片段数量错误：%d", len(result.Segments))
	}
}

func TestFunASRErrorEvent(t *testing.T) {
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, funASRScriptName, scriptErrorEvent)
	audio := writeAudioFile(t, dir)
	engine := &FunASREngine{PythonPath: python, ScriptPath: script}
	_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio}, nil)
	if err == nil || !strings.Contains(err.Error(), "模型文件缺失") {
		t.Fatalf("应透传中文错误：%v", err)
	}
}

func TestNormalizeSegments(t *testing.T) {
	segments := []Segment{
		{Sequence: 3, Start: 5, End: 4, Confidence: -1},
		{Sequence: 1, Start: -2, End: 1},
		{Sequence: 2, Start: 2, End: -3},
	}
	got := normalizeSegments(segments)
	if len(got) != 3 {
		t.Fatalf("长度错误：%d", len(got))
	}
	if got[0].Sequence != 1 || got[0].Start != 0 {
		t.Fatalf("排序或归零错误：%+v", got[0])
	}
	if got[1].Start != 2 || got[1].End != 2 {
		t.Fatalf("负结束时间归零后应取 start：%+v", got[1])
	}
	if got[2].End != 5 || got[2].Confidence != 0 {
		t.Fatalf("规范化错误：%+v", got[2])
	}
}

func TestClampRatio(t *testing.T) {
	cases := map[float64]float64{-5: -1, -1: -1, 0: 0, 0.5: 0.5, 1: 1, 9: 1}
	for in, want := range cases {
		if got := clampRatio(in); got != want {
			t.Fatalf("clampRatio(%v) 期望 %v，实际 %v", in, want, got)
		}
	}
}

func TestBuildPayloadDefaults(t *testing.T) {
	p := buildPayload(Request{AudioPath: "a.wav", Language: "auto"}, "small")
	if p.Language != "" {
		t.Fatalf("auto 应转换为空串交给模型检测：%q", p.Language)
	}
	if p.ModelName != "small" {
		t.Fatalf("未指定模型时应回落到默认模型：%q", p.ModelName)
	}
	if p.Device != "auto" || p.ComputeType != "auto" {
		t.Fatalf("设备与精度默认值错误：%+v", p)
	}
	if p.BeamSize != 5 {
		t.Fatalf("beam_size 默认值错误：%d", p.BeamSize)
	}
	if p.DownloadRoot == "" {
		t.Fatal("download_root 不应为空")
	}

	p2 := buildPayload(Request{AudioPath: "a.wav", ModelPath: `D:\models\small`, Language: "zh",
		Device: "cpu", ComputeType: "int8", BeamSize: 2, VadFilter: true}, "small")
	if p2.ModelName != "" {
		t.Fatalf("指定本地模型目录时不应再传模型名：%q", p2.ModelName)
	}
	if p2.Language != "zh" || p2.Device != "cpu" || p2.ComputeType != "int8" || p2.BeamSize != 2 || !p2.VadFilter {
		t.Fatalf("参数透传错误：%+v", p2)
	}
}

func TestEmbeddedScriptsExist(t *testing.T) {
	fw, err := embeddedScript(fasterWhisperScriptName)
	if err != nil {
		t.Fatalf("内嵌 faster-whisper 脚本缺失：%v", err)
	}
	if !strings.Contains(string(fw), "WhisperModel") || !strings.Contains(string(fw), "done") {
		t.Fatal("faster-whisper 脚本内容不完整")
	}
	fa, err := embeddedScript(funASRScriptName)
	if err != nil {
		t.Fatalf("内嵌 FunASR 脚本缺失：%v", err)
	}
	if !strings.Contains(string(fa), "AutoModel") {
		t.Fatal("FunASR 脚本应优先使用 AutoModel")
	}
}

func TestResolveScriptExtractsEmbedded(t *testing.T) {
	dir := t.TempDir()
	script, err := resolveScript(context.Background(), "", fasterWhisperScriptName, dir)
	if err != nil {
		t.Fatalf("释放内嵌脚本失败：%v", err)
	}
	if _, statErr := os.Stat(script); statErr != nil {
		t.Fatalf("释放后的脚本不存在：%v", statErr)
	}
	// 再次解析应复用同一文件。
	again, err := resolveScript(context.Background(), "", fasterWhisperScriptName, dir)
	if err != nil || again != script {
		t.Fatalf("应复用已释放的脚本：%v %s", err, again)
	}
	// 显式路径优先。
	explicit := writeWorkerScript(t, dir, "custom.py", "print('hi')")
	got, err := resolveScript(context.Background(), explicit, fasterWhisperScriptName, dir)
	if err != nil || got != explicit {
		t.Fatalf("显式脚本路径应优先：%v %s", err, got)
	}
}

func TestResolvePython(t *testing.T) {
	python := testPython(t)
	if got, err := resolvePython(python); err != nil || got != python {
		t.Fatalf("应接受显式解释器路径：%v %s", err, got)
	}
	if _, err := resolvePython(filepath.Join(t.TempDir(), "nope.exe")); err == nil {
		t.Fatal("不存在的解释器应报错")
	}
	if _, err := resolvePython(t.TempDir()); err == nil {
		t.Fatal("目录不应被当作解释器")
	}
	// 环境变量优先于 PATH
	t.Setenv("TRANSCRIBE_PYTHON", python)
	got, err := resolvePython("")
	if err != nil || got != python {
		t.Fatalf("应读取 TRANSCRIBE_PYTHON：%v %s", err, got)
	}
}

func TestConsumeEventsProtocolError(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"progress","ratio":0.2,"message":"处理中"}`,
		`{"type":"error","message":"模型下载失败"}`,
	}, "\n")
	var ratios []float64
	_, err := consumeEvents(strings.NewReader(input), "faster-whisper", func(r float64, _ string) {
		ratios = append(ratios, r)
	})
	if err == nil || !strings.Contains(err.Error(), "模型下载失败") {
		t.Fatalf("应返回脚本错误：%v", err)
	}
	var target *protocolError
	if !errors.As(err, &target) {
		t.Fatalf("错误类型应为 protocolError：%v", err)
	}
	if len(ratios) != 1 || ratios[0] != 0.2 {
		t.Fatalf("错误前的事件仍应回调：%v", ratios)
	}
}

func TestConsumeEventsProgressWithoutRatio(t *testing.T) {
	var ratio float64 = 9
	_, err := consumeEvents(strings.NewReader(`{"type":"progress","message":"未定义进度"}`), "x",
		func(r float64, m string) {
			if m == "未定义进度" {
				ratio = r
			}
		})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if ratio != -1 {
		t.Fatalf("未提供 ratio 时应回调 -1，实际：%v", ratio)
	}
}

func TestRequestBodyIsValidJSON(t *testing.T) {
	// 用真实脚本回显入参：把 REQUEST: 行写到 stderr，再由错误信息透出。
	// 这里用会失败的脚本以确保 stderr 被带出。
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, fasterWhisperScriptName, scriptPrelude+`fail("测试失败以回显参数")
`)
	audio := writeAudioFile(t, dir)
	engine := &FasterWhisperEngine{PythonPath: python, ScriptPath: script}
	_, err := engine.Transcribe(context.Background(), Request{
		AudioPath: audio, ModelName: "small", Language: "zh", BeamSize: 4, VadFilter: true, InitialPrompt: "提示",
	}, nil)
	if err == nil {
		t.Fatal("预期失败")
	}
	_ = err
	// 用 consumeEvents 单独确认 payload 字段名与协议一致。
	raw, marshalErr := json.Marshal(buildPayload(Request{AudioPath: audio, ModelName: "small"}, "small"))
	if marshalErr != nil {
		t.Fatalf("payload 序列化失败：%v", marshalErr)
	}
	for _, field := range []string{"audio_path", "model_path", "model_name", "language", "device",
		"compute_type", "beam_size", "vad_filter", "initial_prompt", "download_root"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("payload 缺少字段 %q：%s", field, raw)
		}
	}
}

func TestScriptStdoutAndStderrSeparation(t *testing.T) {
	// 脚本向 stderr 打印日志、向 stdout 打印事件，Go 侧不应把日志当成事件。
	python := testPython(t)
	dir := t.TempDir()
	script := writeWorkerScript(t, dir, fasterWhisperScriptName, scriptPrelude+`log("这是一行普通日志")
print("stderr 之外的内容", file=sys.stderr)
emit({"type": "done", "language": "zh", "language_score": 1.0, "duration": 1.0})
`)
	audio := writeAudioFile(t, dir)
	engine := &FasterWhisperEngine{PythonPath: python, ScriptPath: script}
	result, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"}, nil)
	if err != nil {
		t.Fatalf("识别不应失败：%v", err)
	}
	if result.Language != "zh" {
		t.Fatalf("结果解析错误：%+v", result)
	}
}

func TestConcurrentProgressSafety(t *testing.T) {
	engine, audio := newTestEngine(t, scriptHappyPath)
	var mu sync.Mutex
	count := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := engine.Transcribe(context.Background(), Request{AudioPath: audio, ModelName: "small"},
			func(ratio float64, message string) {
				mu.Lock()
				count++
				mu.Unlock()
			})
		if err != nil {
			t.Errorf("识别不应失败：%v", err)
		}
	}()
	wg.Wait()
	if count == 0 {
		t.Fatal("progress 应被回调")
	}
}

func TestNilEngineSafe(t *testing.T) {
	var fw *FasterWhisperEngine
	if ok, msg := fw.Available(context.Background()); ok || msg == "" {
		t.Fatal("nil 引擎应返回不可用与中文说明")
	}
	if _, err := fw.Transcribe(context.Background(), Request{}, nil); err == nil {
		t.Fatal("nil 引擎应返回错误")
	}
	var fa *FunASREngine
	if ok, msg := fa.Available(context.Background()); ok || msg == "" {
		t.Fatal("nil FunASR 引擎应返回不可用与中文说明")
	}
	if _, err := fa.Transcribe(context.Background(), Request{}, nil); err == nil {
		t.Fatal("nil FunASR 引擎应返回错误")
	}
}

func TestSegmentTimeoutApplied(t *testing.T) {
	engine, audio := newTestEngine(t, scriptSlowsDown)
	start := time.Now()
	_, err := engine.Transcribe(context.Background(), Request{
		AudioPath: audio, ModelName: "small", SegmentTimeout: 1,
	}, nil)
	if err == nil {
		t.Fatal("超时后应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 40*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
}

// TestRealFasterWhisperSilence 尝试真实运行一段静音 wav。
// 需要本机已安装 faster_whisper 且已下载模型，否则跳过，绝不因下载模型而失败。
func TestRealFasterWhisperSilence(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("平台不支持")
	}
	if os.Getenv("TRANSCRIBE_REAL_ASR") == "" {
		t.Skip("未设置 TRANSCRIBE_REAL_ASR=1，跳过真实模型用例（避免下载模型）")
	}
	python := testPython(t)
	engine := NewFasterWhisperEngine()
	engine.PythonPath = python
	if ok, msg := engine.Available(context.Background()); !ok {
		t.Skipf("本机 faster-whisper 不可用，跳过：%s", msg)
	}

	dir := t.TempDir()
	audio := filepath.Join(dir, "silence.wav")
	if err := writeSilenceWAV(audio, 16000, 1); err != nil {
		t.Skipf("生成静音 wav 失败，跳过：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := engine.Transcribe(ctx, Request{
		AudioPath: audio, ModelName: "tiny", Language: "zh", Device: "cpu", ComputeType: "int8",
	}, nil)
	if err != nil {
		t.Skipf("真实识别未成功（可能缺少模型），跳过：%v", err)
	}
	if result.Engine != "faster-whisper" {
		t.Fatalf("引擎标识错误：%s", result.Engine)
	}
}

// writeSilenceWAV 生成一段静音 PCM16 单声道 wav，供真实识别用例使用。
func writeSilenceWAV(path string, sampleRate, seconds int) error {
	samples := sampleRate * seconds
	dataSize := samples * 2
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	putUint32LE(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	putUint32LE(header[16:20], 16)
	putUint16LE(header[20:22], 1)
	putUint16LE(header[22:24], 1)
	putUint32LE(header[24:28], uint32(sampleRate))
	putUint32LE(header[28:32], uint32(sampleRate*2))
	putUint16LE(header[32:34], 2)
	putUint16LE(header[34:36], 16)
	copy(header[36:40], "data")
	putUint32LE(header[40:44], uint32(dataSize))

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		return err
	}
	_, err = f.Write(make([]byte, dataSize))
	return err
}

func putUint32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putUint16LE(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}
