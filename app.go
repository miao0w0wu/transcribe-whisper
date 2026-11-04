package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type App struct {
	ctx   context.Context
	mu    sync.RWMutex
	tasks map[string]*Task
	db    *sql.DB
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
	ActiveTask *Task   `json:"activeTask"`
	Tasks      []*Task `json:"tasks"`
}

type SubtitleSegment struct {
	ID         string  `json:"id"`
	Sequence   int     `json:"sequence"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	SourceText string  `json:"sourceText"`
	Translated string  `json:"translatedText"`
	Status     string  `json:"translationStatus"`
}

func NewApp() *App {
	app := &App{tasks: make(map[string]*Task)}
	if err := app.openDatabase(":memory:"); err != nil {
		panic(err)
	}
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Printf("cannot locate application config directory: %v", err)
		return
	}
	databasePath := filepath.Join(configDir, "transcribe-whisper", "transcribe-whisper.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		log.Printf("cannot create application data directory: %v", err)
		return
	}
	if err := a.openDatabase(databasePath); err != nil {
		log.Printf("cannot open task database: %v", err)
		return
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			log.Printf("close task database: %v", err)
		}
		a.db = nil
	}
}

func (a *App) openDatabase(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		source_file TEXT NOT NULL,
		source_language TEXT NOT NULL,
		target_language TEXT NOT NULL,
		status TEXT NOT NULL,
		progress INTEGER NOT NULL DEFAULT 0,
		current_stage TEXT NOT NULL,
		subtitle_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS subtitle_segments (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		start_time REAL NOT NULL,
		end_time REAL NOT NULL,
		source_text TEXT NOT NULL,
		translated_text TEXT NOT NULL,
		translation_status TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
	)`); err != nil {
		_ = db.Close()
		return err
	}
	rows, err := db.Query(`SELECT id, name, source_file, source_language, target_language, status, progress, current_stage, subtitle_count, created_at FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		_ = db.Close()
		return err
	}
	defer rows.Close()
	loaded := make(map[string]*Task)
	for rows.Next() {
		task := &Task{}
		if err := rows.Scan(&task.ID, &task.Name, &task.SourceFile, &task.SourceLanguage, &task.TargetLanguage, &task.Status, &task.Progress, &task.CurrentStage, &task.SubtitleCount, &task.CreatedAt); err != nil {
			_ = db.Close()
			return err
		}
		loaded[task.ID] = task
	}
	if err := rows.Err(); err != nil {
		_ = db.Close()
		return err
	}
	a.mu.Lock()
	if a.db != nil && a.db != db {
		_ = a.db.Close()
	}
	a.db = db
	a.tasks = loaded
	a.mu.Unlock()
	return nil
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
	if _, err := a.db.Exec(`INSERT INTO tasks (id, name, source_file, source_language, target_language, status, progress, current_stage, subtitle_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.Name, task.SourceFile, task.SourceLanguage, task.TargetLanguage, task.Status, task.Progress, task.CurrentStage, task.SubtitleCount, task.CreatedAt); err != nil {
		delete(a.tasks, task.ID)
		a.mu.Unlock()
		return nil, fmt.Errorf("save task: %w", err)
	}
	for _, segment := range demoSubtitles() {
		if _, err := a.db.Exec(`INSERT INTO subtitle_segments (id, task_id, sequence, start_time, end_time, source_text, translated_text, translation_status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			segment.ID, task.ID, segment.Sequence, segment.Start, segment.End, segment.SourceText, segment.Translated, segment.Status); err != nil {
			_, _ = a.db.Exec(`DELETE FROM tasks WHERE id = ?`, task.ID)
			delete(a.tasks, task.ID)
			a.mu.Unlock()
			return nil, fmt.Errorf("save subtitles: %w", err)
		}
	}
	task.SubtitleCount = len(demoSubtitles())
	if _, err := a.db.Exec(`UPDATE tasks SET subtitle_count = ? WHERE id = ?`, task.SubtitleCount, task.ID); err != nil {
		delete(a.tasks, task.ID)
		a.mu.Unlock()
		return nil, fmt.Errorf("save subtitle count: %w", err)
	}
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
	if _, err := a.db.Exec(`UPDATE tasks SET status = ?, progress = ?, current_stage = ? WHERE id = ?`, task.Status, task.Progress, task.CurrentStage, task.ID); err != nil {
		return nil, fmt.Errorf("save task status: %w", err)
	}
	copy := *task
	return &copy, nil
}

func (a *App) ListSubtitles(taskID string) ([]SubtitleSegment, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	taskID = strings.TrimSpace(taskID)
	if _, ok := a.tasks[taskID]; !ok {
		return nil, errors.New("task not found")
	}
	rows, err := a.db.Query(`SELECT id, sequence, start_time, end_time, source_text, translated_text, translation_status FROM subtitle_segments WHERE task_id = ? ORDER BY sequence`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list subtitles: %w", err)
	}
	defer rows.Close()
	segments := make([]SubtitleSegment, 0)
	for rows.Next() {
		var segment SubtitleSegment
		if err := rows.Scan(&segment.ID, &segment.Sequence, &segment.Start, &segment.End, &segment.SourceText, &segment.Translated, &segment.Status); err != nil {
			return nil, fmt.Errorf("read subtitles: %w", err)
		}
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read subtitles: %w", err)
	}
	return segments, nil
}

func (a *App) UpdateSubtitles(taskID string, segments []SubtitleSegment) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	taskID = strings.TrimSpace(taskID)
	if _, ok := a.tasks[taskID]; !ok {
		return errors.New("task not found")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("start subtitle update: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM subtitle_segments WHERE task_id = ?`, taskID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear subtitles: %w", err)
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment.ID) == "" || segment.Sequence < 1 || segment.End < segment.Start || segment.Start < 0 {
			_ = tx.Rollback()
			return errors.New("subtitle segment is invalid")
		}
		if _, err := tx.Exec(`INSERT INTO subtitle_segments (id, task_id, sequence, start_time, end_time, source_text, translated_text, translation_status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			segment.ID, taskID, segment.Sequence, segment.Start, segment.End, segment.SourceText, segment.Translated, segment.Status); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("save subtitle segment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subtitles: %w", err)
	}
	task := a.tasks[taskID]
	task.SubtitleCount = len(segments)
	if _, err := a.db.Exec(`UPDATE tasks SET subtitle_count = ? WHERE id = ?`, task.SubtitleCount, taskID); err != nil {
		return fmt.Errorf("save subtitle count: %w", err)
	}
	return nil
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
