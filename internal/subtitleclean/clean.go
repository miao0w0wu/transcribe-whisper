// Package subtitleclean 对识别结果做幻觉与噪声清洗。
//
// Whisper 类模型在静音、背景音乐或噪声段容易产出「重复」与「套话」字幕
// （例如「请订阅频道」「感谢观看」、括号里的音效描述、连续重复的同一句话）。
// 这里按可选的严格程度把这些条目剔除，提高字幕可用性。
//
// 参考实现：WhisperJAV 的 whisperjav/data/hallucination_filters/regexp_v09.json。
// 本包只做与语言无关的通用规则，不引入特定语种的脏词库，避免误伤正常内容。
package subtitleclean

import (
	"strings"
	"unicode"

	"transcribe-whisper/internal/asr"
)

// Level 是清洗严格程度。
type Level string

// 清洗等级。low ⊂ medium ⊂ high，等级越高删得越多。
const (
	// LevelOff 不做任何清洗。
	LevelOff Level = "off"
	// LevelLow 只去装饰符号、表情与纯标点条目。
	LevelLow Level = "low"
	// LevelMedium 另去括号音效描述与连续重复条目（推荐）。
	LevelMedium Level = "medium"
	// LevelHigh 另去常见幻觉套话与超短条目。
	LevelHigh Level = "high"
)

// Options 是清洗参数。
type Options struct {
	Level Level
}

// Result 是清洗结果。
type Result struct {
	// Segments 是清洗后的字幕（顺序与时间戳保持不变）。
	Segments []asr.Segment
	// Dropped 是整条被丢弃的条目数（不统计仅裁剪文本的条目）。
	Dropped int
}

// Clean 按等级清洗字幕。
//
// 返回的 Segments 是新的切片，不会修改入参；时间戳与 Sequence 原样保留，
// 这样调用方可以安全地把结果继续落库。
func Clean(segments []asr.Segment, opts Options) Result {
	level := normalizeLevel(opts.Level)
	if level == LevelOff {
		return Result{Segments: append([]asr.Segment(nil), segments...)}
	}

	out := make([]asr.Segment, 0, len(segments))
	dropped := 0
	// prevKey 用于识别「连续重复」：只有紧邻的重复才算幻觉。
	prevKey := ""

	for _, seg := range segments {
		text := sanitizeText(seg.Text)
		if level == LevelMedium || level == LevelHigh {
			text = stripSoundEffects(text)
		}
		text = strings.TrimSpace(text)

		if text == "" || isPunctuationOnly(text) {
			dropped++
			prevKey = ""
			continue
		}

		key := repeatKey(text)
		if level == LevelMedium || level == LevelHigh {
			if key != "" && key == prevKey {
				// 连续两次完全相同：判定为幻觉，整条丢弃。
				dropped++
				continue
			}
		}

		if level == LevelHigh {
			if isHallucinationPhrase(text) || visibleRuneCount(text) < 2 {
				dropped++
				prevKey = ""
				continue
			}
		}

		prevKey = key
		seg.Text = text
		out = append(out, seg)
	}

	return Result{Segments: out, Dropped: dropped}
}

// normalizeLevel 把等级规范成小写，未知值回落到 off（不擅自清洗）。
func normalizeLevel(level Level) Level {
	switch Level(strings.ToLower(strings.TrimSpace(string(level)))) {
	case LevelLow:
		return LevelLow
	case LevelMedium:
		return LevelMedium
	case LevelHigh:
		return LevelHigh
	default:
		return LevelOff
	}
}

// sanitizeText 去掉表情符号与不可见装饰字符，并压缩多余空白。
//
// 保留正常文字、标点与空格；只剔除 emoji、变体选择符、零宽字符这类
// 识别模型偶发输出的噪声。
func sanitizeText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case isInvisible(r):
			continue
		case isEmoji(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return collapseSpaces(b.String())
}

// isInvisible 判断零宽与格式控制字符。
func isInvisible(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', '\u00ad':
		return true
	}
	// 其它 Cf（格式）类字符一律视为不可见装饰。
	return unicode.Is(unicode.Cf, r)
}

// isEmoji 判断表情与装饰符号。
//
// 覆盖常见 emoji 区段、变体选择符与「键帽」组合符，避免把正常汉字误判。
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // 表情、交通、补充符号
		return true
	case r >= 0x2600 && r <= 0x27BF: // 杂项符号与装饰符号
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // 变体选择符
		return true
	case r == 0x20E3: // 键帽组合符
		return true
	}
	return false
}

// collapseSpaces 把连续空白压成一个空格并去掉首尾空白。
func collapseSpaces(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if prevSpace {
				continue
			}
			prevSpace = true
			b.WriteByte(' ')
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// soundEffectSpans 是括号类音效描述的成对定界符。
var soundEffectSpans = [][2]rune{
	{'(', ')'},
	{'（', '）'},
	{'[', ']'},
	{'［', '］'},
	{'【', '】'},
	{'〔', '〕'},
}

// stripSoundEffects 去掉括号内的音效描述，例如「(音乐)」「【掌声】」。
//
// 逐字符扫描而不是用正则：括号有全角半角多种写法，正则拼接易出错，
// 且需要处理嵌套与不闭合的情况。不闭合的左括号直接丢弃到结尾。
func stripSoundEffects(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		closer, ok := closerFor(r)
		if !ok {
			b.WriteRune(r)
			continue
		}
		// 找到配对的右括号；找不到就丢弃剩余部分。
		end := -1
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == closer {
				end = j
				break
			}
		}
		if end < 0 {
			break
		}
		i = end
	}
	return b.String()
}

// closerFor 返回左括号对应的右括号；不是左括号时 ok 为 false。
func closerFor(r rune) (rune, bool) {
	for _, span := range soundEffectSpans {
		if span[0] == r {
			return span[1], true
		}
	}
	return 0, false
}

// isPunctuationOnly 判断是否只有标点与符号、没有可读文字。
func isPunctuationOnly(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

// visibleRuneCount 统计可见字符数（不含空白与标点）。
func visibleRuneCount(text string) int {
	count := 0
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		count++
	}
	return count
}

// repeatKey 返回用于判重的规范化键：小写、去空白与标点。
//
// 只用于「紧邻重复」比较，因此不需要全局唯一性。
func repeatKey(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// hallucinationPhrases 是 Whisper 在静音段最常产出的套话。
//
// 只收「几乎不可能出现在真实对话里」的整句，避免误删正常字幕。
var hallucinationPhrases = []string{
	// 中文字幕组水印与套话
	"请不吝点赞订阅转发打赏支持明镜与点点栏目",
	"请不吝点赞订阅转发打赏支持明镜与点点",
	"字幕由amaraorg社区提供",
	"字幕提供者",
	"中文字幕志愿者",
	"字幕组",
	"感谢观看",
	"谢谢观看",
	"订阅我的频道",
	"点赞订阅",
	"下集再见",
	// 英文常见幻觉
	"thanksforwatching",
	"thankyouforwatching",
	"pleasesubscribe",
	"subscribetomychannel",
	"likesubscribe",
	"subtitlesby",
	"transcribedby",
	"amaraorg",
	// 日文常见幻觉
	"ご視聴ありがとうございました",
	"チャンネル登録お願いします",
	"高評価お願いします",
	"最後までご視聴いただきありがとうございました",
}

// isHallucinationPhrase 判断整句是否为已知幻觉套话。
//
// 用「规范化后包含」而不是全等：模型常在这些套话前后加标点或语气词，
// 而这类长句本身几乎不会出现在真实对话中，包含匹配的误伤风险很低。
func isHallucinationPhrase(text string) bool {
	key := repeatKey(text)
	if key == "" {
		return false
	}
	for _, phrase := range hallucinationPhrases {
		if strings.Contains(key, phrase) {
			return true
		}
	}
	return false
}
