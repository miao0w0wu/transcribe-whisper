// Package service 实现任务编排：媒体提取、语音识别、字幕翻译、导出。
//
// 本文件定义编排层依赖的下游接口（依赖倒置），以及依赖装配构造器，
// 由 Lead 维护。真正的队列与阶段推进实现在 queue.go / runner.go。
package service

import (
	"context"
	"path/filepath"
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
type ModelResolver interface {
	Ensure(ctx context.Context, modelID string, progress modelmanager.Progress) (string, error)
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
	Store      *repository.Store
	Media      MediaExtractor
	Engine     asr.Engine
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
