package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/translation"
)

// providerTester 负责验证翻译服务是否可用。
type providerTester struct {
	provider translation.Provider
}

// newTranslationClient 依据设置构造 Provider 用于连接测试。
func newTranslationClient(settings repository.SettingsRecord) (*providerTester, error) {
	kind := settings.TranslationProvider
	if kind == "" {
		kind = "ollama"
	}
	if kind != "ollama" && kind != "openai" {
		return nil, errors.New("不支持的翻译服务：" + kind)
	}
	provider, err := translation.NewWithClient(translation.Config{
		Kind:    kind,
		BaseURL: settings.TranslationEndpoint,
		APIKey:  settings.TranslationAPIKey,
		Model:   settings.TranslationModel,
	}, nil)
	if err != nil {
		return nil, err
	}
	return &providerTester{provider: provider}, nil
}

// ping 发送一条最短测试字幕验证服务可用。
func (t *providerTester) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	_, err := t.provider.Translate(ctx, translation.Request{
		Items:          []translation.Item{{ID: "1", Text: "hello"}},
		SourceLanguage: "en",
		TargetLanguage: "zh-CN",
		Style:          "subtitle",
		ContentMode:    "subtitle",
	})
	if err != nil {
		return errors.New("翻译服务不可用：" + err.Error())
	}
	return nil
}

// isLocalProviderKind 判断 Provider 是否为本机服务。
func isLocalProviderKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "ollama")
}

// pingProvider 调用测试器。
func pingProvider(ctx context.Context, tester *providerTester) error {
	if tester == nil {
		return errors.New("翻译服务客户端未初始化")
	}
	return tester.ping(ctx)
}
