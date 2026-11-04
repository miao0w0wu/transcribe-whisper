package main

import "testing"

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
