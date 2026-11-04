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
	segments, err := second.ListSubtitles(task.ID)
	if err != nil {
		t.Fatalf("list restored subtitles: %v", err)
	}
	if len(segments) != 3 || segments[0].Translated != "欢迎来到本教程。" {
		t.Fatalf("subtitles were not restored: %+v", segments)
	}
}

func TestUpdateSubtitles(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: `D:\Media\episode.mp4`})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	segments := []SubtitleSegment{{
		ID: "edited-001", Sequence: 1, Start: 1.25, End: 3.5,
		SourceText: "Edited source", Translated: "编辑后的译文", Status: "translated",
	}}
	if err := app.UpdateSubtitles(task.ID, segments); err != nil {
		t.Fatalf("update subtitles: %v", err)
	}
	got, err := app.ListSubtitles(task.ID)
	if err != nil {
		t.Fatalf("list updated subtitles: %v", err)
	}
	if len(got) != 1 || got[0].ID != "edited-001" {
		t.Fatalf("unexpected updated subtitles: %+v", got)
	}
}
