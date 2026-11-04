package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/service"
)

// pickEngineForTest 用与 app.go 相同的路由规则挑选引擎。
func pickEngineForTest(mgr *modelmanager.ManagerImpl, modelID string, ct2, st asr.Engine) asr.Engine {
	selector := service.StaticEngineSelector{
		Fallback: ct2,
		Engines: map[string]asr.Engine{
			modelmanager.EngineFasterWhisper: ct2,
			modelmanager.EngineTransformers:  st,
		},
		Lookup: func(id string) string {
			if entry, ok := mgr.Find(id); ok {
				return entry.Engine
			}
			return ""
		},
	}
	return selector.Select(modelID, "faster-whisper")
}

// TestRealCohereTranscribe 验证 cohere-transcribe-03-2026 能真实跑通识别。
//
// 与 faster-whisper 的 e2e 不同，这条链路验证的是 transformers 引擎：
//  1. 模型清单里有 cohere，且引擎标记为 transformers；
//  2. 引擎选择器把 cohere 路由到 transformers 而不是 faster-whisper；
//  3. 本地目录能被识别、注册并解析；
//  4. 真实调用 Python worker 得到带时间轴的中文/英文字幕。
//
// 需要本机已下载 cohere 模型（约 4.13GB）与 torch/transformers 环境，
// 缺少时跳过，不假装成功。
func TestRealCohereTranscribe(t *testing.T) {
	if os.Getenv("TRANSCRIBE_E2E_COHERE") != "1" {
		t.Skip("设置 TRANSCRIBE_E2E_COHERE=1 后运行")
	}

	// 第一步：清单与引擎标记。
	entry, ok := modelmanager.FindCatalogEntry("cohere-transcribe")
	if !ok {
		t.Fatal("清单应包含 cohere-transcribe")
	}
	if entry.Engine != modelmanager.EngineTransformers {
		t.Fatalf("cohere 应使用 transformers 引擎，实际 %q", entry.Engine)
	}

	// 第二步：定位本地模型目录。
	modelDir := os.Getenv("TRANSCRIBE_E2E_COHERE_DIR")
	if modelDir == "" {
		modelDir = filepath.Join("tmp-cohere-models", "cohere-transcribe")
	}
	// 统一成绝对路径再比较，避免相对路径与绝对路径的口径差异。
	if abs, err := filepath.Abs(modelDir); err == nil {
		modelDir = abs
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skipf("本机没有 cohere 模型（%s），跳过", modelDir)
	}

	// 第三步：识别格式与注册。
	engine, _, err := modelmanager.DetectLocalModel(modelDir)
	if err != nil {
		t.Fatalf("识别本地目录失败: %v", err)
	}
	if engine != modelmanager.EngineTransformers {
		t.Fatalf("应识别为 transformers 格式，实际 %q", engine)
	}

	mgr := modelmanager.New(modelmanager.Options{RootDir: t.TempDir()})
	// 用别名注册：清单里已有 cohere-transcribe，直接注册会撞 ID。
	if _, err := mgr.AddLocalModel("cohere-local", modelDir); err != nil {
		t.Fatalf("注册本地模型失败: %v", err)
	}

	// 第四步：引擎选择器路由。
	ct2 := asr.NewFasterWhisperEngine()
	st := asr.NewTransformersEngine()
	selected := pickEngineForTest(mgr, "cohere-local", ct2, st)
	if selected.Name() != "transformers" {
		t.Fatalf("cohere 本地模型应路由到 transformers，实际 %s", selected.Name())
	}

	// 第五步：真实识别。
	audioPath := os.Getenv("TRANSCRIBE_E2E_AUDIO")
	if audioPath == "" {
		t.Skip("需要 TRANSCRIBE_E2E_AUDIO 指向一个 16kHz wav，跳过真实识别")
	}
	if resolved, err := mgr.Ensure(context.Background(), "cohere-local", nil); err != nil {
		t.Fatalf("解析模型目录失败: %v", err)
	} else if resolved != modelDir {
		t.Fatalf("应返回原始目录 %q，实际 %q", modelDir, resolved)
	}

	st.PythonPath = os.Getenv("TRANSCRIBE_E2E_PYTHON")
	ok, detail := st.Available(context.Background())
	if !ok {
		t.Fatalf("transformers 引擎不可用: %s", detail)
	}
	t.Logf("引擎可用性: %s", detail)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()
	result, err := selected.Transcribe(ctx, asr.Request{
		AudioPath:      audioPath,
		ModelPath:      modelDir,
		ModelName:      "cohere-transcribe",
		Language:       os.Getenv("TRANSCRIBE_E2E_LANG"),
		Device:         "cuda",
		SegmentTimeout: 1800,
	}, func(ratio float64, message string) {
		t.Logf("[%.0f%%] %s", ratio*100, message)
	})
	if err != nil {
		t.Fatalf("cohere 识别失败: %v", err)
	}
	elapsed := time.Since(start)

	if len(result.Segments) == 0 {
		t.Fatal("识别结果为空")
	}
	if result.Engine != "transformers" {
		t.Fatalf("结果引擎应为 transformers，实际 %q", result.Engine)
	}
	if result.Duration <= 0 {
		t.Fatalf("时长异常: %v", result.Duration)
	}

	t.Logf("识别完成，用时 %.1fs，%d 条字幕，时长 %.2fs", elapsed.Seconds(), len(result.Segments), result.Duration)
	for i, seg := range result.Segments {
		if i >= 5 {
			t.Logf("  ... 共 %d 条", len(result.Segments))
			break
		}
		if seg.Start > seg.End {
			t.Fatalf("第 %d 条时间轴倒挂: %.2f > %.2f", i+1, seg.Start, seg.End)
		}
		if seg.End > result.Duration+1 {
			t.Fatalf("第 %d 条结束时间 %.2f 超出音频时长 %.2f", i+1, seg.End, result.Duration)
		}
		t.Logf("  [%.2f-%.2f] %s", seg.Start, seg.End, seg.Text)
	}
}
