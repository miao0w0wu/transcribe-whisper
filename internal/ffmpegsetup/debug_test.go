package ffmpegsetup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestDebugDownloadHeader 只做「请求 + 读响应头」，确认连接与重定向是否正常。
// 不下载正文，用于把「连不上」与「下载慢/写盘卡住」区分开。
func TestDebugDownloadHeader(t *testing.T) {
	if os.Getenv("FFMPEGSETUP_DEBUG") != "1" {
		t.Skip("设置 FFMPEGSETUP_DEBUG=1 才执行")
	}
	source := DefaultSources()[0]
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("User-Agent", "transcribe-whisper/1.0")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败（%.1fs）: %v", time.Since(start).Seconds(), err)
	}
	defer resp.Body.Close()
	fmt.Printf("status=%d elapsed=%.2fs contentLength=%d type=%s\n",
		resp.StatusCode, time.Since(start).Seconds(), resp.ContentLength,
		resp.Header.Get("Content-Type"))

	// 只读前 256KB，确认正文能流动。
	buf := make([]byte, 256*1024)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("读取正文失败: %v", err)
	}
	fmt.Printf("读到 %d 字节，首字节=%v\n", n, buf[:4])
	if n < 1024 {
		t.Fatalf("正文只读到 %d 字节，不像正常下载", n)
	}
}
