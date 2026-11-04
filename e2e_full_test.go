package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
)

// TestRealFullPipeline 是可选的真实全链路测试：FFmpeg 提取 → faster-whisper 识别
// → Ollama 翻译 → 多格式导出。
//
// 运行条件（全部满足才会执行）：
//   - TRANSCRIBE_REAL_E2E=1
//   - TRANSCRIBE_E2E_AUDIO 指向一段含人声的音视频
//   - TRANSCRIBE_E2E_TRANSLATE=1 时才会调用本机 Ollama
//
// 模型目录可通过 TRANSCRIBE_E2E_MODEL_DIR 复用已有模型，避免联网下载。
func TestRealFullPipeline(t *testing.T) {
	if os.Getenv("TRANSCRIBE_REAL_E2E") != "1" {
		t.Skip("未设置 TRANSCRIBE_REAL_E2E=1，跳过真实全链路测试")
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
	workDir := filepath.Join(home, "transcribe-whisper-e2e", time.Now().Format("150405"))
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	if err := app.openDatabase(filepath.Join(workDir, "e2e.db")); err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer app.shutdown(context.Background())

	pythonPath := firstEnv("TRANSCRIBE_E2E_PYTHON", "python")
	modelID := firstEnv("TRANSCRIBE_E2E_MODEL", "tiny")
	modelDir := os.Getenv("TRANSCRIBE_E2E_MODEL_DIR")
	translate := os.Getenv("TRANSCRIBE_E2E_TRANSLATE") == "1"

	settings := repository.DefaultSettings()
	settings.PythonPath = pythonPath
	settings.DefaultModel = modelID
	settings.TranslationProvider = "ollama"
	settings.TranslationEndpoint = firstEnv("TRANSCRIBE_E2E_OLLAMA", "http://127.0.0.1:11434")
	settings.TranslationModel = firstEnv("TRANSCRIBE_E2E_OLLAMA_MODEL", "qwen2.5:7b")
	settings.CloudPrivacyConfirm = true
	settings.TranslationStyle = "subtitle"
	settings.Glossary = map[string]string{"timestamps": "时间戳", "pipeline": "流水线"}
	if modelDir != "" {
		settings.ModelDir = filepath.Dir(modelDir)
	} else {
		settings.ModelDir = filepath.Join(workDir, "models")
	}

	app.mu.Lock()
	app.settings = settings
	if app.store != nil {
		_ = app.store.SaveSettings(context.Background(), settings)
	}
	// 用测试准备好的模型目录重建管理器，跳过多镜像下载。
	app.models = modelmanager.New(modelmanager.Options{
		RootDir:   settings.ModelDir,
		Catalog:   modelmanager.DefaultCatalog(),
		UserAgent: "transcribe-whisper/1.0",
	})
	app.mu.Unlock()

	input := CreateTaskInput{
		SourceFile:     audio,
		SourceLanguage: "auto",
		TargetLanguage: "zh-CN",
		OutputFormat:   "srt",
		Engine:         "faster-whisper",
		Model:          modelID,
		Translate:      translate,
	}
	if translate {
		input.Provider = "ollama"
	}

	task, err := app.CreateTask(input)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.StartTask(task.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}

	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		rec, err := app.store.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("读取任务失败: %v", err)
		}
		switch rec.Status {
		case repository.TaskStatusCompleted:
			t.Logf("任务完成: 字幕 %d 条 / 已翻译 %d 条 / 时长 %.1fs / 模型 %s",
				rec.SubtitleCount, rec.TranslatedCount, rec.Duration, rec.Model)
			if rec.Error != "" {
				t.Logf("警告信息: %s", rec.Error)
			}
			segments, err := app.ListSubtitles(task.ID)
			if err != nil {
				t.Fatalf("读取字幕失败: %v", err)
			}
			for _, seg := range segments {
				t.Logf("  [%06.2f-%06.2f] %s → %s", seg.Start, seg.End, seg.SourceText, seg.Translated)
			}
			if translate {
				for _, seg := range segments {
					if strings.TrimSpace(seg.Translated) == "" {
						t.Fatalf("开启翻译后仍有空译文: %+v", seg)
					}
				}
				joined := ""
				for _, seg := range segments {
					joined += seg.Translated + " "
				}
				// 术语表里 pipeline→流水线，译文应体现术语译法而不是原词。
				if strings.Contains(joined, "pipeline") && !strings.Contains(joined, "流水线") {
					t.Logf("提示：译文未采用术语表译法（模型可能改写了词形）")
				}
			}
			// 五种格式都要能导出且非空。
			for _, format := range []string{"srt", "vtt", "txt", "json", "bilingual-srt"} {
				content, err := app.ExportSubtitles(task.ID, format)
				if err != nil {
					t.Fatalf("导出 %s 失败: %v", format, err)
				}
				if strings.TrimSpace(content) == "" {
					t.Fatalf("导出 %s 结果为空", format)
				}
				t.Logf("导出 %s 长度 %d 字节", format, len(content))
			}
			return
		case repository.TaskStatusFailed:
			t.Fatalf("任务失败: %s", rec.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("等待任务完成超时")
}

// firstEnv 返回第一个非空环境变量。
func firstEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}