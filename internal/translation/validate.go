package translation

import (
	"sort"
	"strings"
	"unicode"
)

// Validate 过滤不可翻译的条目，并把需要改写的条目归一化。
//
// 规则与返回值语义：
//   - 文本为空、或只由空白组成的条目被直接丢弃（不进入模型、也不出现在结果里）；
//   - 纯标点/纯数字/纯符号的条目不送模型，原文直接透传为译文；
//   - 其余条目保持原有顺序参与后续翻译。
//
// 返回的两个切片共享同一批 Item 值副本，调用方不需要再复制。
func Validate(items []Item) (translatable []Item, passthrough []Choice) {
	translatable = make([]Item, 0, len(items))
	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		normalized := Item{ID: it.ID, Text: text}
		if !needsTranslation(text) {
			passthrough = append(passthrough, Choice{ID: it.ID, Text: text})
			continue
		}
		translatable = append(translatable, normalized)
	}
	return translatable, passthrough
}

// needsTranslation 判断一段文本是否值得送模型翻译。
//
// 只要文本里出现了字母或表意文字就认为需要翻译；
// 纯数字、纯标点、纯符号（例如 "♪♪"、"..."、"00:12"）都不需要。
func needsTranslation(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// applyGlossary 对一批译文做术语表后置校验。
//
// 如果源文本命中了术语表条目，但译文里没有出现指定的译法，就把这条标记为需要人工复核，
// 并在译文里补上一个内联标记，方便界面上定位；这个过程不阻断整体翻译。
//
// 返回值 needsReview 是受影响的 Choice 下标，方便调用方统计与提示。
func applyGlossary(src []Item, choices []Choice, glossary map[string]string) (out []Choice, needsReview []int) {
	out = make([]Choice, len(choices))
	copy(out, choices)
	if len(glossary) == 0 || len(src) == 0 {
		return out, nil
	}

	srcText := make(map[string]string, len(src))
	for _, it := range src {
		srcText[it.ID] = it.Text
	}

	// 固定遍历顺序，保证同样的输入得到同样的输出。
	keys := make([]string, 0, len(glossary))
	for k := range glossary {
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})

	for i := range out {
		original, ok := srcText[out[i].ID]
		if !ok {
			continue
		}
		missing := missingTerms(original, out[i].Text, keys, glossary)
		if len(missing) == 0 {
			continue
		}
		out[i].Text = appendGlossaryNote(out[i].Text, missing)
		needsReview = append(needsReview, i)
	}
	return out, needsReview
}

// termGap 表示源文本命中、但译文里缺失指定译法的术语。
type termGap struct {
	Source string
	Target string
}

// missingTerms 返回源文本命中、但译文里缺失指定译法的术语。
func missingTerms(original, translated string, keys []string, glossary map[string]string) []termGap {
	var missing []termGap
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if !strings.Contains(original, k) {
			continue
		}
		target := strings.TrimSpace(glossary[k])
		if target == "" || seen[k+"\x00"+target] {
			continue
		}
		seen[k+"\x00"+target] = true
		if !strings.Contains(translated, target) {
			missing = append(missing, termGap{Source: k, Target: target})
		}
	}
	return missing
}

// appendGlossaryNote 在译文末尾追加术语提示，便于人工复核时一眼看到缺口。
func appendGlossaryNote(text string, missing []termGap) string {
	parts := make([]string, 0, len(missing))
	for _, g := range missing {
		if g.Source != "" && g.Source != g.Target {
			parts = append(parts, g.Source+"→"+g.Target)
		} else {
			parts = append(parts, g.Target)
		}
	}
	sort.Strings(parts)
	note := "【待复核：术语 " + strings.Join(parts, "、") + "】"
	text = strings.TrimSpace(text)
	if text == "" {
		return note
	}
	return text + " " + note
}
