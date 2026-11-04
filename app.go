package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type App struct {
	ctx   context.Context
	mu    sync.RWMutex
	tasks map[string]*Task
}

type Task struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SourceFile     string `json:"sourceFile"`
	SourceLanguage string `json:"sourceLanguage"`
	TargetLanguage string `json:"targetLanguage"`
	Status         string `json:"status"`
	Progress       int    `json:"progress"`
	CurrentStage   string `json:"currentStage"`
	SubtitleCount  int    `json:"subtitleCount"`
	CreatedAt      string `json:"createdAt"`
}

type CreateTaskInput struct {
	SourceFile     string `json:"sourceFile"`
	SourceLanguage string `json:"sourceLanguage"`
	TargetLanguage string `json:"targetLanguage"`
	OutputFormat   string `json:"outputFormat"`
}

type Dashboard struct {
	ActiveTask *Task  `json:"activeTask"`
	Tasks      []*Task `json:"tasks"`
}

type SubtitleSegment struct {
	ID          string  `json:"id"`
	Sequence    int     `json:"sequence"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	SourceText  string  `json:"sourceText"`
	Translated  string  `json:"translatedText"`
	Status      string  `json:"translationStatus"`
}

func NewApp() *App {
	return &App{tasks: make(map[string]*Task)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) GetDashboard() Dashboard {
	a.mu.RLock()
	defer a.mu.RUnlock()

	tasks := make([]*Task, 0, len(a.tasks))
	var active *Task
	for _, task := range a.tasks {
		copy := *task
		tasks = append(tasks, &copy)
		if active == nil && task.Status != "completed" {
			active = &copy
		}
	}
	return Dashboard{ActiveTask: active, Tasks: tasks}
}

func (a *App) CreateTask(input CreateTaskInput) (*Task, error) {
	sourceFile := strings.TrimSpace(input.SourceFile)
	if sourceFile == "" {
		return nil, errors.New("source file is required")
	}

	name := strings.TrimSpace(filepath.Base(sourceFile))
	if name == "." || name == string(filepath.Separator) {
		return nil, errors.New("source file name is invalid")
	}

	sourceLanguage := strings.TrimSpace(input.SourceLanguage)
	if sourceLanguage == "" {
		sourceLanguage = "auto"
	}
	targetLanguage := strings.TrimSpace(input.TargetLanguage)
	if targetLanguage == "" {
		targetLanguage = "zh-CN"
	}

	task := &Task{
		ID:             uuid.NewString(),
		Name:           strings.TrimSuffix(name, filepath.Ext(name)),
		SourceFile:     sourceFile,
		SourceLanguage: sourceLanguage,
		TargetLanguage: targetLanguage,
		Status:         "created",
		CurrentStage:   "等待识别",
		SubtitleCount:  0,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	a.mu.Lock()
	a.tasks[task.ID] = task
	a.mu.Unlock()
	return task, nil
}

func (a *App) StartTask(taskID string) (*Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	task, ok := a.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return nil, errors.New("task not found")
	}
	task.Status = "transcribing"
	task.CurrentStage = "正在识别"
	task.Progress = 8
	copy := *task
	return &copy, nil
}

func (a *App) ListSubtitles(taskID string) ([]SubtitleSegment, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.tasks[strings.TrimSpace(taskID)]; !ok {
		return nil, errors.New("task not found")
	}
	return demoSubtitles(), nil
}

func (a *App) ExportSubtitles(taskID string, format string) (string, error) {
	segments, err := a.ListSubtitles(taskID)
	if err != nil {
		return "", err
	}
	if strings.ToLower(strings.TrimSpace(format)) != "srt" {
		return "", errors.New("only srt export is supported")
	}

	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n",
			segment.Sequence,
			formatSRTTime(segment.Start),
			formatSRTTime(segment.End),
			segment.Translated,
		))
	}
	return builder.String(), nil
}

func demoSubtitles() []SubtitleSegment {
	return []SubtitleSegment{
		{ID: "segment-001", Sequence: 1, Start: 12.35, End: 17.82, SourceText: "Welcome to this tutorial.", Translated: "欢迎来到本教程。", Status: "translated"},
		{ID: "segment-002", Sequence: 2, Start: 18.04, End: 23.60, SourceText: "Today we are going to talk about running large language models on your own machine.", Translated: "今天我们来聊聊，如何在自己的电脑上运行大型语言模型。", Status: "translated"},
		{ID: "segment-003", Sequence: 3, Start: 23.88, End: 29.15, SourceText: "No cloud, no API keys, and nothing ever leaves your computer.", Translated: "不需要云端，也不需要 API Key，任何数据都不会离开你的电脑。", Status: "translated"},
	}
}

func formatSRTTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	wholeSeconds := int(seconds) % 60
	milliseconds := int(math.Round((seconds - float64(int(seconds))) * 1000))
	if milliseconds == 1000 {
		wholeSeconds++
		milliseconds = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, wholeSeconds, milliseconds)
}
