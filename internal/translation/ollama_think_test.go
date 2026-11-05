package translation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOllamaSendsThinkFalse 保护「默认关闭思考」这一关键行为。
//
// 背景：推理模型（qwen3 / qwen3.5 / deepseek-r1）默认会先输出一大段 reasoning，
// 且 reasoning 走 message.thinking 而不是 message.content。批量字幕翻译里这段
// 思考毫无用处，却会吃光 num_predict 预算，导致 content 为空、
// 调用方 20~25 秒超时被触发，界面报「context deadline exceeded」。
// 实测 huihui-qwen3.5-9b：不关思考 29.6 秒且 content 为空；关掉后 3.9 秒正常。
// 所以请求体里必须带 think=false，这个测试守住它不被改回去。
func TestOllamaSendsThinkFalse(t *testing.T) {
	var gotThink any
	var sawThink bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体不是合法 JSON：%v", err)
			http.Error(w, "bad", 400)
			return
		}
		gotThink, sawThink = body["think"], true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"1\":\"你好\"}"},"done":true}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "m"})
	if _, err := p.Translate(t.Context(), Request{
		Items:          []Item{{ID: "1", Text: "hello"}},
		TargetLanguage: "zh-CN",
	}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}

	if !sawThink {
		t.Fatal("请求体缺少 think 字段：推理模型会陷入空正文")
	}
	if v, ok := gotThink.(bool); !ok || v {
		t.Fatalf("think 应为 false，实际为 %#v", gotThink)
	}
}

// TestOllamaKeepThinkingOmitsFlag 确认显式要求保留思考时不发送该字段。
func TestOllamaKeepThinkingOmitsFlag(t *testing.T) {
	var sawThink bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, sawThink = body["think"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"1\":\"你好\"}"},"done":true}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "m", KeepThinking: true})
	if _, err := p.Translate(t.Context(), Request{
		Items: []Item{{ID: "1", Text: "hello"}}, TargetLanguage: "zh-CN",
	}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if sawThink {
		t.Fatal("KeepThinking=true 时不应发送 think 字段")
	}
}

// TestOllamaEmptyContentWithThinking 确认「思考吃光预算」时给出可操作的中文提示，
// 而不是一句无用的「内容为空」。
func TestOllamaEmptyContentWithThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"","thinking":"Thinking Process:\n1. Analyze...\n2. ..."},"done":true,"done_reason":"length"}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "think-model"})
	_, err := p.Translate(t.Context(), Request{
		Items: []Item{{ID: "1", Text: "hello"}}, TargetLanguage: "zh-CN",
	})
	if err == nil {
		t.Fatal("正文为空且有思考内容时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "思考") || !strings.Contains(msg, "think-model") {
		t.Fatalf("错误信息应提示思考吃光预算并带上模型名，实际：%s", msg)
	}
}

// TestOllamaRepeatLoopFailureIsDetected 确认 token repeat limit 错误被识别为
// 解码循环（流水线据此拆分批次隔离肇事条目）。
func TestOllamaRepeatLoopFailureIsDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"prediction aborted, token repeat limit reached"}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "m"})
	_, err := p.Translate(t.Context(), Request{
		Items: []Item{{ID: "1", Text: "あああ"}}, TargetLanguage: "zh-CN",
	})
	if err == nil {
		t.Fatal("应当返回错误")
	}
	if !isRepeatLoopError(err) {
		t.Fatalf("应被识别为重复循环错误，实际：%v", err)
	}
}

// TestPipelineSplitsBatchOnRepeatLoop 确认一条烂字幕不会连累同批其他条目。
//
// 模拟 Ollama 行为：只有包含 "POISON" 的批次会返回 500 重复循环错误，
// 其余批次正常。批量翻译后，正常条目必须全部拿到译文，只有毒条目失败。
func TestPipelineSplitsBatchOnRepeatLoop(t *testing.T) {
	provider := &loopFailingProvider{poison: "POISON"}
	tr := NewTranslator(provider, testOptions())

	items := []Item{
		{ID: "1", Text: "こんにちは"},
		{ID: "2", Text: "POISON ああああああ"},
		{ID: "3", Text: "ありがとう"},
		{ID: "4", Text: "さようなら"},
	}

	got, err := tr.Translate(t.Context(), items, nil)
	byID := make(map[string]string, len(got))
	for _, c := range got {
		byID[c.ID] = c.Text
	}

	for _, id := range []string{"1", "3", "4"} {
		if strings.TrimSpace(byID[id]) == "" {
			t.Errorf("id=%s 是正常条目，不应因同批有毒条目而失败（拿到：%v，err：%v）", id, byID, err)
		}
	}
	if _, ok := byID["2"]; ok {
		t.Errorf("毒条目不应翻译成功：%v", byID["2"])
	}
	if provider.calls < 2 {
		t.Errorf("应发生批次拆分，实际只调用 %d 次", provider.calls)
	}
}

// loopFailingProvider 模拟「批次里含毒文本就整批 500」的 Ollama 行为。
type loopFailingProvider struct {
	poison string
	calls  int
}

func (p *loopFailingProvider) Name() string { return "loop-fake" }

func (p *loopFailingProvider) Translate(_ context.Context, req Request) ([]Choice, error) {
	p.calls++
	out := make([]Choice, 0, len(req.Items))
	for _, it := range req.Items {
		if strings.Contains(it.Text, p.poison) {
			return nil, &Error{
				Kind:    KindFatal,
				Message: "Ollama 中止了预测：模型在翻译本批内容时陷入重复循环",
			}
		}
		out = append(out, Choice{ID: it.ID, Text: "译:" + it.Text})
	}
	return out, nil
}
