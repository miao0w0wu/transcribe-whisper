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
}

// BurnSubtitlesInput 是字幕压制的入参。
type BurnSubtitlesInput struct {
	// OutputPath 为空时由后端在工作目录下自动生成文件名。
	OutputPath string `json:"outputPath"`
	FontName   string `json:"fontName"`
	FontSize   int    `json:"fontSize"`
	MarginV    int    `json:"marginV"`
}

// EnvironmentStatus 汇总运行环境探测结果。
type EnvironmentStatus struct {
	FFmpegPath     string `json:"ffmpegPath"`
	FFmpegOK       bool   `json:"ffmpegOk"`
	FFmpegDetail   string `json:"ffmpegDetail"`
	FfmpegSubtitle bool   `json:"ffmpegSubtitle"`
	PythonPath     string `json:"pythonPath"`
	PythonOK       bool   `json:"pythonOk"`
	PythonDetail   string `json:"pythonDetail"`
	FasterWhisper  bool   `json:"fasterWhisper"`
	GPU            string `json:"gpu"`
	ModelDir       string `json:"modelDir"`
}
