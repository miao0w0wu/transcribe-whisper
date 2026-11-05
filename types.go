package main

// 本文件只放 Wails Binding 暴露给前端的公共类型。
// 领域实现位于 internal/ 目录，app.go / api_*.go 负责把领域结果映射成这里的结构。

// Task 表示一个字幕任务在界面上的完整快照。
type Task struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	SourceFile      string  `json:"sourceFile"`
	SourceLanguage  string  `json:"sourceLanguage"`
	TargetLanguage  string  `json:"targetLanguage"`
	Status          string  `json:"status"`
	Stage           string  `json:"stage"`
	Progress        int     `json:"progress"`
	CurrentStage    string  `json:"currentStage"`
	SubtitleCount   int     `json:"subtitleCount"`
	TranslatedCount int     `json:"translatedCount"`
	Message         string  `json:"message"`
	Error           string  `json:"error"`
	Engine          string  `json:"engine"`
	Model           string  `json:"model"`
	Provider        string  `json:"provider"`
	OutputFormat    string  `json:"outputFormat"`
	Duration        float64 `json:"duration"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

// CreateTaskInput 是新建任务的入参。
type CreateTaskInput struct {
	SourceFile     string `json:"sourceFile"`
	SourceLanguage string `json:"sourceLanguage"`
	TargetLanguage string `json:"targetLanguage"`
	OutputFormat   string `json:"outputFormat"`
	Engine         string `json:"engine"`
	Model          string `json:"model"`
	Translate      bool   `json:"translate"`
	Provider       string `json:"provider"`
}

// Dashboard 是工作台首屏数据。
type Dashboard struct {
	ActiveTask *Task   `json:"activeTask"`
	Tasks      []*Task `json:"tasks"`
}

// SubtitleSegment 是单条字幕。
type SubtitleSegment struct {
	ID         string  `json:"id"`
	Sequence   int     `json:"sequence"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	SourceText string  `json:"sourceText"`
	Translated string  `json:"translatedText"`
	Status     string  `json:"translationStatus"`
}

// TaskProgressEvent 通过 Wails Events 推送给前端。
type TaskProgressEvent struct {
	TaskID       string `json:"taskId"`
	Status       string `json:"status"`
	Stage        string `json:"stage"`
	Progress     int    `json:"progress"`
	CurrentStage string `json:"currentStage"`
	Message      string `json:"message"`
	Error        string `json:"error"`
}

// ModelInfo 描述一个可下载 / 已安装的 ASR 模型。
type ModelInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Repo        string   `json:"repo"`
	SizeBytes   int64    `json:"sizeBytes"`
	Installed   bool     `json:"installed"`
	Installing  bool     `json:"installing"`
	Progress    int      `json:"progress"`
	LocalPath   string   `json:"localPath"`
	Mirror      string   `json:"mirror"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
	// Engine 是该模型适用的识别引擎：faster-whisper 或 transformers。
	Engine string `json:"engine"`
	// Custom 表示这是用户自选 / 自建条目，不是内置清单项。
	Custom bool `json:"custom"`
	// Gated 表示 Hugging Face 上的受限仓库，下载需要访问令牌。
	Gated bool `json:"gated"`
	// Local 表示这是本地目录模型，不下载也不删除。
	Local bool `json:"local"`
	// InstalledOK 是本地模型目录的可用性（已安装但目录被移动时为 false）。
	InstalledOK bool `json:"installedOk"`
}

// AddLocalModelInput 是「自选本地模型」的入参。
type AddLocalModelInput struct {
	// Directory 是本地模型目录绝对路径。
	Directory string `json:"directory"`
	// ID 是模型标识，留空时取目录名。
	ID string `json:"id"`
}

// AddRepoModelInput 是「按链接添加模型」的入参。
type AddRepoModelInput struct {
	// Repo 是仓库名（org/name）或 Hugging Face 链接。
	Repo string `json:"repo"`
	// ID 是模型标识，留空时取仓库名。
	ID string `json:"id"`
	// Engine 强制指定引擎，留空时按仓库内容自动判断。
	Engine string `json:"engine"`
}

// SettingsSnapshot 是前端可读的设置快照，API Key 只返回是否已配置。
type SettingsSnapshot struct {
	FFmpegPath           string `json:"ffmpegPath"`
	PythonPath           string `json:"pythonPath"`
	ModelDir             string `json:"modelDir"`
	Mirror               string `json:"mirror"`
	HuggingFaceEndpoint  string `json:"huggingFaceEndpoint"`
	AsrEngine            string `json:"asrEngine"`
	AsrDevice            string `json:"asrDevice"`
	ComputeType          string `json:"computeType"`
	DefaultModel         string `json:"defaultModel"`
	TranslationProvider  string `json:"translationProvider"`
	TranslationEndpoint  string `json:"translationEndpoint"`
	TranslationModel     string `json:"translationModel"`
	TranslationAPIKeySet bool   `json:"translationApiKeySet"`
	CloudPrivacyConfirm  bool   `json:"cloudPrivacyConfirm"`
	TranslationStyle     string `json:"translationStyle"`
	GlossaryText         string `json:"glossaryText"`
	// HuggingFaceTokenSet 只返回是否已配置，绝不把令牌明文透给前端。
	HuggingFaceTokenSet bool `json:"huggingFaceTokenSet"`
	// LocalModels 是用户自选的本地识别模型：标识 -> 目录。
	LocalModels map[string]string `json:"localModels"`

	// 音频降噪：off / light / standard / strong / custom。
	NoiseReduction string `json:"noiseReduction"`
	// NoiseReductionFilter 仅在 NoiseReduction == "custom" 时生效。
	NoiseReductionFilter string `json:"noiseReductionFilter"`
	// NoiseReductionSampleRate 为 0 表示沿用 16000。
	NoiseReductionSampleRate int `json:"noiseReductionSampleRate"`

	// 识别准确性参数。
	BeamSize                  int     `json:"beamSize"`
	VadFilter                 bool    `json:"vadFilter"`
	VadThreshold              float64 `json:"vadThreshold"`
	VadMinSilenceMs           int     `json:"vadMinSilenceMs"`
	VadPaddingMs              int     `json:"vadPaddingMs"`
	HallucinationFilter       string  `json:"hallucinationFilter"`
	ConditionOnPreviousText   bool    `json:"conditionOnPreviousText"`
	NoSpeechThreshold         float64 `json:"noSpeechThreshold"`
	CompressionRatioThreshold float64 `json:"compressionRatioThreshold"`
}

// SettingsPatch 是设置的部分更新入参，空字段表示保持不变。
type SettingsPatch struct {
	FFmpegPath          *string `json:"ffmpegPath"`
	PythonPath          *string `json:"pythonPath"`
	ModelDir            *string `json:"modelDir"`
	Mirror              *string `json:"mirror"`
	HuggingFaceEndpoint *string `json:"huggingFaceEndpoint"`
	AsrEngine           *string `json:"asrEngine"`
	AsrDevice           *string `json:"asrDevice"`
	ComputeType         *string `json:"computeType"`
	DefaultModel        *string `json:"defaultModel"`
	TranslationProvider *string `json:"translationProvider"`
	TranslationEndpoint *string `json:"translationEndpoint"`
	TranslationModel    *string `json:"translationModel"`
	TranslationAPIKey   *string `json:"translationApiKey"`
	CloudPrivacyConfirm *bool   `json:"cloudPrivacyConfirm"`
	TranslationStyle    *string `json:"translationStyle"`
	// GlossaryText 是术语表原文，每行一条，格式：源术语=目标译法。
	GlossaryText *string `json:"glossaryText"`
	// HuggingFaceToken 是 HF 访问令牌，用于下载受限（gated）仓库。
	// 传空串表示保持不变；传 "-" 表示清除。
	HuggingFaceToken *string `json:"huggingFaceToken"`

	// 音频降噪。
	NoiseReduction           *string `json:"noiseReduction"`
	NoiseReductionFilter     *string `json:"noiseReductionFilter"`
	NoiseReductionSampleRate *int    `json:"noiseReductionSampleRate"`

	// 识别准确性参数。
	BeamSize                  *int     `json:"beamSize"`
	VadFilter                 *bool    `json:"vadFilter"`
	VadThreshold              *float64 `json:"vadThreshold"`
	VadMinSilenceMs           *int     `json:"vadMinSilenceMs"`
	VadPaddingMs              *int     `json:"vadPaddingMs"`
	HallucinationFilter       *string  `json:"hallucinationFilter"`
	ConditionOnPreviousText   *bool    `json:"conditionOnPreviousText"`
	NoSpeechThreshold         *float64 `json:"noSpeechThreshold"`
	CompressionRatioThreshold *float64 `json:"compressionRatioThreshold"`
}

// BurnSubtitlesInput 是字幕压制的入参。
type BurnSubtitlesInput struct {
	// OutputPath 为空时由后端在工作目录下自动生成文件名。
	OutputPath string `json:"outputPath"`
	FontName   string `json:"fontName"`
	FontSize   int    `json:"fontSize"`
	MarginV    int    `json:"marginV"`
}

// OllamaModelInfo 描述本机 Ollama 中的一个已安装模型。
type OllamaModelInfo struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	ParameterSize string `json:"parameterSize"`
	Quantization  string `json:"quantization"`
	Family        string `json:"family"`
	ModifiedAt    string `json:"modifiedAt"`
}

// FFmpegSource 描述一个 FFmpeg 下载源。
type FFmpegSource struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	URL     string `json:"url"`
	Note    string `json:"note"`
	License string `json:"license"`
}

// FFmpegStatus 是 FFmpeg 的安装与可用性状态。
type FFmpegStatus struct {
	// Configured 表示用户是否指定过路径；false 即「未安装」。
	Configured bool   `json:"configured"`
	Path       string `json:"path"`
	Version    string `json:"version"`
	OK         bool   `json:"ok"`
	// CanBurn 表示支持字幕压制（需要带 libass 的完整版）。
	CanBurn bool   `json:"canBurn"`
	Detail  string `json:"detail"`
	// DefaultDir 是默认安装目录（应用数据目录下的 ffmpeg/）。
	DefaultDir string `json:"defaultDir"`
	// DataDir 是应用数据目录，界面用来展示文件实际存放位置。
	DataDir     string `json:"dataDir"`
	Downloading bool   `json:"downloading"`
	// Sources 是可选的下载源。
	Sources []FFmpegSource `json:"sources"`
}

// DownloadFFmpegInput 是 FFmpeg 下载入参。
type DownloadFFmpegInput struct {
	// Dir 是安装目录，留空用默认目录（应用数据目录下的 ffmpeg/）。
	Dir string `json:"dir"`
	// SourceID 指定下载源，留空按内置顺序自动降级。
	SourceID string `json:"sourceId"`
}

// FFmpegProgressPayload 是 ffmpeg:progress 事件载荷。
type FFmpegProgressPayload struct {
	Progress int    `json:"progress"`
	Message  string `json:"message"`
	Done     bool   `json:"done"`
	Error    string `json:"error"`
	// Path 是安装完成后的 ffmpeg.exe 路径（仅 Done 且无 Error 时有效）。
	Path string `json:"path"`
}

// EnvironmentStatus 汇总运行环境探测结果。
type EnvironmentStatus struct {
	FFmpegPath     string `json:"ffmpegPath"`
	FFmpegOK       bool   `json:"ffmpegOk"`
	FFmpegDetail   string `json:"ffmpegDetail"`
	FfmpegSubtitle bool   `json:"ffmpegSubtitle"`
	// FFmpegDefaultDir 是 FFmpeg 的默认安装目录，未安装时界面用它提示用户。
	FFmpegDefaultDir string `json:"ffmpegDefaultDir"`
	PythonPath     string `json:"pythonPath"`
	PythonOK       bool   `json:"pythonOk"`
	PythonDetail   string `json:"pythonDetail"`
	FasterWhisper  bool   `json:"fasterWhisper"`
	// Transformers 表示 torch + transformers 是否就绪（cohere 等模型需要）。
	Transformers bool `json:"transformers"`
	// TransformersDetail 是 transformers 引擎的可用性说明。
	TransformersDetail string `json:"transformersDetail"`
	GPU                string `json:"gpu"`
	ModelDir           string `json:"modelDir"`
}
