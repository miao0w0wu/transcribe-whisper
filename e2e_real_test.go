package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
)

// TestRealPipeline 是可选的真实链路冒烟测试：需要本机 ffmpeg 与 faster-whisper。
// 设置 TRANSCRIBE_REAL_E2E=1 才会执行，并会下载 tiny 模型（约 78MB）。
func TestRealPipeline(t *testing.T) {
	if os.Getenv("TRANSCRIBE_REAL_E2E") != "1" {
		t.Skip("未设置 TRANSCRIBE_REAL_E2E=1，跳过真实链路测试")
	}
	audio := os.Getenv("TRANSCRIBE_E2E_AUDIO")
	if audio == "" {
		t.Fatal("需要设置 TRANSCRIBE_E2E_AUDIO 指向测试音视频文件")
	}

	app := NewApp()
	home, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("定位配置目录失败: %v", err)
	}
	tmpDir := filepath.Join(home, "transcribe-whisper-e2e", time.Now().Format("150405"))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	if err := app.openDatabase(filepath.Join(tmpDir, "e2e.db")); err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer app.shutdown(context.Background())

	// 指定本机 Python 与 tiny 模型，避免依赖外部环境配置。
	pythonObserved := os.Getenv("TRANSCRIBE_E2E_PYTHON")
	if pythonObserved == "" {
		pythonObserved = "python"
	}
	engine := os.Getenv("TRANSCRIBE_E2E_MODEL")
	if engine == "" {
		engine = "tiny"
	}
	// 可直接复用一个已下载模型的目录，跳过网络下载。
	if existing := os.Getenv("TRANSCRIBE_E2E_MODEL_DIR"); existing != "" {
		if err := os.MkdirAll(existing, 0o755); err != nil {
			t.Fatalf("准备模型目录失败: %v", err)
		}
	}

	app.mu.Lock()
	settings := app.settings
	settings.PythonPath = pythonObserved
	settings.DefaultModel = engine
	settings.TranslationProvider = "none"
	if existing := os.Getenv("TRANSCRIBE_E2E_MODEL_DIR"); existing != "" {
		settings.ModelDir = filepath.Dir(existing)
	} else {
		settings.ModelDir = filepath.Join(tmpDir, "models")
	}
	app.settings = settings
	// 测试注入：直接使用外部准备好的模型目录，跳过多镜像下载。
	if existingDir := os.Getenv("TRANSCRIBE_E2E_MODEL_DIR"); existingDir != "" {
		app.models = modelmanager.New(modelmanager.Options{
			RootDir:   settings.ModelDir,
			Catalog:   modelmanager.DefaultCatalog(),
			UserAgent: "transcribe-whisper/1.0",
		})
	}
	app.mu.Unlock()

	task, err := app.CreateTask(CreateTaskInput{
		SourceFile: audio,
		Engine:     "faster-whisper",
		Model:      engine,
		Translate:  false,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.StartTask(task.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		rec, err := app.store.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("读取任务失败: %v", err)
		}
		switch rec.Status {
		case repository.TaskStatusCompleted:
			t.Logf("任务完成: 字幕 %d 条，时长 %.1f 秒，模型 %s", rec.SubtitleCount, rec.Duration, rec.Model)
			segments, err := app.ListSubtitles(task.ID)
			if err != nil {
				t.Fatalf("读取字幕失败: %v", err)
			}
			for _, seg := range segments {
				t.Logf("  [%06.2f-%06.2f] %s", seg.Start, seg.End, seg.SourceText)
			}
			content, err := app.ExportSubtitles(task.ID, "srt")
			if err != nil {
				t.Fatalf("导出失败: %v", err)
			}
			t.Logf("导出预览: %q", content[:min(len(content), 200)])
			return
		case repository.TaskStatusFailed:
			t.Fatalf("任务失败: %s", rec.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("等待任务完成超时")
}
