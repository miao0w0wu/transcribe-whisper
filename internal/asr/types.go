// Package asr 定义语音识别引擎契约。
//
// 该文件是共享契约，由 Lead 维护；实现位于同包其他文件。
package asr

import "context"

// Segment 是一条识别结果。
type Segment struct {
	ID         string  `json:"id"`
	Sequence   int     `json:"sequence"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Text       string  `json:"text"`
	Language   string  `json:"language"`
	Confidence float64 `json:"confidence"`
}

// Result 是一次识别的完整结果。
type Result struct {
	Language      string    `json:"language"`
	LanguageScore float64   `json:"languageScore"`
	Duration      float64   `json:"duration"`
	Segments      []Segment `json:"segments"`
	Model         string    `json:"model"`
	Engine        string    `json:"engine"`
}

// Progress 是识别进度回调。
// 传入 0..1 之间的比例；-1 表示进度未定。
type Progress func(ratio float64, message string)

// Request 是一次识别任务。
type Request struct {
	AudioPath     string
	ModelPath     string // 本地模型目录
	ModelName     string // 模型标识，例如 small
	DownloadRoot  string // 模型下载缓存目录；ModelPath 为空时生效
	Language      string // auto 表示自动检测
	Device        string // auto / cpu / cuda
	ComputeType   string // auto / int8 / float16 / float32
	BeamSize      int
	VadFilter     bool
	InitialPrompt string
	PythonPath    string
	ScriptPath    string
	// SegmentTimeout 是单次识别的超时时间（秒）。
	SegmentTimeout int

	// 以下参数用于提高识别准确性，零值表示「用脚本内默认值」。
	//
	// VadThreshold 是 VAD 判为语音的概率阈值，越高越严格。
	VadThreshold float64
	// VadMinSilenceMs 是最短静音间隔（毫秒），低于该值不切分。
	VadMinSilenceMs int
	// VadPaddingMs 是语音段前后留白（毫秒），避免切掉字头字尾。
	VadPaddingMs int
	// ConditionOnPreviousText 让每段解码参考上一段文本。关掉能显著减少
	// 重复与幻觉，但可能损失一点上下文连贯性。
	ConditionOnPreviousText bool
	// NoSpeechThreshold 是判为「无语音」的阈值，越高越容易判定为静音。
	NoSpeechThreshold float64
	// CompressionRatioThreshold 是压缩比阈值，越高对重复文本越宽容。
	CompressionRatioThreshold float64
}

// Engine 是 ASR 引擎接口。
type Engine interface {
	// Name 返回引擎标识，例如 faster-whisper / funasr。
	Name() string
	// Available 报告引擎依赖是否就绪。
	Available(ctx context.Context) (bool, string)
	// Transcribe 执行识别，progress 可为 nil。
	Transcribe(ctx context.Context, req Request, progress Progress) (*Result, error)
}
