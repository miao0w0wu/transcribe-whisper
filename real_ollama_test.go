package main

import (
	"context"
	"os"
	"testing"
	"time"

	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/translation"
)

// TestPingRealOllamaThinkModel 用真实本机 Ollama 验证「测试翻译服务」不再超时。
//
// 这是用户报障的原始场景：设置里选了推理模型（huihui-qwen3.5-9b），
// 点「测试翻译服务」报 context deadline exceeded。ping 的预算是 25 秒，
// 而该模型不关思考时单次请求要 28~32 秒且正文为空。
//
// 需要本机 Ollama 与模型存在，否则跳过；设置 TRANSCRIBE_TEST_OLLAMA_MODEL 可换模型。
func TestPingRealOllamaThinkModel(t *testing.T) {
	if os.Getenv("TRANSCRIBE_TEST_REAL_OLLAMA") == "" {
		t.Skip("设置 TRANSCRIBE_TEST_REAL_OLLAMA=1 才跑真实 Ollama 测试")
	}
	model := os.Getenv("TRANSCRIBE_TEST_OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5:7b-instruct"
	}

	tester, err := newTranslationClient(repository.SettingsRecord{
		TranslationProvider: "ollama",
		TranslationEndpoint: "http://127.0.0.1:11434",
		TranslationModel:    model,
	})
	if err != nil {
		t.Fatalf("构造翻译客户端失败：%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	if err := pingProvider(ctx, tester); err != nil {
		t.Fatalf("测试翻译服务失败（耗时 %s）：%v", time.Since(start).Round(time.Millisecond), err)
	}
	t.Logf("模型 %s 连接正常，耗时 %s", model, time.Since(start).Round(time.Millisecond))
}

// TestPingRealOllamaTranslateGemma 验证专业翻译模型可用。
func TestPingRealOllamaTranslateGemma(t *testing.T) {
	if os.Getenv("TRANSCRIBE_TEST_REAL_OLLAMA") == "" {
		t.Skip("设置 TRANSCRIBE_TEST_REAL_OLLAMA=1 才跑真实 Ollama 测试")
	}
	tester, err := newTranslationClient(repository.SettingsRecord{
		TranslationProvider: "ollama",
		TranslationEndpoint: "http://127.0.0.1:11434",
		TranslationModel:    "translategemma:12b",
	})
	if err != nil {
		t.Fatalf("构造翻译客户端失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if err := pingProvider(ctx, tester); err != nil {
		t.Fatalf("translategemma 测试失败（耗时 %s）：%v", time.Since(start).Round(time.Millisecond), err)
	}
	t.Logf("translategemma:12b 连接正常，耗时 %s", time.Since(start).Round(time.Millisecond))
}

// 保证测试文件引用的包被使用。
var _ = translation.KindOllama
