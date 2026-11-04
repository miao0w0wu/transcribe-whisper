package main

import (
	"context"
	"errors"
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
