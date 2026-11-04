// Package service 实现任务编排：媒体提取、语音识别、字幕翻译、导出。
//
// 本文件定义编排层依赖的下游接口（依赖倒置），以及依赖装配构造器，
// 由 Lead 维护。真正的队列与阶段推进实现在 queue.go / runner.go。
package service

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/translation"
)

// MediaExtractor 负责音频提取与媒体探测。
type MediaExtractor interface {
	Probe(ctx context.Context, inputPath string) (media.Info, error)
	Extract(ctx context.Context, req media.ExtractRequest) error
}

// ModelResolver 负责确保某个模型已经下载到本地，并返回本地目录。
//
// 除了下载与目录解析，它还向编排层提供「这个模型该用哪个识别引擎」的
// 信息：同一个任务里模型格式可能是 CTranslate2，也可能是 transformers
// safetensors，不能一律交给 faster-whisper。
type ModelResolver interface {
	Ensure(ctx context.Context, modelID string, progress modelmanager.Progress) (string, error)
}

// ModelEngineResolver 是可选能力：能根据模型 ID 报告其适用的识别引擎。
//
// 未实现时编排层回退到设置里的引擎名，保证旧行为不变。
type ModelEngineResolver interface {
	// EngineFor 返回该模型适用的引擎标识（faster-whisper / transformers）。
	// 未知模型返回空串。
	EngineFor(modelID string) string
}

// EngineSelector 根据模型格式挑选识别引擎。
type EngineSelector interface {
	// Select 返回处理该模型所需的引擎。
	//   modelID 可为空（表示用默认模型）；
	//   prefer 是设置里配置的引擎名，仅在没有更具体的模型信息时生效。
	Select(modelID, prefer string) asr.Engine
}

// StaticEngineSelector 是 EngineSelector 的默认实现：按模型清单里的 Engine
// 字段路由，找不到清单项时用 prefer 对应的引擎。
type StaticEngineSelector struct {
	// Fallback 是模型不在清单里时使用的引擎（通常是 faster-whisper）。
	Fallback asr.Engine
	// Engines 是引擎标识 -> 引擎实例。
	Engines map[string]asr.Engine
	// Lookup 返回模型适配的引擎标识；为 nil 时只用 prefer。
	Lookup func(modelID string) string
}

// Select 实现 EngineSelector。
//
// 优先级：模型的 Engine 字段 > prefer > Fallback。这样用户选了 cohere 时
// 一定走 transformers，不会因为设置里写着 faster-whisper 而加载失败。
func (s StaticEngineSelector) Select(modelID, prefer string) asr.Engine {
	pick := func(name string) asr.Engine {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return nil
		}
		if engine, ok := s.Engines[name]; ok && engine != nil {
			return engine
		}
		return nil
	}
	if s.Lookup != nil {
		if engine := pick(s.Lookup(modelID)); engine != nil {
			return engine
		}
	}
	if engine := pick(prefer); engine != nil {
		return engine
	}
	return s.Fallback
}

// TranslatorFactory 根据任务设置构造翻译流水线。
type TranslatorFactory interface {
	New(ctx context.Context, job TranslationJob) (Pipeline, error)
}

// Pipeline 是一次翻译任务的执行单元。
type Pipeline interface {
	// Translate 翻译字幕，progress(done, total) 用于上报进度。
	Translate(ctx context.Context, items []translation.Item, progress func(done, total int)) ([]translation.Choice, error)
}

// TranslationJob 描述一次翻译所需上下文。
type TranslationJob struct {
	TaskID         string
	Provider       string
	Endpoint       string
	APIKey         string
	Model          string
	SourceLanguage string
	TargetLanguage string
}

// Deps 是 Queue 的下游依赖集合，测试时可逐个替换为假实现。
type Deps struct {
	Store *repository.Store
	Media MediaExtractor
	// Burner 负责把字幕烧进视频；为 nil 时压制功能不可用。
	Burner     media.Burner
	Engine     asr.Engine
	// Engines 允许按模型格式挑选引擎；为 nil 时一律用 Engine。
	Engines    EngineSelector
	Resolver   ModelResolver
	Translator TranslatorFactory
	Emitters   []Emitter
	WorkDir    string
	Settings   func(ctx context.Context) (repository.SettingsRecord, error)
	Clock      func() time.Time
	// Concurrency 是同时进行的任务数量，默认 1。
	Concurrency int
}

// Emitter 接收任务状态变更通知，用于推送 Wails 事件。
type Emitter func(ctx context.Context, rec *repository.TaskRecord)

// SettingsDefaults 返回设置的默认值，供首次启动与测试使用。
func SettingsDefaults(userConfigDir string) repository.SettingsRecord {
	return repository.SettingsRecord{
		ModelDir:            SettingsModelDir(userConfigDir),
		Mirror:              "hf-mirror",
		AsrEngine:           "faster-whisper",
		AsrDevice:           "auto",
		ComputeType:         "auto",
		DefaultModel:        "small",
		TranslationProvider: "ollama",
		TranslationEndpoint: "http://127.0.0.1:11434",
		TranslationModel:    "qwen2.5:7b-instruct",
	}
}

// SettingsModelDir 返回默认模型缓存目录。
func SettingsModelDir(userConfigDir string) string {
	if userConfigDir == "" {
		return ""
	}
	return filepath.Join(userConfigDir, "transcribe-whisper", "models")
}
