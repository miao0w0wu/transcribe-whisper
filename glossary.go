package main

import (
	"errors"
	"sort"
	"strings"
)

// defaultStyle 归一化翻译语气，空值回落到影视字幕风格。
func defaultStyle(style string) string {
	normalized := strings.ToLower(strings.TrimSpace(style))
	switch normalized {
	case "subtitle", "formal", "casual", "literal", "explicit":
		return normalized
	default:
		return "subtitle"
	}
}

// parseGlossary 解析术语表文本，每行一条，格式 `源术语=目标译法`。
//
// 兼容全角等号与多余空白；忽略空行和以 # 开头的注释行；
// 格式错误时返回带行号的中文错误，避免用户保存后才发现术语没生效。
func parseGlossary(text string) (map[string]string, error) {
	result := make(map[string]string)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		normalized := strings.ReplaceAll(line, "＝", "=")
		parts := strings.SplitN(normalized, "=", 2)
		if len(parts) != 2 {
			return nil, errors.New("术语表第 " + itoa(index+1) + " 行格式错误，应为「源术语=目标译法」：" + line)
		}
		source := strings.TrimSpace(parts[0])
		target := strings.TrimSpace(parts[1])
		if source == "" || target == "" {
			return nil, errors.New("术语表第 " + itoa(index+1) + " 行的术语或译法为空：" + line)
		}
		result[source] = target
	}
	return result, nil
}

// formatGlossary 把术语表还原成可编辑文本，按源术语排序保证稳定。
func formatGlossary(glossary map[string]string) string {
	if len(glossary) == 0 {
		return ""
	}
	keys := make([]string, 0, len(glossary))
	for key := range glossary {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+glossary[key])
	}
	return strings.Join(lines, "\n")
}

// itoa 是 strconv.Itoa 的轻量封装，避免在纯字符串拼接里多一个导入。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
