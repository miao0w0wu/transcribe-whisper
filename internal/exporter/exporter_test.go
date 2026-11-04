package exporter

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleSegments() []Segment {
	return []Segment{
		{ID: "s1", Sequence: 1, Start: 0, End: 2.5, Source: "Hello", Translated: "你好"},
		{ID: "s2", Sequence: 2, Start: 3.25, End: 5.125, Source: "World", Translated: "世界"},
	}
}

func TestSupportedFormats(t *testing.T) {
	got := SupportedFormats()
	want := []string{"srt", "vtt", "txt", "json", "bilingual-srt"}
	if len(got) != len(want) {
		t.Fatalf("格式数量不符: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个格式不符: got=%q want=%q", i, got[i], want[i])
		}
	}
	// 返回的是副本，修改不应影响内部数据。
	got[0] = "changed"
	if SupportedFormats()[0] != "srt" {
		t.Fatalf("SupportedFormats 返回了内部切片，调用方修改污染了包状态")
	}
}

func TestExtension(t *testing.T) {
	cases := map[string]string{
		"srt":           "srt",
		"VTT":           "vtt",
		"txt":           "txt",
		"json":          "json",
		"bilingual-srt": "srt",
		" Bilingual-SRT ": "srt",
	}
	for format, want := range cases {
		got, err := Extension(format)
		if err != nil {
			t.Fatalf("Extension(%q) 返回错误: %v", format, err)
		}
		if got != want {
			t.Fatalf("Extension(%q)=%q, want %q", format, got, want)
		}
	}
}

func TestExtensionUnknownFormat(t *testing.T) {
	_, err := Extension("ass")
	if err == nil {
		t.Fatal("未知格式应当返回错误")
	}
	if !strings.Contains(err.Error(), "不支持的字幕导出格式") || !strings.Contains(err.Error(), "bilingual-srt") {
		t.Fatalf("错误信息应为中文并列出支持格式，实际: %v", err)
	}
}

func TestExportSRT(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "srt"})
	if err != nil {
		t.Fatalf("导出 srt 失败: %v", err)
	}
	want := "1\n00:00:00,000 --> 00:00:02,500\n你好\n\n" +
		"2\n00:00:03,250 --> 00:00:05,125\n世界\n\n"
	if got != want {
		t.Fatalf("srt 输出不符\n got=%q\nwant=%q", got, want)
	}
}

func TestExportVTT(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "vtt"})
	if err != nil {
		t.Fatalf("导出 vtt 失败: %v", err)
	}
	if !strings.HasPrefix(got, "WEBVTT\n\n") {
		t.Fatalf("vtt 缺少文件头: %q", got)
	}
	if !strings.Contains(got, "00:00:00.000 --> 00:00:02.500") {
		t.Fatalf("vtt 时间格式应为点分隔: %q", got)
	}
	if !strings.Contains(got, "00:00:03.250 --> 00:00:05.125") {
		t.Fatalf("vtt 第二条时间不符: %q", got)
	}
}

func TestExportVTTEmpty(t *testing.T) {
	got, err := Export(nil, Options{Format: "vtt"})
	if err != nil {
		t.Fatalf("空字幕导出 vtt 失败: %v", err)
	}
	if got != "WEBVTT\n\n" {
		t.Fatalf("空字幕 vtt 应仅含文件头，实际 %q", got)
	}
}

func TestExportSRTEmpty(t *testing.T) {
	for _, format := range []string{"srt", "txt", "bilingual-srt"} {
		got, err := Export(nil, Options{Format: format})
		if err != nil {
			t.Fatalf("空字幕导出 %s 失败: %v", format, err)
		}
		if got != "" {
			t.Fatalf("空字幕导出 %s 应为空字符串，实际 %q", format, got)
		}
	}
}

func TestExportTXT(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "txt"})
	if err != nil {
		t.Fatalf("导出 txt 失败: %v", err)
	}
	if want := "你好\n世界\n"; got != want {
		t.Fatalf("txt 输出不符: got=%q want=%q", got, want)
	}
}

func TestExportTXTIncludeSource(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "txt", IncludeSource: true})
	if err != nil {
		t.Fatalf("导出 txt 失败: %v", err)
	}
	if want := "Hello\t你好\nWorld\t世界\n"; got != want {
		t.Fatalf("txt 带原文输出不符: got=%q want=%q", got, want)
	}
}

func TestExportJSON(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "json"})
	if err != nil {
		t.Fatalf("导出 json 失败: %v", err)
	}
	if !strings.Contains(got, "\n  {") {
		t.Fatalf("json 应带缩进，实际:\n%s", got)
	}
	var items []struct {
		ID         string  `json:"id"`
		Sequence   int     `json:"sequence"`
		Start      float64 `json:"start"`
		End        float64 `json:"end"`
		Source     string  `json:"source"`
		Translated string  `json:"translated"`
	}
	if err := json.Unmarshal([]byte(got), &items); err != nil {
		t.Fatalf("json 反序列化失败: %v\n内容:\n%s", err, got)
	}
	if len(items) != 2 {
		t.Fatalf("json 条目数不符: %d", len(items))
	}
	if items[1].ID != "s2" || items[1].Sequence != 2 || items[1].Start != 3.25 || items[1].End != 5.125 {
		t.Fatalf("json 第二条字段不符: %+v", items[1])
	}
	if items[0].Source != "Hello" || items[0].Translated != "你好" {
		t.Fatalf("json 文本字段不符: %+v", items[0])
	}
}

func TestExportJSONEmpty(t *testing.T) {
	got, err := Export(nil, Options{Format: "json"})
	if err != nil {
		t.Fatalf("空字幕导出 json 失败: %v", err)
	}
	if strings.TrimSpace(got) != "[]" {
		t.Fatalf("空字幕 json 应为空数组，实际 %q", got)
	}
}

func TestExportBilingualSRT(t *testing.T) {
	got, err := Export(sampleSegments(), Options{Format: "bilingual-srt"})
	if err != nil {
		t.Fatalf("导出 bilingual-srt 失败: %v", err)
	}
	want := "1\n00:00:00,000 --> 00:00:02,500\nHello\n你好\n\n" +
		"2\n00:00:03,250 --> 00:00:05,125\nWorld\n世界\n\n"
	if got != want {
		t.Fatalf("bilingual-srt 输出不符\n got=%q\nwant=%q", got, want)
	}
	// 原文行必须在上、译文行在下。
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if lines[2] != "Hello" || lines[3] != "你好" {
		t.Fatalf("bilingual 原文/译文顺序不符: %v", lines[:4])
	}
}

func TestExportBilingualSRTEmptyTranslation(t *testing.T) {
	segs := []Segment{
		{ID: "s1", Sequence: 1, Start: 0, End: 1, Source: "Only source", Translated: ""},
	}
	got, err := Export(segs, Options{Format: "bilingual-srt"})
	if err != nil {
		t.Fatalf("导出 bilingual-srt 失败: %v", err)
	}
	want := "1\n00:00:00,000 --> 00:00:01,000\nOnly source\n\n"
	if got != want {
		t.Fatalf("译文为空时 bilingual 只应输出原文，got=%q want=%q", got, want)
	}
}

func TestExportBilingualSRTIncludeSourceFlag(t *testing.T) {
	// IncludeSource 不应影响 bilingual 的两行结构（它天然输出原文）。
	segs := []Segment{{ID: "s1", Sequence: 1, Start: 0, End: 1, Source: "src", Translated: "译"}}
	on, _ := Export(segs, Options{Format: "bilingual-srt", IncludeSource: true})
	off, _ := Export(segs, Options{Format: "bilingual-srt"})
	if on != off {
		t.Fatalf("IncludeSource 不应改变 bilingual 输出: on=%q off=%q", on, off)
	}
}

func TestFormatTimestampCarry(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{1.9999, "00:00:02,000"},
		{59.9999, "00:01:00,000"},
		{3599.9999, "01:00:00,000"},
		{0, "00:00:00,000"},
		{1.5, "00:00:01,500"},
		{3725.6789, "01:02:05,679"},
	}
	for _, c := range cases {
		if got := formatSRTTime(c.seconds); got != c.want {
			t.Fatalf("formatSRTTime(%v)=%q, want %q", c.seconds, got, c.want)
		}
	}
	if got := formatVTTTime(1.9999); got != "00:00:02.000" {
		t.Fatalf("formatVTTTime 进位错误: %q", got)
	}
}

func TestExportTimeNormalization(t *testing.T) {
	segs := []Segment{
		{ID: "s1", Sequence: 1, Start: -3.5, End: -1, Source: "a", Translated: "甲"},
		{ID: "s2", Sequence: 2, Start: 5, End: 2, Source: "b", Translated: "乙"},
	}
	got, err := Export(segs, Options{Format: "srt"})
	if err != nil {
		t.Fatalf("时间非法时导出不应报错: %v", err)
	}
	if !strings.Contains(got, "00:00:00,000 --> 00:00:00,000") {
		t.Fatalf("负数时间应归一化为 0: %q", got)
	}
	if !strings.Contains(got, "00:00:05,000 --> 00:00:05,000") {
		t.Fatalf("End<Start 时应令 End=Start: %q", got)
	}
}

func TestExportUseSourceFallback(t *testing.T) {
	segs := []Segment{{ID: "s1", Sequence: 1, Start: 0, End: 1, Source: "fallback", Translated: ""}}

	got, err := Export(segs, Options{Format: "srt", UseSource: true})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !strings.Contains(got, "fallback") {
		t.Fatalf("UseSource=true 时应用原文回退: %q", got)
	}

	got, err = Export(segs, Options{Format: "srt", UseSource: false})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if strings.Contains(got, "fallback") {
		t.Fatalf("UseSource=false 时不应回退原文: %q", got)
	}

	// 非空译文始终优先。
	segs[0].Translated = "正常译文"
	got, _ = Export(segs, Options{Format: "srt", UseSource: true})
	if !strings.Contains(got, "正常译文") || strings.Contains(got, "fallback") {
		t.Fatalf("译文非空时不应回退原文: %q", got)
	}
}

func TestExportUseSourceFallbackTXT(t *testing.T) {
	segs := []Segment{{ID: "s1", Sequence: 1, Source: "orig", Translated: ""}}
	got, err := Export(segs, Options{Format: "txt", UseSource: true})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if got != "orig\n" {
		t.Fatalf("txt 回退原文不符: %q", got)
	}
}

func TestExportUnknownFormat(t *testing.T) {
	_, err := Export(sampleSegments(), Options{Format: "ass"})
	if err == nil {
		t.Fatal("未知格式应报错")
	}
	msg := err.Error()
	for _, wantPart := range []string{"不支持的字幕导出格式", "srt", "vtt", "txt", "json", "bilingual-srt"} {
		if !strings.Contains(msg, wantPart) {
			t.Fatalf("错误信息缺少 %q: %s", wantPart, msg)
		}
	}
}

func TestExportEmptyFormat(t *testing.T) {
	if _, err := Export(sampleSegments(), Options{}); err == nil {
		t.Fatal("格式为空时应报错")
	}
}

func TestExportFormatCaseInsensitive(t *testing.T) {
	segs := sampleSegments()
	lower, _ := Export(segs, Options{Format: "srt"})
	mixed, err := Export(segs, Options{Format: "  SRT "})
	if err != nil {
		t.Fatalf("大写格式应被接受: %v", err)
	}
	if lower != mixed {
		t.Fatalf("格式名大小写/空白不应影响输出")
	}
}
