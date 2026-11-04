package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateTaskRequiresSourceFile(t *testing.T) {
	app := NewApp()
	if _, err := app.CreateTask(CreateTaskInput{}); err == nil {
		t.Fatal("expected an empty source file to be rejected")
	}
}

func TestCreateAndStartTask(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: `D:\Media\episode.mp4`})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if task.Status != "created" || task.Name != "episode" {
		t.Fatalf("unexpected task: %+v", task)
	}

	started, err := app.StartTask(task.ID)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if started.Status != "transcribing" || started.Progress != 8 {
		t.Fatalf("unexpected started task: %+v", started)
	}
}

func TestExportSubtitles(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: `D:\Media\episode.mp4`})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	content, err := app.ExportSubtitles(task.ID, "srt")
	if err != nil {
		t.Fatalf("export subtitles: %v", err)
	}
	expected := "1\n00:00:12,350 --> 00:00:17,820\n欢迎来到本教程。"
	if !strings.HasPrefix(content, expected) {
		t.Fatalf("unexpected SRT content: %q", content)
	}
}

func TestTasksPersistAcrossAppInstances(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "tasks.db")
	first := NewApp()
	defer first.shutdown(context.Background())
	if err := first.openDatabase(databasePath); err != nil {
		t.Fatalf("open first database: %v", err)
	}
	task, err := first.CreateTask(CreateTaskInput{SourceFile: `D:\Media\persisted.mp4`})
	if err != nil {
		t.Fatalf("create persisted task: %v", err)
	}
	if _, err := first.StartTask(task.ID); err != nil {
		t.Fatalf("start persisted task: %v", err)
	}

	second := NewApp()
	defer second.shutdown(context.Background())
	if err := second.openDatabase(databasePath); err != nil {
		t.Fatalf("open second database: %v", err)
	}
	dashboard := second.GetDashboard()
	if dashboard.ActiveTask == nil || dashboard.ActiveTask.ID != task.ID || dashboard.ActiveTask.Status != "transcribing" {
		t.Fatalf("task was not restored: %+v", dashboard)
	}
}
