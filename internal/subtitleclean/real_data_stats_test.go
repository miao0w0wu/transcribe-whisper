package subtitleclean

import (
	"bufio"
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"transcribe-whisper/internal/asr"
)

// TestRealDataAfterCollapseStats 打印清洗后的长度分布，便于人工确认循环被充分折叠。
func TestRealDataAfterCollapseStats(t *testing.T) {
	f, err := os.Open("../../.gotmp/mida825_segments.csv")
	if err != nil {
		t.Skipf("缺少真实数据：%v", err)
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 1<<16))
	r.FieldsPerRecord = -1
	records, _ := r.ReadAll()

	segs := make([]asr.Segment, 0, len(records))
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		txt := strings.Join(rec[1:], ",")
		if strings.TrimSpace(txt) == "" {
			continue
		}
		segs = append(segs, asr.Segment{ID: rec[0], Text: txt})
	}

	res := Clean(segs, Options{Level: LevelMedium, ExpectedLanguage: "ja"})

	maxLen := 0
	var longest string
	var longestID string
	over120 := 0
	for _, s := range res.Segments {
		n := len([]rune(s.Text))
		if n > maxLen {
			maxLen, longest, longestID = n, s.Text, s.ID
		}
		if n > 120 {
			over120++
		}
	}
	t.Logf("清洗后 %d 条，最长 %d 字符（id=%s），超 120 字符的有 %d 条",
		len(res.Segments), maxLen, longestID, over120)
	t.Logf("最长条目内容：%q", longest)
}
