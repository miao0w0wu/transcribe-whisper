package subtitleclean

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"testing"

	"transcribe-whisper/internal/asr"
)

// TestCleanRealMIDA825 用 MIDA-825 的真实识别结果（241 条）回归清洗器。
//
// 数据来源：transcribe-whisper.db 中 cohere-transcribe 那次任务的字幕。
// 该视频是日语，原本有 20 条幻觉（14 条跨语种 + 6 条长循环），
// 这里断言清洗后这些幻觉全部消失，且真实对白不被误伤。
func TestCleanRealMIDA825(t *testing.T) {
	csvPath := os.Getenv("MIDA825_SEGMENTS_CSV")
	if csvPath == "" {
		t.Skip("未设置 MIDA825_SEGMENTS_CSV，跳过真实数据回归")
	}
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("打开数据文件失败：%v", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("解析 CSV 失败：%v", err)
	}

	segments := make([]asr.Segment, 0, len(rows))
	for i, row := range rows {
		if len(row) < 2 {
			continue
		}
		seq, _ := strconv.Atoi(row[0])
		segments = append(segments, asr.Segment{
			ID:       fmt.Sprintf("s%d", seq),
			Sequence: seq,
			Start:    float64(i) * 30,
			End:      float64(i)*30 + 30,
			Text:     row[1],
		})
	}
	if len(segments) != 241 {
		t.Fatalf("期望 241 条真实数据，实际 %d 条", len(segments))
	}

	got := Clean(segments, Options{Level: LevelMedium, ExpectedLanguage: "ja"})

	// 被丢弃的应该是幻觉条目。
	kept := make(map[int]string, len(got.Segments))
	for _, s := range got.Segments {
		kept[s.Sequence] = s.Text
	}

	// 1) 跨语种幻觉必须消失（原本是纯英文/葡语/法语）。
	hallucSeq := []int{87, 182, 185, 186, 202, 207, 209, 218, 220, 228, 232, 233, 240, 241}
	for _, seq := range hallucSeq {
		if text, ok := kept[seq]; ok {
			t.Errorf("seq=%d 应是跨语种幻觉被丢弃，实际保留：%q", seq, text)
		}
	}

	// 2) 长循环必须被折叠（不再是几百字符）。
	loopSeq := []int{100, 129, 196, 204, 229, 178}
	for _, seq := range loopSeq {
		text, ok := kept[seq]
		if !ok {
			continue // 被整条丢弃也可接受
		}
		if len([]rune(text)) > 60 {
			t.Errorf("seq=%d 的长循环应被折叠到 60 字以内，实际 %d 字：%q",
				seq, len([]rune(text)), text)
		}
	}

	// 3) 真实对白不能被误伤。
	realSeq := map[int]string{
		2:   "そうしよう。",
		43:  "ねえおじさんさっきはあんな感じだったけどさあここ見れたら見たいでしょ?",
		171: "おじさんすごいねおじさんなんか最初怒ってたのに結局出しちゃってるじゃんほんとなよフフフねえでもさおじさんお金持ちそうだからもっといいことしてあげちゃおっかなねえねえねえフフフじゃあさっきいっぱい触ってね触らせてくれたからおかしい?",
		197: "ねえおじさんちんちんビクビクさせすぎああすごい動いちゃうねえねえこれじゃ上手になめれないよああくみ出されてももうビンビンなんだもんあっおじさんまたお尻見てるでしょあっほんと大好きだねお尻大好きなんだねうんうんああ",
		239: "すっごいいっぱい出てる笑ったすっごい楽しそうねえおじさん気持ちよかった気持ちよかったよねでもいっぱい出したんだからいっぱい買ってよねそうだよでもこれまたしようね続々だよ",
	}
	for seq, want := range realSeq {
		text, ok := kept[seq]
		if !ok {
			t.Errorf("seq=%d 是真实对白，不应被丢弃", seq)
			continue
		}
		if text != want {
			t.Errorf("seq=%d 真实对白被改动：\n  期望 %q\n  实际 %q", seq, want, text)
		}
	}

	// 4) 整体效果：至少丢掉 14 条幻觉，但保留绝大多数（>= 90%）。
	if got.Dropped < 14 {
		t.Errorf("期望至少丢弃 14 条幻觉，实际丢弃 %d 条", got.Dropped)
	}
	keepRatio := float64(len(got.Segments)) / float64(len(segments))
	if keepRatio < 0.90 {
		t.Errorf("保留比例 %.1f%% 过低（丢太多，可能误伤正常字幕）", keepRatio*100)
	}
	t.Logf("241 条 -> 保留 %d 条（丢弃 %d 条，保留率 %.1f%%）",
		len(got.Segments), got.Dropped, keepRatio*100)
}
