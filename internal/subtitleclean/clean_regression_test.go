package subtitleclean

import (
	"strings"
	"testing"

	"transcribe-whisper/internal/asr"
)

// TestCollapseLongLoopsKeepsShortEmphasis 保护「正常语气重复」不被误删。
//
// 这是本规则最危险的边界：真实的日语对白里大量出现「はいはい」「うんうんうん」
// 「あっあっあっ」这类重复，一旦误判就会把正常字幕改坏。判定必须只看
// 重复段总长，不能只看重复次数。
func TestCollapseLongLoopsKeepsShortEmphasis(t *testing.T) {
	cases := []string{
		"はいはい。",
		"そうそう。",
		"うんうんうん。",
		"ねえねえねえ",
		"だめだめだめ！",
		"すごいすごい",
		"あっあっあっ",
		"ちょっとちょっと",
		"もうもう",
		"いやいや",
		"ああああ",
		"ふんふんふん",
		"あははははは",
		"やだやだやだ",
		"はい。はい。はい。",
		"やめて。やめて。やめて。",
		"あ。あ。あ。",
	}
	for _, c := range cases {
		if got := CollapseLongLoops(c); got != c {
			t.Errorf("短语气重复不应被改动：%q -> %q", c, got)
		}
	}
}

// TestCollapseLongLoopsCollapsesRealLoops 确认真实的解码退化会被折叠。
func TestCollapseLongLoopsCollapsesRealLoops(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"单字符循环", strings.Repeat("あ", 429)},
		{"双字符循环", strings.Repeat("ふん", 150)},
		{"促音循环", strings.Repeat("あっ", 200)},
		{"四字符循环", strings.Repeat("そうそう", 80)},
	}
	for _, c := range cases {
		got := CollapseLongLoops(c.input)
		if got == c.input {
			t.Errorf("%s 应被折叠，实际未改动（长度 %d）", c.name, len([]rune(c.input)))
			continue
		}
		// 折叠后应保留一点点语气，但必须显著变短。
		if n := len([]rune(got)); n > 8 {
			t.Errorf("%s 折叠后应不超过 8 字，实际 %d 字：%q", c.name, n, got)
		}
	}
}

// TestCollapseLongLoopsPreservesSurroundingText 确认只折叠循环部分，前后正文保留。
func TestCollapseLongLoopsPreservesSurroundingText(t *testing.T) {
	input := "すごいね" + strings.Repeat("あ", 300) + "またね"
	got := CollapseLongLoops(input)
	if !strings.HasPrefix(got, "すごいね") {
		t.Errorf("循环前的正文应保留，实际 %q", got)
	}
	if !strings.HasSuffix(got, "またね") {
		t.Errorf("循环后的正文应保留，实际 %q", got)
	}
	if len([]rune(got)) > 20 {
		t.Errorf("折叠后应显著变短，实际 %d 字", len([]rune(got)))
	}
}

// TestCleanCrossLanguageOnlyForNonLatin 确认拉丁语系不启用跨语种规则。
//
// 英语视频里出现法语、西班牙语是正常现象（歌曲、引用），不能一律判为幻觉。
func TestCleanCrossLanguageOnlyForNonLatin(t *testing.T) {
	input := []asr.Segment{
		{ID: "a", Sequence: 1, Text: "Hello everyone, welcome back."},
		{ID: "b", Sequence: 2, Text: "Bonjour, comment allez-vous?"},
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "en"})
	if len(got.Segments) != 2 {
		t.Errorf("拉丁语系不应启用跨语种过滤，期望保留 2 条，实际 %d 条", len(got.Segments))
	}
}

// TestCleanCrossLanguageKeepsMixedText 确认夹杂英文术语的日语不被误伤。
func TestCleanCrossLanguageKeepsMixedText(t *testing.T) {
	input := []asr.Segment{
		{ID: "a", Sequence: 1, Text: "このAPIのドキュメントを見て"},
		{ID: "b", Sequence: 2, Text: "React と Vue の違い"},
		{ID: "c", Sequence: 3, Text: "OK"},
		{ID: "d", Sequence: 4, Text: "パソコンのCPUが"},
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	if len(got.Segments) != len(input) {
		var kept []string
		for _, s := range got.Segments {
			kept = append(kept, s.Text)
		}
		t.Errorf("夹杂英文术语的日语不应被丢弃，期望 %d 条，实际 %d 条：%v",
			len(input), len(got.Segments), kept)
	}
}

// TestCleanCrossLanguageDropsPureForeign 确认真实的跨语种幻觉会被丢弃。
func TestCleanCrossLanguageDropsPureForeign(t *testing.T) {
	input := []asr.Segment{
		{ID: "a", Sequence: 1, Text: "I don't give much."},
		{ID: "b", Sequence: 2, Text: "O que é que você está fazendo?"},
		{ID: "c", Sequence: 3, Text: "Il y avait des choses."},
		{ID: "d", Sequence: 4, Text: "本当の対白"},
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	if len(got.Segments) != 1 {
		var kept []string
		for _, s := range got.Segments {
			kept = append(kept, s.Text)
		}
		t.Errorf("纯外语条目应被丢弃，期望保留 1 条，实际 %d 条：%v", len(got.Segments), kept)
	}
	if len(got.Segments) == 1 && got.Segments[0].Text != "本当の対白" {
		t.Errorf("应保留真实日语条目，实际 %q", got.Segments[0].Text)
	}
}

// TestCleanCrossLanguageOffAtLowLevel 确认 low 档不启用跨语种规则。
func TestCleanCrossLanguageOffAtLowLevel(t *testing.T) {
	input := []asr.Segment{{ID: "a", Sequence: 1, Text: "I don't give much."}}
	got := Clean(input, Options{Level: LevelLow, ExpectedLanguage: "ja"})
	if len(got.Segments) != 1 {
		t.Error("low 档不应启用跨语种过滤")
	}
}

// TestCleanExpectedLanguageEmptyDisablesRule 确认不传语言时行为与改动前一致。
func TestCleanExpectedLanguageEmptyDisablesRule(t *testing.T) {
	input := []asr.Segment{{ID: "a", Sequence: 1, Text: "I don't give much."}}
	for _, level := range []Level{LevelLow, LevelMedium, LevelHigh} {
		got := Clean(input, Options{Level: level})
		if len(got.Segments) != 1 {
			t.Errorf("ExpectedLanguage 为空时 %s 档不应丢弃跨语种条目", level)
		}
	}
}

// TestCleanLongLoopKeepsTimestamps 确认折叠只改文本，不动时间轴。
func TestCleanLongLoopKeepsTimestamps(t *testing.T) {
	input := []asr.Segment{{
		ID:       "x",
		Sequence: 42,
		Start:    100.5,
		End:      130.25,
		Text:     strings.Repeat("あ", 400),
	}}
	got := Clean(input, Options{Level: LevelMedium})
	if len(got.Segments) != 1 {
		t.Fatalf("期望保留 1 条，实际 %d 条", len(got.Segments))
	}
	s := got.Segments[0]
	if s.ID != "x" || s.Sequence != 42 || s.Start != 100.5 || s.End != 130.25 {
		t.Errorf("折叠不应改动标识或时间戳，实际 %+v", s)
	}
	if len([]rune(s.Text)) > 8 {
		t.Errorf("长循环应被折叠，实际 %d 字", len([]rune(s.Text)))
	}
}
