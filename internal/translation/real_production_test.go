package translation

import (
	"bufio"
	"context"
	"encoding/csv"
	"os"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/subtitleclean"
)

// loadRealSegments 读取真实 ASR 结果（CSV 无表头，格式 id,text）。
func loadRealSegments(t *testing.T) []Item {
	t.Helper()
	f, err := os.Open("../../.gotmp/mida825_segments.csv")
	if err != nil {
		return nil
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 1<<16))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("读取 CSV 失败：%v", err)
	}
	out := make([]Item, 0, len(records))
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		text := strings.TrimSpace(strings.Join(rec[1:], ","))
		if text == "" {
			continue
		}
		out = append(out, Item{ID: strings.TrimSpace(rec[0]), Text: text})
	}
	return out
}

func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// TestRealOllamaProductionPath 按真实生产链路验证：先清洗（丢弃幻觉/折叠循环），
// 再翻译清洗后的数据。这才是用户实际跑任务时走的路径。
//
// 关键区别：未经清洗的原始 ASR 输出里有纯英文/葡语重复幻觉（最长 1755 字符），
// 这类长文本会耗尽输出预算导致 JSON 截断。生产链路在翻译前就已丢弃它们，
// 因此翻译阶段面对的是干净数据。
//
// 需要本机 Ollama 与 translategemma:12b，否则跳过。
func TestRealOllamaProductionPath(t *testing.T) {
	if os.Getenv("TRANSCRIBE_TEST_REAL_OLLAMA") == "" {
		t.Skip("设置 TRANSCRIBE_TEST_REAL_OLLAMA=1 才跑真实 Ollama 测试")
	}

	raw := loadRealSegments(t)
	if len(raw) == 0 {
		t.Skip("缺少真实字幕数据")
	}

	// 第一步：按生产参数清洗（等级 medium，源语言日语）。
	segs := make([]asr.Segment, 0, len(raw))
	for _, it := range raw {
		segs = append(segs, asr.Segment{ID: it.ID, Text: it.Text})
	}
	cleaned := subtitleclean.Clean(segs, subtitleclean.Options{
		Level:            subtitleclean.LevelMedium,
		ExpectedLanguage: "ja",
	})
	t.Logf("清洗：%d 条 -> %d 条（丢弃 %d）", len(raw), len(cleaned.Segments), cleaned.Dropped)

	items := make([]Item, 0, len(cleaned.Segments))
	for _, s := range cleaned.Segments {
		items = append(items, Item{ID: s.ID, Text: s.Text})
	}

	provider := NewOllamaProvider(OllamaConfig{
		BaseURL: "http://127.0.0.1:11434",
		Model:   "translategemma:12b",
	})
	tr := NewTranslator(provider, Options{
		BatchSize:      20,
		MaxRetries:     2,
		Concurrency:    1,
		SourceLanguage: "ja",
		TargetLanguage: "zh-CN",
		Style:          "subtitle",
		ContentMode:    "subtitle",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()
	got, translateErr := tr.Translate(ctx, items, func(done, total int) {
		if done == total || done%60 == 0 {
			t.Logf("进度 %d/%d", done, total)
		}
	})
	elapsed := time.Since(start)
	if translateErr != nil {
		t.Logf("聚合错误：%v", translateErr)
	}

	translated := make(map[string]string, len(got))
	for _, c := range got {
		translated[c.ID] = c.Text
	}

	okCount := 0
	var failed []string
	for _, it := range items {
		if strings.TrimSpace(translated[it.ID]) != "" {
			okCount++
		} else {
			failed = append(failed, it.ID)
		}
	}
	rate := 100.0 * float64(okCount) / float64(len(items))
	t.Logf("耗时 %s，成功 %d/%d（%.1f%%），失败：%v",
		elapsed.Round(time.Second), okCount, len(items), rate, failed)

	if rate < 95 {
		t.Errorf("生产路径成功率仅 %.1f%%，期望 >= 95%%", rate)
	}

	shown := 0
	for _, it := range items {
		if v := translated[it.ID]; v != "" && shown < 10 {
			t.Logf("[%s] %s => %s", it.ID, truncRunes(it.Text, 28), truncRunes(v, 42))
			shown++
		}
	}
}
