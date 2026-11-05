package subtitleclean

import (
	"strings"
	"testing"
)

// TestCollapseLongLoopsSentenceLevel 确认整句级重复（单元远超 4 rune）也能折叠。
//
// 背景：真实 ASR 数据里出现过 "I don't give much. " ×60（1190 字符）、
// "Ai, que monte. " ×70 这类整句循环。这类文本送到翻译模型后，模型会跟着
// 陷入循环，Ollama 以 500「token repeat limit reached」中止整批，
// 导致同批另外 19 条正常字幕一起失败。
func TestCollapseLongLoopsSentenceLevel(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, got string)
	}{
		{
			name:  "英文整句重复60次",
			input: strings.Repeat("I don't give much. ", 60),
			check: func(t *testing.T, got string) {
				if len([]rune(got)) > 100 {
					t.Errorf("整句循环未折叠，仍长 %d 字符：%q", len([]rune(got)), got)
				}
			},
		},
		{
			name:  "葡语整句重复",
			input: strings.Repeat("Ai, que monte. ", 70),
			check: func(t *testing.T, got string) {
				if len([]rune(got)) > 100 {
					t.Errorf("葡语整句循环未折叠，仍长 %d 字符", len([]rune(got)))
				}
			},
		},
		{
			name:  "中文整句重复",
			input: strings.Repeat("我真的很喜欢你。", 15),
			check: func(t *testing.T, got string) {
				if len([]rune(got)) > 60 {
					t.Errorf("中文整句循环未折叠，仍长 %d 字符：%q", len([]rune(got)), got)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.check(t, CollapseLongLoops(c.input))
		})
	}
}

// TestCollapseLongLoopsKeepsLegitRepeatedDialogue 保护真实的「重复说同一句话」
// 不被折叠：口语里重复两遍同一句台词很常见（强调、撒娇、复读）。
func TestCollapseLongLoopsKeepsLegitRepeatedDialogue(t *testing.T) {
	inputs := []string{
		"好き。好き。", // 短重复，远低于 span 阈值
		"もう一回言って。もう一回言って。", // 9 字符 ×2 = 18 < 60
		"ごめんなさい、ごめんなさい。",   // 短句重复两次
		"はい。", // 单句
		"いいよいいよ、別に気にしないで。",   // 正常对白，无长循环
		"ダメだよダメだよ、そんなことしちゃ。", // 重复但 span 不足
	}
	for _, in := range inputs {
		if got := CollapseLongLoops(in); got != in {
			t.Errorf("正常对白不应被折叠：%q -> %q", in, got)
		}
	}
}

// TestCollapseLongLoopsRealPoisonSegments 用真实毒条目验证折叠效果。
func TestCollapseLongLoopsRealPoisonSegments(t *testing.T) {
	cases := []string{
		// 真实数据 id=87
		strings.Repeat("I don't give much. ", 60),
		// 真实数据 id=100：「でもこっちもいいもんね」+「あ」×约180
		"でもこっちもいいもんね" + strings.Repeat("あ", 180),
		// 真实数据 id=178：「あっ」×100+
		strings.Repeat("あっ", 120),
		// 真实数据 id=241
		strings.Repeat("The best of the best of the best of the ", 30),
	}
	for _, in := range cases {
		got := CollapseLongLoops(in)
		if len([]rune(got)) > 150 {
			t.Errorf("毒条目未充分折叠：%d -> %d 字符", len([]rune(in)), len([]rune(got)))
		}
	}
}
