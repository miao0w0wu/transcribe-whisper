package subtitleclean

import (
	"testing"

	"transcribe-whisper/internal/asr"
)

// seg 是构造测试字幕的简写。
func seg(seq int, text string) asr.Segment {
	return asr.Segment{
		ID:       "s" + string(rune('0'+seq%10)),
		Sequence: seq,
		Start:    float64(seq),
		End:      float64(seq) + 1,
		Text:     text,
	}
}

func texts(segments []asr.Segment) []string {
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		out = append(out, s.Text)
	}
	return out
}

func TestCleanLevelOffKeepsEverything(t *testing.T) {
	input := []asr.Segment{seg(1, "你好"), seg(2, "（音乐）"), seg(3, "😀")}
	got := Clean(input, Options{Level: LevelOff})
	if got.Dropped != 0 {
		t.Fatalf("off 等级不应丢弃任何条目，实际丢弃 %d", got.Dropped)
	}
	if len(got.Segments) != len(input) {
		t.Fatalf("off 等级应原样返回 %d 条，实际 %d 条", len(input), len(got.Segments))
	}
	for i := range input {
		if got.Segments[i].Text != input[i].Text {
			t.Errorf("第 %d 条文本被改动：%q -> %q", i, input[i].Text, got.Segments[i].Text)
		}
	}
}

func TestCleanLowRemovesEmojiAndPunctuationOnly(t *testing.T) {
	input := []asr.Segment{
		seg(1, "今天天气不错"),
		seg(2, "😀🎉"),
		seg(3, "…"),
		seg(4, "好😀的"),
	}
	got := Clean(input, Options{Level: LevelLow})
	if len(got.Segments) != 2 {
		t.Fatalf("期望保留 2 条，实际 %d 条：%v", len(got.Segments), texts(got.Segments))
	}
	if got.Dropped != 2 {
		t.Fatalf("期望丢弃 2 条，实际 %d", got.Dropped)
	}
	if got.Segments[1].Text != "好的" {
		t.Errorf("表情应从文本中剔除，实际得到 %q", got.Segments[1].Text)
	}
}

func TestCleanMediumStripsSoundEffects(t *testing.T) {
	input := []asr.Segment{
		seg(1, "（音乐）他走了"),
		seg(2, "[掌声]谢谢大家"),
		seg(3, "【BGM】接下来"),
		seg(4, "括号里的(说明)保留正文"),
	}
	got := Clean(input, Options{Level: LevelMedium})
	want := []string{"他走了", "谢谢大家", "接下来", "括号里的保留正文"}
	if len(got.Segments) != len(want) {
		t.Fatalf("期望 %d 条，实际 %d 条：%v", len(want), len(got.Segments), texts(got.Segments))
	}
	for i, w := range want {
		if got.Segments[i].Text != w {
			t.Errorf("第 %d 条期望 %q，实际 %q", i, w, got.Segments[i].Text)
		}
	}
}

func TestCleanMediumDropsConsecutiveRepeats(t *testing.T) {
	input := []asr.Segment{
		seg(1, "我爱你"),
		seg(2, "我爱你"),
		seg(3, "我爱你"),
		seg(4, "别的话"),
		seg(5, "我爱你"),
	}
	got := Clean(input, Options{Level: LevelMedium})
	if len(got.Segments) != 3 {
		t.Fatalf("期望保留 3 条（首条 + 别的话 + 末尾一条），实际 %d 条：%v",
			len(got.Segments), texts(got.Segments))
	}
	if got.Dropped != 2 {
		t.Fatalf("期望丢弃 2 条紧邻重复，实际 %d", got.Dropped)
	}
}

func TestCleanMediumKeepsNonConsecutiveRepeats(t *testing.T) {
	input := []asr.Segment{seg(1, "嗯"), seg(2, "好的"), seg(3, "嗯")}
	got := Clean(input, Options{Level: LevelMedium})
	if len(got.Segments) != 3 {
		t.Fatalf("非紧邻重复不应被丢弃，实际保留 %d 条：%v", len(got.Segments), texts(got.Segments))
	}
}

func TestCleanHighDropsHallucinationPhrases(t *testing.T) {
	input := []asr.Segment{
		seg(1, "真正的对话"),
		seg(2, "感谢观看"),
		seg(3, "请订阅我的频道！"),
		seg(4, "Thanks for watching"),
		seg(5, "ご視聴ありがとうございました"),
	}
	got := Clean(input, Options{Level: LevelHigh})
	if len(got.Segments) != 1 {
		t.Fatalf("期望只保留 1 条，实际 %d 条：%v", len(got.Segments), texts(got.Segments))
	}
	if got.Segments[0].Text != "真正的对话" {
		t.Errorf("保留的应是正常对话，实际 %q", got.Segments[0].Text)
	}
}

func TestCleanHighDropsTooShort(t *testing.T) {
	input := []asr.Segment{seg(1, "嗯"), seg(2, "好的走吧")}
	got := Clean(input, Options{Level: LevelHigh})
	if len(got.Segments) != 1 {
		t.Fatalf("期望丢弃单字条目，实际保留 %d 条：%v", len(got.Segments), texts(got.Segments))
	}
	if got.Segments[0].Text != "好的走吧" {
		t.Errorf("期望保留较长条目，实际 %q", got.Segments[0].Text)
	}
}

func TestCleanPreservesTimestampsAndOrder(t *testing.T) {
	input := []asr.Segment{
		{ID: "a", Sequence: 3, Start: 10.5, End: 11.25, Text: "第二句"},
		{ID: "b", Sequence: 7, Start: 20, End: 21, Text: "第三句"},
	}
	got := Clean(input, Options{Level: LevelMedium})
	if len(got.Segments) != 2 {
		t.Fatalf("期望保留 2 条，实际 %d", len(got.Segments))
	}
	for i, s := range got.Segments {
		if s.ID != input[i].ID || s.Sequence != input[i].Sequence ||
			s.Start != input[i].Start || s.End != input[i].End {
			t.Errorf("第 %d 条的标识或时间戳被改动：%+v", i, s)
		}
	}
}

func TestCleanDoesNotMutateInput(t *testing.T) {
	input := []asr.Segment{seg(1, "（音乐）正文😀")}
	original := input[0].Text
	_ = Clean(input, Options{Level: LevelHigh})
	if input[0].Text != original {
		t.Fatalf("Clean 不应修改入参，原值 %q 变成了 %q", original, input[0].Text)
	}
}

func TestCleanUnknownLevelIsOff(t *testing.T) {
	input := []asr.Segment{seg(1, "😀"), seg(2, "感谢观看")}
	got := Clean(input, Options{Level: Level("乱写的")})
	if len(got.Segments) != 2 || got.Dropped != 0 {
		t.Fatalf("未知等级应按 off 处理，实际保留 %d 条、丢弃 %d 条", len(got.Segments), got.Dropped)
	}
}

func TestCleanEmptyInput(t *testing.T) {
	got := Clean(nil, Options{Level: LevelMedium})
	if len(got.Segments) != 0 || got.Dropped != 0 {
		t.Fatalf("空输入应返回空结果，实际 %+v", got)
	}
}

func TestStripSoundEffectsUnclosedBracket(t *testing.T) {
	got := Clean([]asr.Segment{seg(1, "开头（未闭合")}, Options{Level: LevelMedium})
	// 未闭合括号把剩余内容丢弃，因此该条只剩「开头」。
	if len(got.Segments) != 1 {
		t.Fatalf("期望保留 1 条，实际 %d", len(got.Segments))
	}
	if got.Segments[0].Text != "开头" {
		t.Errorf("期望 %q，实际 %q", "开头", got.Segments[0].Text)
	}
}

func TestCleanLevelOrdering(t *testing.T) {
	// 同一份输入，等级越高保留越少（low ⊂ medium ⊂ high）。
	input := []asr.Segment{
		seg(1, "正常对话内容"),
		seg(2, "😀"),
		seg(3, "（音乐）"),
		seg(4, "正常对话内容"),
		seg(5, "感谢观看"),
		seg(6, "嗯"),
	}
	low := Clean(input, Options{Level: LevelLow})
	medium := Clean(input, Options{Level: LevelMedium})
	high := Clean(input, Options{Level: LevelHigh})

	if !(len(low.Segments) >= len(medium.Segments) && len(medium.Segments) >= len(high.Segments)) {
		t.Fatalf("等级应单调递增地变严格：low=%d medium=%d high=%d",
			len(low.Segments), len(medium.Segments), len(high.Segments))
	}
}
