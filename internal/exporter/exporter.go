// Package exporter 把字幕片段导出为 srt / vtt / txt / json / bilingual-srt 文本。
//
// 该包是纯函数式实现：不依赖 repository、不做文件 IO，调用方拿到字符串后自行落盘。
package exporter

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// 支持的导出格式常量。
const (
	FormatSRT          = "srt"
	FormatVTT          = "vtt"
	FormatTXT          = "txt"
	FormatJSON         = "json"
	FormatBilingualSRT = "bilingual-srt"
)

// supportedFormats 是全部导出格式，顺序即 SupportedFormats 的返回顺序。
var supportedFormats = []string{FormatSRT, FormatVTT, FormatTXT, FormatJSON, FormatBilingualSRT}

// Segment 是导出用的字幕片段。Source 为原文，Translated 为译文。
type Segment struct {
	ID         string
	Sequence   int
	Start      float64
	End        float64
	Source     string
	Translated string
}

// Options 控制导出细节。
type Options struct {
	Format        string // srt / vtt / txt / json / bilingual-srt
	IncludeSource bool   // bilingual 或 txt 模式是否输出原文
	UseSource     bool   // 译文为空时是否回退原文
}

// SupportedFormats 返回支持的格式列表（副本，调用方可安全修改）。
func SupportedFormats() []string {
	return append([]string(nil), supportedFormats...)
}

// Extension 返回格式对应的扩展名（不含点）。
func Extension(format string) (string, error) {
	switch NormalizeFormat(format) {
	case FormatSRT:
		return "srt", nil
	case FormatVTT:
		return "vtt", nil
	case FormatTXT:
		return "txt", nil
	case FormatJSON:
		return "json", nil
	case FormatBilingualSRT:
		return "srt", nil
	}
	return "", unknownFormatError(format)
}

// NormalizeFormat 统一格式名：去空白、转小写，并把常见英文别名映射到标准名。
func NormalizeFormat(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "webvtt":
		return FormatVTT
	case "bilingual_srt", "srt-bilingual", "bilingualsrt":
		return FormatBilingualSRT
	case "text", "plain":
		return FormatTXT
	}
	return f
}

func unknownFormatError(format string) error {
	return fmt.Errorf("不支持的字幕导出格式 %q，支持的格式: %s", format, strings.Join(supportedFormats, ", "))
}

// Export 按 opts 导出字幕文本。
func Export(segments []Segment, opts Options) (string, error) {
	format := NormalizeFormat(opts.Format)
	switch format {
	case FormatSRT:
		return exportSRT(segments, opts), nil
	case FormatVTT:
		return exportVTT(segments, opts), nil
	case FormatTXT:
		return exportTXT(segments, opts), nil
	case FormatJSON:
		return exportJSON(segments, opts)
	case FormatBilingualSRT:
		return exportBilingualSRT(segments, opts), nil
	}
	return "", unknownFormatError(opts.Format)
}

// textFor 返回一条字幕实际要输出的文本：优先译文，译文为空且 UseSource 时回退原文。
func textFor(seg Segment, opts Options) string {
	translated := strings.TrimSpace(seg.Translated)
	if translated != "" {
		return translated
	}
	if opts.UseSource {
		return strings.TrimSpace(seg.Source)
	}
	return ""
}

// normalizeTimes 归一化时间轴：负数取 0，End < Start 时 End = Start。
func normalizeTimes(seg Segment) (float64, float64) {
	start, end := seg.Start, seg.End
	if math.IsNaN(start) || start < 0 {
		start = 0
	}
	if math.IsNaN(end) || end < 0 {
		end = 0
	}
	if end < start {
		end = start
	}
	return start, end
}

// formatTimestamp 把秒数格式化为 HH:MM:SS<sep>mmm，毫秒四舍五入且会进位到秒/分/时。
func formatTimestamp(seconds float64, sep string) string {
	if math.IsNaN(seconds) || seconds < 0 {
		seconds = 0
	}
	// 先把秒四舍五入到毫秒，避免 1.9999 -> 1999ms 这类浮点误差。
	totalMillis := int64(math.Round(seconds * 1000))
	hours := totalMillis / 3600000
	minutes := (totalMillis / 60000) % 60
	secs := (totalMillis / 1000) % 60
	millis := totalMillis % 1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", hours, minutes, secs, sep, millis)
}

func formatSRTTime(seconds float64) string { return formatTimestamp(seconds, ",") }
func formatVTTTime(seconds float64) string { return formatTimestamp(seconds, ".") }

func exportSRT(segments []Segment, opts Options) string {
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range segments {
		start, end := normalizeTimes(seg)
		text := textFor(seg, opts)
		b.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", seg.Sequence, formatSRTTime(start), formatSRTTime(end), text))
	}
	return b.String()
}

func exportVTT(segments []Segment, opts Options) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, seg := range segments {
		start, end := normalizeTimes(seg)
		text := textFor(seg, opts)
		b.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", seg.Sequence, formatVTTTime(start), formatVTTTime(end), text))
	}
	return b.String()
}

func exportTXT(segments []Segment, opts Options) string {
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range segments {
		if opts.IncludeSource {
			b.WriteString(strings.TrimSpace(seg.Source))
			b.WriteString("\t")
			b.WriteString(textFor(seg, opts))
		} else {
			b.WriteString(textFor(seg, opts))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func exportBilingualSRT(segments []Segment, opts Options) string {
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range segments {
		start, end := normalizeTimes(seg)
		source := strings.TrimSpace(seg.Source)
		translated := strings.TrimSpace(seg.Translated)
		if translated == "" && opts.UseSource {
			translated = source
		}
		b.WriteString(fmt.Sprintf("%d\n%s --> %s\n", seg.Sequence, formatSRTTime(start), formatSRTTime(end)))
		switch {
		case source != "" && translated != "":
			// 原文在上、译文在下。
			b.WriteString(source + "\n" + translated + "\n\n")
		case source != "":
			b.WriteString(source + "\n\n")
		case translated != "":
			b.WriteString(translated + "\n\n")
		default:
			b.WriteString("\n")
		}
	}
	return b.String()
}

// jsonSegment 是 json 导出的字段结构，标签与前端约定保持一致。
type jsonSegment struct {
	ID         string  `json:"id"`
	Sequence   int     `json:"sequence"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Source     string  `json:"source"`
	Translated string  `json:"translated"`
}

func exportJSON(segments []Segment, opts Options) (string, error) {
	items := make([]jsonSegment, 0, len(segments))
	for _, seg := range segments {
		start, end := normalizeTimes(seg)
		translated := strings.TrimSpace(seg.Translated)
		if translated == "" && opts.UseSource {
			translated = strings.TrimSpace(seg.Source)
		}
		items = append(items, jsonSegment{
			ID:         seg.ID,
			Sequence:   seg.Sequence,
			Start:      roundMillis(start),
			End:        roundMillis(end),
			Source:     seg.Source,
			Translated: translated,
		})
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("生成 JSON 字幕失败: %w", err)
	}
	return string(data) + "\n", nil
}

// roundMillis 把秒数收敛到毫秒精度，避免 JSON 里出现 12.350000000000001 这种噪声。
func roundMillis(seconds float64) float64 {
	return math.Round(seconds*1000) / 1000
}
