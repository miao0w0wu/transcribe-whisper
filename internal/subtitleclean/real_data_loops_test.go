// 临时验证：真实 241 条 ASR 结果经过清洗层后，循环是否被折叠。
package subtitleclean

import (
	"bufio"
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"transcribe-whisper/internal/asr"
)

func TestRealDataLoopsCollapsed(t *testing.T) {
	f, err := os.Open("../../.gotmp/mida825_segments.csv")
	if err != nil {
		t.Skipf("缺少真实数据：%v", err)
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 1<<16))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("读取 CSV 失败：%v", err)
	}

	segs := make([]asr.Segment, 0, len(records))
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		segs = append(segs, asr.Segment{ID: rec[0], Text: strings.Join(rec[1:], ",")})
	}
	if len(segs) == 0 {
		t.Fatal("没有读到任何字幕")
	}

	res := Clean(segs, Options{Level: LevelMedium, ExpectedLanguage: "ja"})
	t.Logf("原始 %d 条 -> 清洗后 %d 条（丢弃 %d）", len(segs), len(res.Segments), res.Dropped)

	for _, s := range res.Segments {
		if strings.Count(s.Text, "あ") > 20 {
			t.Errorf("id=%s 仍含长循环（あ 出现 %d 次）：%q",
				s.ID, strings.Count(s.Text, "あ"), truncate(s.Text, 60))
		}
		if len([]rune(s.Text)) > 200 {
			t.Errorf("id=%s 清洗后仍过长（%d 字符）", s.ID, len([]rune(s.Text)))
		}
	}
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
