package translation

import (
	"errors"
	"strings"
	"testing"
)

// 本文件覆盖「模型返回对象形态」的解析路径。
//
// 背景：Ollama 的 format=json 会强制模型输出 JSON 对象，实测 qwen2.5 会给出
// {"1":"欢迎来到…"} 这种 id->译文 的映射，而不是提示词里建议的数组。
// 早期实现只认数组，导致真实本地模型的翻译 100% 失败。

func TestParseChoicesAcceptsIDToTextObject(t *testing.T) {
	raw := `{"1": "欢迎来到转录流水线演示。", "2": "模型会生成带准确时间戳的字幕。"}`
	choices, err := parseChoices(raw)
	if err != nil {
		t.Fatalf("id->译文对象应当被接受: %v", err)
	}
	if len(choices) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d", len(choices))
	}
	// 结果按 id 排序，顺序稳定。
	if choices[0].ID != "1" || choices[0].Text != "欢迎来到转录流水线演示。" {
		t.Fatalf("第一条解析不正确: %+v", choices[0])
	}
	if choices[1].ID != "2" {
		t.Fatalf("第二条解析不正确: %+v", choices[1])
	}
}

func TestParseChoicesAcceptsObjectInCodeFence(t *testing.T) {
	raw := "```json\n{\"a\": \"甲\", \"b\": \"乙\"}\n```"
	choices, err := parseChoices(raw)
	if err != nil {
		t.Fatalf("代码块包裹的对象应当被接受: %v", err)
	}
	if len(choices) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d", len(choices))
	}
}

func TestParseChoicesAcceptsWrappedArray(t *testing.T) {
	raw := `{"translations": [{"id": "1", "text": "甲"}, {"id": "2", "text": "乙"}]}`
	choices, err := parseChoices(raw)
	if err != nil {
		t.Fatalf("包装数组应当被接受: %v", err)
	}
	if len(choices) != 2 || choices[0].ID != "1" || choices[1].Text != "乙" {
		t.Fatalf("包装数组解析不正确: %+v", choices)
	}
}

func TestParseChoicesArrayStillPreferred(t *testing.T) {
	raw := `[{"id":"1","text":"甲"}]`
	choices, err := parseChoices(raw)
	if err != nil {
		t.Fatalf("数组形态应当继续可用: %v", err)
	}
	if len(choices) != 1 || choices[0].Text != "甲" {
		t.Fatalf("数组解析不正确: %+v", choices)
	}
}

func TestParseChoicesObjectWithNonStringValueFails(t *testing.T) {
	raw := `{"1": {"text": "甲"}}`
	if _, err := parseChoices(raw); err == nil {
		t.Fatal("值不是字符串的对象应当报错")
	}
}

func TestParseChoicesObjectWithEmptyTextFails(t *testing.T) {
	raw := `{"1": "  "}`
	if _, err := parseChoices(raw); err == nil {
		t.Fatal("译文为空应当报错")
	}
}

func TestParseChoicesEmptyObjectFails(t *testing.T) {
	if _, err := parseChoices(`{}`); err == nil {
		t.Fatal("空对象应当报错")
	}
}

// 未闭合的数组不能因为「对象部分仍然合法」而被误读成 id->译文 映射。
func TestParseChoicesUnclosedArrayIsNotMistakenForObject(t *testing.T) {
	raw := `[{"id":"1","text":"甲"}`
	_, err := parseChoices(raw)
	if err == nil {
		t.Fatal("未闭合数组应当报错，而不是被当成 id->译文 映射")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *Error，实际 %T", err)
	}
	if pe.Kind != KindRetryable {
		t.Fatalf("格式错误应可重试，实际 %s", pe.Kind)
	}
}

func TestParseChoicesObjectWithStructuralKeysFails(t *testing.T) {
	// 模型有时会返回单条记录对象，这无法对应整批条目，必须报错重试。
	for _, raw := range []string{
		`{"id":"1","text":"甲"}`,
		`{"translation":"甲"}`,
		`{"segments":"甲"}`,
	} {
		if _, err := parseChoices(raw); err == nil {
			t.Fatalf("结构字段对象应当报错: %s", raw)
		}
	}
}

func TestParseChoicesIgnoresSurroundingProse(t *testing.T) {
	raw := `好的，以下是翻译结果：{"1":"甲","2":"乙"} 希望对你有帮助。`
	choices, err := parseChoices(raw)
	if err != nil {
		t.Fatalf("前后夹带解释文字应当被忽略: %v", err)
	}
	if len(choices) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d", len(choices))
	}
}

func TestSystemPromptAllowsObjectForm(t *testing.T) {
	prompt := buildSystemPrompt(Request{TargetLanguage: "zh-CN"})
	for _, want := range []string{"JSON 对象", "JSON", "id"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少 %q", want)
		}
	}
	// 不能只要求数组：Ollama 的 format=json 下模型会倾向输出对象。
	if strings.Contains(prompt, "只输出一个 JSON 数组") {
		t.Fatal("系统提示不应强制要求数组形态")
	}
}
