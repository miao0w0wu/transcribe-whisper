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
