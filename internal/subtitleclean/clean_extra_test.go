package subtitleclean

import (
	"strings"
	"testing"

	"transcribe-whisper/internal/asr"
)

// segN 构造一条测试字幕（避免依赖 clean_test.go 里的 seg）。
func segN(seq int, text string) asr.Segment {
	return asr.Segment{
		ID:       "x" + strings.Repeat("i", seq%3+1),
		Sequence: seq,
		Start:    float64(seq) * 2,
		End:      float64(seq)*2 + 1.5,
		Text:     text,
	}
}

// -----------------------------------------------------------------------------
// 长循环折叠
// -----------------------------------------------------------------------------

func TestCollapseLongLoopsSingleChar(t *testing.T) {
	// 真实数据：日语视频里 "あ" 被吐了 429 次。
	got := CollapseLongLoops(strings.Repeat("あ", 429))
	if got != "ああ" {
		t.Fatalf("期望折叠成 %q，实际 %q（长度 %d）", "ああ", got, len([]rune(got)))
	}
}

func TestCollapseLongLoopsMultiCharUnit(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"2 字符单元", strings.Repeat("ふん", 150), "ふんふん"},
		{"3 字符单元", strings.Repeat("ですよ", 40), "ですよですよ"},
		{"4 字符单元", strings.Repeat("abcd", 30), "abcdabcd"},
		{"英文短词重复", strings.Repeat("give", 30), "givegive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CollapseLongLoops(c.input); got != c.want {
				t.Fatalf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

func TestCollapseLongLoopsKeepsShortExclamations(t *testing.T) {
	// 这些是真实语气重复，必须原样保留。
	cases := []string{
		"うんうんうん",
		"ねえねえねえ",
		"あっあっあっ",
		"哈哈哈哈哈",
		"そうそうそう",
		"はいはいはい",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if got := CollapseLongLoops(input); got != input {
				t.Fatalf("短语气重复不应被折叠：期望 %q，实际 %q", input, got)
			}
		})
	}
}

func TestCollapseLongLoopsKeepsNormalText(t *testing.T) {
	// 正常对白不应被改动（含长度不足 20 的重复段）。
	cases := []string{
		"今日はいい天気ですね",
		"ちょっと待ってください",
		"ああああ",
		"",
		"あ",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if got := CollapseLongLoops(input); got != input {
				t.Fatalf("正常文本不应被改动：期望 %q，实际 %q", input, got)
			}
		})
	}
}

func TestCollapseLongLoopsKeepsSurroundingText(t *testing.T) {
	loop := strings.Repeat("あ", 60)
	input := "そうですね" + loop + "わかりました"
	want := "そうですねああわかりました"
	if got := CollapseLongLoops(input); got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}
}

func TestCollapseLongLoopsDoesNotMutateInput(t *testing.T) {
	// 字符串不可变，这里验证返回的是新内容且入参语义不受影响。
	input := strings.Repeat("あ", 100)
	_ = CollapseLongLoops(input)
	if input != strings.Repeat("あ", 100) {
		t.Fatalf("CollapseLongLoops 不应改变入参")
	}
}

// -----------------------------------------------------------------------------
// 跨语种幻觉过滤
// -----------------------------------------------------------------------------

func TestCleanCrossLanguageDroppedAtMedium(t *testing.T) {
	input := []asr.Segment{
		segN(1, "今日はいい天気ですね"),
		segN(2, "I don't give much."),
		segN(3, "O que é que você está fazendo?"),
		segN(4, "Je ne sais pas du tout ce qui se passe"),
		segN(5, "また明日会いましょう"),
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	want := []string{"今日はいい天気ですね", "また明日会いましょう"}
	if len(got.Segments) != len(want) {
		t.Fatalf("期望保留 %d 条，实际 %d 条：%v", len(want), len(got.Segments), texts(got.Segments))
	}
	for i, w := range want {
		if got.Segments[i].Text != w {
			t.Errorf("第 %d 条期望 %q，实际 %q", i, w, got.Segments[i].Text)
		}
	}
	if got.Dropped != 3 {
		t.Fatalf("期望丢弃 3 条跨语种幻觉，实际 %d", got.Dropped)
	}
}

func TestCleanCrossLanguageWorksForChineseAndKorean(t *testing.T) {
	cases := []struct {
		lang string
		keep string
		drop string
	}{
		{"zh", "今天天气真不错", "Thanks for watching this video"},
		{"zh-CN", "我们一起走吧", "Bonjour tout le monde"},
		{"ko", "오늘 날씨가 좋네요", "I really do not know what happened"},
		{"ja-JP", "そうですね", "This is completely wrong output"},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			got := Clean([]asr.Segment{segN(1, c.keep), segN(2, c.drop)},
				Options{Level: LevelMedium, ExpectedLanguage: c.lang})
			if len(got.Segments) != 1 || got.Segments[0].Text != c.keep {
				t.Fatalf("期望只保留 %q，实际 %v", c.keep, texts(got.Segments))
			}
		})
	}
}

func TestCleanCrossLanguageKeepsJapaneseDialogue(t *testing.T) {
	// 日语正常对白（含汉字、假名、长音符、数字、标点）不应被误伤。
	input := []asr.Segment{
		segN(1, "ちょっと待ってください"),
		segN(2, "「明日は晴れだよ」と彼は言った。"),
		segN(3, "コーヒーを2つお願いします"),
		segN(4, "えーっと、そうですね……"),
		segN(5, "ふん"),
		segN(6, "あっ"),
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	if len(got.Segments) != len(input) {
		t.Fatalf("日语正常对白不应被丢弃，期望 %d 条，实际 %d 条：%v",
			len(input), len(got.Segments), texts(got.Segments))
	}
	if got.Dropped != 0 {
		t.Fatalf("不应丢弃任何条目，实际丢弃 %d", got.Dropped)
	}
}

func TestCleanCrossLanguageKeepsMixedTextWithTerms(t *testing.T) {
	// 日语里夹英文术语：只要出现一个日文字符就不算幻觉。
	input := []asr.Segment{
		segN(1, "このAPIのドキュメントは英語で書かれています"),
		segN(2, "React と Vue の違いについて説明します"),
		segN(3, "OK"),
		segN(4, "ありがとうございます"),
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	if len(got.Segments) != len(input) {
		t.Fatalf("夹英文术语的日语条目不应被丢弃，实际 %d 条：%v",
			len(got.Segments), texts(got.Segments))
	}
}

func TestCleanCrossLanguageIgnoredAtLow(t *testing.T) {
	// low 等级不启用跨语种规则。
	input := []asr.Segment{
		segN(1, "今日はいい天気ですね"),
		segN(2, "I don't give much."),
	}
	got := Clean(input, Options{Level: LevelLow, ExpectedLanguage: "ja"})
	if len(got.Segments) != 2 {
		t.Fatalf("low 等级不应启用跨语种过滤，实际保留 %d 条：%v",
			len(got.Segments), texts(got.Segments))
	}
}

func TestCleanCrossLanguageDisabledWhenEmpty(t *testing.T) {
	// ExpectedLanguage 为空：规则完全不生效，与旧行为一致。
	input := []asr.Segment{
		segN(1, "今日はいい天気ですね"),
		segN(2, "I don't give much."),
	}
	for _, level := range []Level{LevelLow, LevelMedium, LevelHigh} {
		got := Clean(input, Options{Level: level})
		if len(got.Segments) != 2 {
			t.Fatalf("ExpectedLanguage 为空时 %s 等级不应过滤跨语种条目，实际 %d 条：%v",
				level, len(got.Segments), texts(got.Segments))
		}
	}
}

func TestCleanCrossLanguageDisabledForLatinLanguages(t *testing.T) {
	// 拉丁语系没有判别力，规则应自动不启用。
	for _, lang := range []string{"en", "fr", "pt", "es", "de", "unknown"} {
		t.Run(lang, func(t *testing.T) {
			input := []asr.Segment{segN(1, "This is a normal sentence")}
			got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: lang})
			if len(got.Segments) != 1 {
				t.Fatalf("拉丁语系语言不应启用跨语种过滤，实际丢弃到 %d 条", len(got.Segments))
			}
		})
	}
}

func TestCleanCrossLanguageShortLatinKept(t *testing.T) {
	// 拉丁字母不足 8 个（OK / No / Hi 等正常外来语）不判为幻觉。
	input := []asr.Segment{
		segN(1, "OK"),
		segN(2, "No"),
		segN(3, "はい"),
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	if len(got.Segments) != 3 {
		t.Fatalf("短拉丁词不应被判为幻觉，实际保留 %d 条：%v",
			len(got.Segments), texts(got.Segments))
	}
}

// -----------------------------------------------------------------------------
// 组合场景
// -----------------------------------------------------------------------------

func TestCleanCombinesCrossLanguageAndLoop(t *testing.T) {
	// 一条跨语种幻觉 + 一条长循环 + 两条正常对白。
	input := []asr.Segment{
		segN(1, "今日はいい天気ですね"),
		segN(2, "I don't give much."),
		segN(3, strings.Repeat("ふん", 150)),
		segN(4, "また明日会いましょう"),
	}
	got := Clean(input, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	want := []string{"今日はいい天気ですね", "ふんふん", "また明日会いましょう"}
	if len(got.Segments) != len(want) {
		t.Fatalf("期望 %d 条，实际 %d 条：%v", len(want), len(got.Segments), texts(got.Segments))
	}
	for i, w := range want {
		if got.Segments[i].Text != w {
			t.Errorf("第 %d 条期望 %q，实际 %q", i, w, got.Segments[i].Text)
		}
	}
	if got.Dropped != 1 {
		t.Fatalf("期望丢弃 1 条，实际 %d", got.Dropped)
	}
	// 折叠只改文本，时间戳必须原样保留。
	if got.Segments[1].Sequence != 3 || got.Segments[1].Start != 6 || got.Segments[1].End != 7.5 {
		t.Errorf("折叠后时间戳被改动：%+v", got.Segments[1])
	}
}

func TestCleanLoopCollapseKeepsSegmentCount(t *testing.T) {
	// 折叠不丢弃条目：输入几条就输出几条。
	input := []asr.Segment{
		segN(1, strings.Repeat("あ", 429)),
		segN(2, strings.Repeat("ねえ", 200)),
		segN(3, "普通の会話です"),
	}
	got := Clean(input, Options{Level: LevelMedium})
	if len(got.Segments) != 3 || got.Dropped != 0 {
		t.Fatalf("折叠不应丢弃条目，实际保留 %d 条、丢弃 %d 条", len(got.Segments), got.Dropped)
	}
	for _, s := range got.Segments {
		if len([]rune(s.Text)) > 30 {
			t.Errorf("折叠后仍然过长：%d 个字符", len([]rune(s.Text)))
		}
	}
}

func TestCleanLoopCollapseAtLowLevel(t *testing.T) {
	// low 等级也折叠长循环（C 规则从 low 起生效）。
	input := []asr.Segment{segN(1, strings.Repeat("あ", 429))}
	got := Clean(input, Options{Level: LevelLow})
	if len(got.Segments) != 1 {
		t.Fatalf("low 等级应保留该条，实际 %d 条", len(got.Segments))
	}
	if got.Segments[0].Text != "ああ" {
		t.Fatalf("low 等级应折叠长循环，实际 %q", got.Segments[0].Text)
	}
}

func TestCleanLevelOrderingWithNewRules(t *testing.T) {
	// 新规则接入后，等级仍应保持 low ⊃ medium ⊃ high。
	input := []asr.Segment{
		segN(1, "正常对话内容"),
		segN(2, "I don't give much."),
		segN(3, strings.Repeat("あ", 100)),
		segN(4, "感谢观看"),
		segN(5, "嗯"),
		segN(6, "😀"),
	}
	opts := func(level Level) Options {
		return Options{Level: level, ExpectedLanguage: "ja"}
	}
	low := Clean(input, opts(LevelLow))
	medium := Clean(input, opts(LevelMedium))
	high := Clean(input, opts(LevelHigh))
	if !(len(low.Segments) >= len(medium.Segments) && len(medium.Segments) >= len(high.Segments)) {
		t.Fatalf("等级应单调递增地变严格：low=%d medium=%d high=%d",
			len(low.Segments), len(medium.Segments), len(high.Segments))
	}
}

// -----------------------------------------------------------------------------
// 辅助函数
// -----------------------------------------------------------------------------

func TestNormalizeLanguageTag(t *testing.T) {
	cases := map[string]string{
		"ja":      "ja",
		"JA":      "ja",
		" ja-JP ": "ja",
		"zh-CN":   "zh",
		"zh_Hans": "zh",
		"":        "",
	}
	for input, want := range cases {
		if got := normalizeLanguageTag(input); got != want {
			t.Errorf("normalizeLanguageTag(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestLanguageRuneRanges(t *testing.T) {
	// 有专属文字的语言返回区间，拉丁语系返回 nil。
	for _, lang := range []string{"ja", "zh", "ko", "ru", "ar", "he", "th", "el"} {
		if len(languageRuneRanges(lang)) == 0 {
			t.Errorf("%q 应返回字符区间", lang)
		}
	}
	for _, lang := range []string{"", "en", "fr", "pt", "es", "de", "unknown"} {
		if ranges := languageRuneRanges(lang); ranges != nil {
			t.Errorf("%q 不应返回字符区间，实际 %v", lang, ranges)
		}
	}
}

func TestInRuneRanges(t *testing.T) {
	ranges := languageRuneRanges("ja")
	hit := []rune{'あ', 'ア', '漢', 'ー'}
	for _, r := range hit {
		if !inRuneRanges(r, ranges) {
			t.Errorf("%q 应命中日语区间", r)
		}
	}
	miss := []rune{'a', 'Z', 'é', '1'}
	for _, r := range miss {
		if inRuneRanges(r, ranges) {
			t.Errorf("%q 不应命中日语区间", r)
		}
	}
}

func TestIsCrossLanguageHallucination(t *testing.T) {
	ja := languageRuneRanges("ja")
	cases := []struct {
		text string
		want bool
	}{
		{"I don't give much.", true},
		{"O que é que você está fazendo?", true},
		{"今日はいい天気ですね", false},
		{"React の話", false},
		{"OK", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isCrossLanguageHallucination(c.text, ja); got != c.want {
			t.Errorf("isCrossLanguageHallucination(%q) = %v，期望 %v", c.text, got, c.want)
		}
	}
	// 区间为空时永远不判为幻觉。
	if isCrossLanguageHallucination("I don't give much.", nil) {
		t.Error("区间为空时不应判为幻觉")
	}
}
