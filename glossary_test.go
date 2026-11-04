package main

import (
	"testing"
)

func TestParseGlossaryBasic(t *testing.T) {
	glossary, err := parseGlossary("API=接口\nLLM=大语言模型\n")
	if err != nil {
		t.Fatalf("解析术语表失败: %v", err)
	}
	if len(glossary) != 2 || glossary["API"] != "接口" || glossary["LLM"] != "大语言模型" {
		t.Fatalf("解析结果不正确: %+v", glossary)
	}
}

func TestParseGlossaryIgnoresBlankAndComments(t *testing.T) {
	text := "\n# 这是一行注释\n\nAPI=接口\n   \n"
	glossary, err := parseGlossary(text)
	if err != nil {
		t.Fatalf("解析术语表失败: %v", err)
	}
	if len(glossary) != 1 || glossary["API"] != "接口" {
		t.Fatalf("空行与注释应被忽略: %+v", glossary)
	}
}

func TestParseGlossaryAcceptsFullWidthEquals(t *testing.T) {
	glossary, err := parseGlossary("API＝接口")
	if err != nil {
		t.Fatalf("全角等号应被接受: %v", err)
	}
	if glossary["API"] != "接口" {
		t.Fatalf("全角等号解析结果不正确: %+v", glossary)
	}
}

func TestParseGlossaryKeepsEqualsInTarget(t *testing.T) {
	glossary, err := parseGlossary("x=a=b")
	if err != nil {
		t.Fatalf("目标值中含等号应被保留: %v", err)
	}
	if glossary["x"] != "a=b" {
		t.Fatalf("目标值解析不正确: %+v", glossary)
	}
}

func TestParseGlossaryRejectsMalformedLine(t *testing.T) {
	if _, err := parseGlossary("API=接口\n这一行没有等号"); err == nil {
		t.Fatal("缺少等号的行应当报错")
	}
	if _, err := parseGlossary("=接口"); err == nil {
		t.Fatal("缺少源术语应当报错")
	}
	if _, err := parseGlossary("API="); err == nil {
		t.Fatal("缺少目标译法应当报错")
	}
}

func TestParseGlossaryErrorMentionsLineNumber(t *testing.T) {
	_, err := parseGlossary("ok=好的\n坏行")
	if err == nil {
		t.Fatal("应当报错")
	}
	if !contains(err.Error(), "第 2 行") {
		t.Fatalf("错误信息应包含行号，实际: %v", err)
	}
}

func TestFormatGlossaryRoundTrip(t *testing.T) {
	source := map[string]string{"API": "接口", "LLM": "大语言模型"}
	text := formatGlossary(source)
	glossary, err := parseGlossary(text)
	if err != nil {
		t.Fatalf("回合解析失败: %v", err)
	}
	if len(glossary) != len(source) {
		t.Fatalf("回合后条目数不一致: %+v", glossary)
	}
	for key, value := range source {
		if glossary[key] != value {
			t.Fatalf("回合后 %s 的值不一致: %q", key, glossary[key])
		}
	}
}

func TestFormatGlossaryIsSorted(t *testing.T) {
	text := formatGlossary(map[string]string{"z": "1", "a": "2", "m": "3"})
	want := "a=2\nm=3\nz=1"
	if text != want {
		t.Fatalf("术语表应稳定排序，实际 %q", text)
	}
}

func TestFormatGlossaryEmpty(t *testing.T) {
	if formatGlossary(nil) != "" || formatGlossary(map[string]string{}) != "" {
		t.Fatal("空术语表应返回空字符串")
	}
}

func TestDefaultStyle(t *testing.T) {
	cases := map[string]string{
		"":         "subtitle",
		"SUBTITLE": "subtitle",
		" formal ": "formal",
		"casual":   "casual",
		"literal":  "literal",
		"unknown":  "subtitle",
	}
	for input, want := range cases {
		if got := defaultStyle(input); got != want {
			t.Fatalf("defaultStyle(%q)=%q，期望 %q", input, got, want)
		}
	}
}

// contains 是 strings.Contains 的测试内联版本，避免再多一个导入。
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}