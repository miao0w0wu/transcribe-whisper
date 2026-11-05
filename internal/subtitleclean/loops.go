package subtitleclean

import (
	"strings"
	"unicode"
)

// 长循环折叠的参数。
//
// Whisper/cohere 这类模型在解码退化时会陷入循环，把同一单元吐几十上百次：
// 「あ」×429、「ふん」×150、英文短语重复几十遍。真实的语气重复（「うんうんうん」
// 「ねえねえねえ」「あっあっあっ」）长度很短，因此用「重复段总长」而不是
// 「重复次数」作为主要闸门，就能把两者区分开。
const (
	// loopUnitMinLen / loopUnitMaxLen 是重复单元的长度范围（按 rune 计）。
	loopUnitMinLen = 1
	loopUnitMaxLen = 4
	// loopMinRepeat 是判定为循环所需的最少连续重复次数。
	loopMinRepeat = 3
	// loopMinSpanLen 是判定为循环所需的重复段最短总长（按 rune 计）。
	loopMinSpanLen = 20
	// loopKeepRepeat 是折叠后保留的重复次数，保留一点真实感叹语气。
	loopKeepRepeat = 2

	// loopPhraseMaxLen 是「整句级重复单元」的最大长度（按 rune 计）。
	//
	// Whisper 在噪声段跑飞时会整句重复："I don't give much. " ×60、
	// "Ai, que monte. " ×70。这类单元远超 4 个 rune，只靠上面的短单元
	// 扫描折叠不掉，会原样送进翻译层——实测会让专业翻译模型跟着陷入循环，
	// Ollama 直接以 500「token repeat limit reached」中止整批。
	loopPhraseMaxLen = 40
	// loopPhraseMinSpanLen 是整句级循环所需的重复段最短总长。
	//
	// 取 60 而不是 20，是为了把「真的重复说了几遍同一句台词」和
	// 「解码退化」区分开：正常对白不会把同一句话重复到 60 个字符以上。
	loopPhraseMinSpanLen = 60
)

// CollapseLongLoops 把长循环折叠成保留 2 次重复。
//
// 判定条件：同一单元（1..4 个 rune）连续重复 >= 3 次，且重复段总长 >= 20 个 rune。
// 满足时把整段替换成「单元 × 2」；不满足的重复原样保留，因此
// 「うんうんうん」「ねえねえねえ」「あっあっあっ」这类短语气重复不受影响。
//
// 例："あ"×429 -> "ああ"；"ふん"×150 -> "ふんふん"。
//
// 该函数是纯函数，不做 trim，调用方负责前后的空白处理。
func CollapseLongLoops(text string) string {
	runes := []rune(text)
	if len(runes) < loopMinSpanLen {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))

	// i 是尚未输出的起点。
	i := 0
	for i < len(runes) {
		unitLen, repeat, ok := longestLoopAt(runes, i)
		if !ok {
			b.WriteRune(runes[i])
			i++
			continue
		}
		// 折叠：只保留前 loopKeepRepeat 次重复。
		for k := 0; k < unitLen*loopKeepRepeat; k++ {
			b.WriteRune(runes[i+k])
		}
		i += unitLen * repeat
	}
	return b.String()
}

// longestLoopAt 判断从 start 开始是否存在满足条件的长循环。
//
// 返回重复单元长度、重复次数与是否命中；命中时 span 覆盖 unitLen*repeat 个 rune。
// 先按短单元（1..4 rune）扫描，命中即返回，保证 "あ"×429 这类单字符循环
// 优先折叠；短单元无命中时再尝试整句级重复（见 loopPhraseMaxLen）。
func longestLoopAt(runes []rune, start int) (unitLen int, repeat int, ok bool) {
	remaining := len(runes) - start
	for ul := loopUnitMinLen; ul <= loopUnitMaxLen; ul++ {
		if remaining < ul*loopMinRepeat {
			break
		}
		rep := countRepeats(runes, start, ul)
		if rep < loopMinRepeat {
			continue
		}
		if ul*rep < loopMinSpanLen {
			continue
		}
		return ul, rep, true
	}

	// 整句级重复：单元更长，需要更大的 span 阈值才判定为循环。
	for ul := loopUnitMaxLen + 1; ul <= loopPhraseMaxLen; ul++ {
		if remaining < ul*2 {
			break
		}
		rep := countRepeats(runes, start, ul)
		if rep < 2 {
			continue
		}
		if ul*rep < loopPhraseMinSpanLen {
			continue
		}
		return ul, rep, true
	}
	return 0, 0, false
}

// countRepeats 统计从 start 开始、长度为 unitLen 的单元连续重复了多少次。
func countRepeats(runes []rune, start, unitLen int) int {
	if unitLen <= 0 || start+unitLen > len(runes) {
		return 0
	}
	repeat := 1
	for {
		next := start + unitLen*repeat
		if next+unitLen > len(runes) {
			return repeat
		}
		if !equalRunes(runes[start:start+unitLen], runes[next:next+unitLen]) {
			return repeat
		}
		repeat++
	}
}

// equalRunes 比较两段等长 rune 切片是否相同。
func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// latinLetterThreshold 是判定跨语种幻觉所需的最少拉丁字母数。
//
// 取 8 是为了放过 "OK"、"No"、"Hi" 这类正常的短外来语，
// 同时覆盖 "I don't give much."（15 个字母）这种整句跑飞的情况。
const latinLetterThreshold = 8

// languageRuneRanges 返回某种语言的核心文字 rune 区间。
//
// 只对「有专属文字」的语言返回区间：ja/zh/ko/ru/ar/he/th 等。
// 拉丁语系（en/fr/pt/es/de…）返回 nil，因为拉丁字母在日语视频里出现
// 本身不是幻觉证据（人名、术语、歌词都可能正常出现）。
func languageRuneRanges(lang string) [][2]rune {
	primary := normalizeLanguageTag(lang)
	switch primary {
	case "ja":
		// 平假名、片假名、汉字（含扩展 A）、片假名长音符与中点。
		return [][2]rune{
			{0x3040, 0x309F}, // 平假名
			{0x30A0, 0x30FF}, // 片假名
			{0x31F0, 0x31FF}, // 片假名语音扩展
			{0xFF66, 0xFF9F}, // 半角片假名
			{0x4E00, 0x9FFF}, // CJK 统一汉字
			{0x3400, 0x4DBF}, // CJK 扩展 A
			{0xF900, 0xFAFF}, // CJK 兼容汉字
		}
	case "zh":
		return [][2]rune{
			{0x4E00, 0x9FFF},
			{0x3400, 0x4DBF},
			{0xF900, 0xFAFF},
			{0x3005, 0x3007}, // 々 〇
		}
	case "ko":
		return [][2]rune{
			{0xAC00, 0xD7AF}, // 谚文音节
			{0x1100, 0x11FF}, // 谚文字母
			{0x3130, 0x318F}, // 谚文兼容字母
			{0x4E00, 0x9FFF}, // 汉字
		}
	case "ru":
		return [][2]rune{{0x0400, 0x04FF}}
	case "ar":
		return [][2]rune{{0x0600, 0x06FF}, {0x0750, 0x077F}}
	case "he":
		return [][2]rune{{0x0590, 0x05FF}}
	case "th":
		return [][2]rune{{0x0E00, 0x0E7F}}
	case "el":
		return [][2]rune{{0x0370, 0x03FF}}
	}
	return nil
}

// normalizeLanguageTag 取语言标签的主标签并小写，例如 "ja-JP" -> "ja"。
func normalizeLanguageTag(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	return lang
}

// isCrossLanguageHallucination 判断一条文本是否是「跑成外语」的幻觉。
//
// 判定：完全没有预期语言的字符，且拉丁字母数 >= latinLetterThreshold。
// 只要出现一个预期语言字符就认为不是幻觉——真实字幕里夹杂英文术语很常见。
func isCrossLanguageHallucination(text string, expectRanges [][2]rune) bool {
	if len(expectRanges) == 0 {
		return false
	}
	latin := 0
	for _, r := range text {
		if inRuneRanges(r, expectRanges) {
			return false
		}
		if unicode.Is(unicode.Latin, r) {
			latin++
		}
	}
	return latin >= latinLetterThreshold
}

// inRuneRanges 判断 rune 是否落在任一区间内。
func inRuneRanges(r rune, ranges [][2]rune) bool {
	for _, rg := range ranges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}
