package repository

import (
	"fmt"
	"time"
)

// timeLayout 是数据库中时间列的存储格式（UTC RFC3339 字符串）。
const timeLayout = time.RFC3339

// formatTime 把时间统一转成 UTC 的 RFC3339 字符串；零值写空串。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

// parseTime 把数据库里的字符串还原成时间，兼容历史数据中的几种常见写法。
func parseTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("解析时间 %q 失败: %w", raw, lastErr)
}
