// Package translation 定义翻译 Provider 契约与批处理流水线。
//
// 该文件是共享契约，由 Lead 维护；实现位于同包其他文件。
package translation

import "context"

// Item 是一条待翻译字幕。
type Item struct {
	ID   string
	Text string
}

// Choice 是 Provider 返回的一条译文。
type Choice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Request 发给 Provider 的批次请求。
type Request struct {
	Items          []Item
	SourceLanguage string
	TargetLanguage string
	Style          string // 例如 formal / casual / subtitle
	ContentMode    string // 例如 subtitle / dialogue / lecture
	Glossary       map[string]string
}

// Provider 是统一的翻译后端接口。
type Provider interface {
	// Name 返回 Provider 标识。
	Name() string
	// Translate 翻译一批字幕，必须返回与请求 id 一一对应的条目。
	Translate(ctx context.Context, req Request) ([]Choice, error)
}

// ProviderDescription 是给界面展示的 Provider 元信息。
type ProviderDescription struct {
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Endpoint  string           `json:"endpoint"`
	Model     string           `json:"model"`
	Local     bool             `json:"local"`
	Available bool             `json:"available"`
	Detail    string           `json:"detail"`
	Options   []ProviderOption `json:"options"`
}

// ProviderOption 是可切换的 Provider 变体。
type ProviderOption struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Local       bool   `json:"local"`
	Description string `json:"description"`
}

// ErrorKind 用于区分可重试错误与不可重试错误。
type ErrorKind string

const (
	// KindRetryable 表示网络抖动、限流、格式不符等可以重试的错误。
	KindRetryable ErrorKind = "retryable"
	// KindFatal 表示配置错误等不可重试的错误。
	KindFatal ErrorKind = "fatal"
)

// Error 是 Provider 返回的结构化错误。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }
