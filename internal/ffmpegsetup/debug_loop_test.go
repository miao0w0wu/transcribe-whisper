package ffmpegsetup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// defaultClientForDebug 返回与 Install 默认一致的客户端。
func defaultClientForDebug() *http.Client {
	return &http.Client{Timeout: downloadTimeout}
}

// TestDebugDownloadLoop 把 downloadArchive 的循环单独跑一遍并打印每步，
// 用于定位「.part 一直 0 字节」到底卡在哪个环节。
func TestDebugDownloadLoop(t *testing.T) {
	if os.Getenv("FFMPEGSETUP_DEBUG") != "1" {
		t.Skip("设置 FFMPEGSETUP_DEBUG=1 才执行")
	}
	dir := t.TempDir()
	source := DefaultSources()[0]

	var lastRatio float64
	start := time.Now()
	path, err := downloadArchive(context.Background(), defaultClientForDebug(), source, dir, func(ratio float64, msg string) {
		// 只在进度有明显变化时打印，避免刷屏。
		if ratio-lastRatio > 0.05 {
			lastRatio = ratio
			fmt.Printf("[%5.1f%%] %s (%.1fs)\n", ratio*100, msg, time.Since(start).Seconds())
		}
	})
	if err != nil {
		t.Fatalf("下载失败（%.1fs）: %v", time.Since(start).Seconds(), err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("产物不存在: %v", statErr)
	}
	fmt.Printf("下载完成：%s（%.1f MB，%.1fs）\n", path, float64(info.Size())/1048576, time.Since(start).Seconds())
	if info.Size() < 100*1024*1024 {
		t.Fatalf("文件太小：%d 字节", info.Size())
	}
	_ = os.Remove(path)
}
