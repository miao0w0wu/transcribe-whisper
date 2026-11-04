package translation

import (
	"strings"
	"testing"
)

// 露骨语气必须给出足够具体的指令，否则模型会自行「净化」内容。
func TestStyleGuideExplicit(t *testing.T) {
	guide := styleGuide("explicit")
	if guide == "" {
		t.Fatal("露骨语气应有指令")
	}
	// 关键约束逐条确认，避免以后被误删。
	for _, want := range []string{
		"露骨",
		"成人",
		"不要淡化",
		"直白",
		"委婉",
		"脏话",
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("露骨语气指令缺少关键词 %q：\n%s", want, guide)
		}
	}
}

func TestStyleGuideExplicitAliases(t *testing.T) {
	// 中文别名也应识别为同一风格。
	canonical := styleGuide("explicit")
	for _, alias := range []string{"露骨", "成人", "EXPLICIT", "  Explicit  "} {
		if got := styleGuide(alias); got != canonical {
			t.Fatalf("别名 %q 应映射到露骨风格，实际：\n%s", alias, got)
		}
	}
}

// 露骨指令不能污染其它风格。
func TestStyleGuideOthersUnaffected(t *testing.T) {
	for _, style := range []string{"subtitle", "formal", "casual", "literal", "free"} {
		got := styleGuide(style)
		if strings.Contains(got, "露骨") || strings.Contains(got, "成人向") {
			t.Fatalf("风格 %q 不应包含露骨指令：%s", style, got)
		}
	}
}

// 露骨风格必须能通过完整系统提示词组装。
func TestBuildSystemPromptExplicit(t *testing.T) {
	prompt := buildSystemPrompt(Request{
		TargetLanguage: "zh-CN",
		Style:          "explicit",
		ContentMode:    "subtitle",
	})
	for _, want := range []string{"只输出 JSON", "露骨", "成人"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示词缺少 %q：\n%s", want, prompt)
		}
	}
	// 仍然要求只输出 JSON，露骨指令不能破坏输出协议。
	if !strings.Contains(prompt, "不要输出任何解释") {
		t.Fatalf("露骨风格不应破坏 JSON 输出约束：\n%s", prompt)
	}
}

// 不同风格会进入缓存键，切换风格必须重新请求。
func TestExplicitStyleAffectsCacheKey(t *testing.T) {
	base := cacheKey("p", "hello", "en", "zh-CN", "subtitle", "subtitle")
	explicit := cacheKey("p", "hello", "en", "zh-CN", "explicit", "subtitle")
	if base == explicit {
		t.Fatal("露骨风格与字幕风格的缓存键不应相同")
	}
}
