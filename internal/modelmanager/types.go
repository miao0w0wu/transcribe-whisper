// Package modelmanager 负责 ASR 模型的清单、下载、缓存与删除保护。
//
// 该文件是共享契约，由 Lead 维护；实现位于同包其他文件。
package modelmanager

import "context"

// ManifestFile 是模型仓库中的一个文件。
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// 引擎标识常量。CatalogEntry.Engine 取值之一，决定该模型由哪个识别引擎加载。
const (
	// EngineFasterWhisper 表示 CTranslate2 格式，由 faster-whisper 加载。
	EngineFasterWhisper = "faster-whisper"
	// EngineTransformers 表示 Hugging Face safetensors 格式，由 transformers 加载。
	EngineTransformers = "transformers"
)

// CatalogEntry 是内置模型清单项。
type CatalogEntry struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Repo        string         `json:"repo"`
	Description string         `json:"description"`
	SizeBytes   int64          `json:"sizeBytes"`
	Files       []ManifestFile `json:"files"`
	// Required 表示该文件缺失时模型不可用。
	Required []string `json:"required"`
	// Engine 是该模型适用的识别引擎，取 EngineFasterWhisper / EngineTransformers。
	// 空值按 faster-whisper 处理，保证旧数据兼容。
	Engine string `json:"engine"`
	// Gated 表示该模型在 Hugging Face 上是受限仓库，需要提供访问令牌才能下载。
	// 界面据此提示用户配置 HF Token，而不是让下载以一个费解的 403 失败。
	Gated bool `json:"gated"`
	// Custom 表示该条目由用户自选本地目录加入，不是内置清单项。
	Custom bool `json:"custom"`
}

// ProgressEvent 是下载进度事件。
type ProgressEvent struct {
	ModelID  string `json:"modelId"`
	Progress int    `json:"progress"`
	Message  string `json:"message"`
	Done     bool   `json:"done"`
	Error    string `json:"error"`
}

// Progress 是下载进度回调，ratio 取值 0..1。
type Progress func(ratio float64, message string)

// Mirror 是可选的下载镜像。
type Mirror struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	BaseURL string `json:"baseUrl"`
}

// Manager 是模型管理接口，方便测试替换。
type Manager interface {
	Catalog() []CatalogEntry
	Installed(ctx context.Context) (map[string]string, error)
	// Ensure 确保模型已下载到本地，返回本地目录。
	Ensure(ctx context.Context, modelID string, progress Progress) (string, error)
	// Remove 删除模型目录，保护正在被任务使用的模型。
	Remove(ctx context.Context, modelID string) error
	Cancel(modelID string) bool
}
