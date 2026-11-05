package ffmpegsetup

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestZip 造一个结构类似真实发行包的压缩包：
// 顶层带版本目录，bin 下放 ffmpeg/ffprobe。
func buildTestZip(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	files := map[string]string{
		"ffmpeg-8.0-essentials_build/bin/ffmpeg.exe":  "fake ffmpeg binary",
		"ffmpeg-8.0-essentials_build/bin/ffprobe.exe": "fake ffprobe binary",
		"ffmpeg-8.0-essentials_build/README.txt":      "docs",
		"ffmpeg-8.0-essentials_build/doc/ffmpeg.html": "html",
	}
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
}

func TestExtractBinariesFindsVersionedLayout(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.zip")
	buildTestZip(t, archive)

	installDir := filepath.Join(dir, "install")
	ffmpegPath, ffprobePath, err := extractBinaries(archive, installDir)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	// 关键：顶层目录名带版本号，必须仍能找到文件。
	if filepath.Base(ffmpegPath) != "ffmpeg.exe" {
		t.Fatalf("ffmpeg 路径异常：%s", ffmpegPath)
	}
	if filepath.Base(ffprobePath) != "ffprobe.exe" {
		t.Fatalf("ffprobe 路径异常：%s", ffprobePath)
	}
	// 应落在 <dir>/bin/ 下。
	if filepath.Base(filepath.Dir(ffmpegPath)) != "bin" {
		t.Fatalf("应解压到 bin 目录，实际：%s", ffmpegPath)
	}
	// 内容正确。
	data, err := os.ReadFile(ffmpegPath)
	if err != nil {
		t.Fatalf("读取解压后的 ffmpeg 失败: %v", err)
	}
	if string(data) != "fake ffmpeg binary" {
		t.Fatalf("内容不一致：%q", data)
	}
	// 非必需文件不应落盘（文档、html）。
	if _, err := os.Stat(filepath.Join(installDir, "README.txt")); err == nil {
		t.Fatal("不应解压 README")
	}
}

// 同一压缩包里有多个 ffmpeg.exe（32/64 位）时优先选 64 位。
func TestExtractBinariesPrefersX64(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, content := range map[string]string{
		"pkg/bin/x86/ffmpeg.exe":   "32bit",
		"pkg/bin/x64/ffmpeg.exe":   "64bit",
		"pkg/bin/x64/ffprobe.exe":  "probe64",
	} {
		entry, _ := writer.Create(name)
		_, _ = entry.Write([]byte(content))
	}
	_ = writer.Close()
	_ = file.Close()

	ffmpegPath, _, err := extractBinaries(archive, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	data, _ := os.ReadFile(ffmpegPath)
	if string(data) != "64bit" {
		t.Fatalf("应优先选 64 位构建，实际内容：%q", data)
	}
}

func TestExtractBinariesRejectsZipWithoutFFmpeg(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.zip")
	file, _ := os.Create(archive)
	writer := zip.NewWriter(file)
	entry, _ := writer.Create("pkg/readme.txt")
	_, _ = entry.Write([]byte("nothing here"))
	_ = writer.Close()
	_ = file.Close()

	_, _, err := extractBinaries(archive, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("缺少 ffmpeg 时应报错")
	}
	if !strings.Contains(err.Error(), "没有找到 ffmpeg") {
		t.Fatalf("错误信息应说明缺少 ffmpeg，实际：%v", err)
	}
}

func TestInstallRequiresDir(t *testing.T) {
	_, err := Install(context.Background(), InstallOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "安装目录") {
		t.Fatalf("空目录应被拒绝，实际：%v", err)
	}
}

func TestInstallRejectsUnknownSource(t *testing.T) {
	_, err := Install(context.Background(), InstallOptions{
		Dir:      t.TempDir(),
		SourceID: "不存在的源",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "未知的下载源") {
		t.Fatalf("未知源应被拒绝，实际：%v", err)
	}
}

func TestInstallCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Install(ctx, InstallOptions{Dir: t.TempDir()}, nil)
	if err == nil {
		t.Fatal("已取消的上下文应返回错误")
	}
}

func TestDefaultSourcesOrder(t *testing.T) {
	sources := DefaultSources()
	if len(sources) < 2 {
		t.Fatalf("应至少有 2 个下载源，实际 %d", len(sources))
	}
	// gyan 优先，BtbN 兜底。
	if sources[0].ID != "gyan" {
		t.Fatalf("首选源应为 gyan，实际 %s", sources[0].ID)
	}
	if sources[1].ID != "btbn" {
		t.Fatalf("备选源应为 btbn，实际 %s", sources[1].ID)
	}
	for _, s := range sources {
		if !strings.HasPrefix(s.URL, "https://") {
			t.Fatalf("%s 的下载地址必须是 https：%s", s.ID, s.URL)
		}
		if s.Label == "" || s.Note == "" {
			t.Fatalf("%s 缺少展示信息", s.ID)
		}
	}
	// 返回的是副本，改它不能影响内置列表。
	sources[0].ID = "被改了"
	if DefaultSources()[0].ID != "gyan" {
		t.Fatal("DefaultSources 应返回副本")
	}
}

// 真实下载验证：需要 REALSETUP=1，会下载约 115MB。
func TestRealInstall(t *testing.T) {
	if os.Getenv("FFMPEGSETUP_REAL") != "1" {
		t.Skip("设置 FFMPEGSETUP_REAL=1 才执行真实下载（约 115MB）")
	}
	dir := t.TempDir()
	var lastMsg string
	result, err := Install(context.Background(), InstallOptions{Dir: dir}, func(ratio float64, msg string) {
		lastMsg = msg
	})
	if err != nil {
		t.Fatalf("真实下载失败: %v", err)
	}
	info, err := os.Stat(result.FFmpegPath)
	if err != nil {
		t.Fatalf("安装后的 ffmpeg 不存在: %v", err)
	}
	if info.Size() < 1024*1024 {
		t.Fatalf("ffmpeg 体积异常：%d 字节", info.Size())
	}
	t.Logf("安装来源=%s 路径=%s 大小=%d 最后消息=%s", result.Source, result.FFmpegPath, info.Size(), lastMsg)
}
