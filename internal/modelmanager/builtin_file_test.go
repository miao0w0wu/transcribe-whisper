package modelmanager

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDownloadFilesWritesBuiltinWithoutNetwork 验证内置文件不走网络。
//
// 用一个不可达的镜像构造 ManagerImpl：如果 preprocessor_config.json 真的
// 走网络，这里必然失败；能成功就说明是本地写入。
func TestDownloadFilesWritesBuiltinWithoutNetwork(t *testing.T) {
	entry, ok := FindCatalogEntry("large-v3")
	if !ok {
		t.Fatal("清单里找不到 large-v3")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "large-v3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}

	// 把除内置文件外的其它文件先按大小写满，避免测试真去下载 3GB。
	for _, f := range entry.Files {
		if f.Path == "preprocessor_config.json" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.Path), make([]byte, f.Size), 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", f.Path, err)
		}
	}

	m := New(Options{
		RootDir:    root,
		Catalog:    DefaultCatalog(),
		MaxRetries: 1,
		// 指向一个必然不可达的地址：内置文件若走网络就会失败。
		Mirrors:    []Mirror{{ID: "dead", Label: "不可达镜像", BaseURL: "http://127.0.0.1:1"}},
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})

	var msgs []string
	errs, fatal := m.downloadFiles(context.Background(), entry, dir, func(_ int64, msg string) {
		msgs = append(msgs, msg)
	})
	if fatal != nil {
		t.Fatalf("不应有致命错误：%v", fatal)
	}
	if len(errs) != 0 {
		t.Fatalf("不应有下载失败（内置文件不该联网）：%v", errs)
	}

	p := filepath.Join(dir, "preprocessor_config.json")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("内置文件未被写入：%v", err)
	}
	if info.Size() != sizePreprocessorV3 {
		t.Errorf("写入大小应为 %d，实际 %d", sizePreprocessorV3, info.Size())
	}

	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if string(content) != preprocessorLargeV3 {
		t.Errorf("写入内容与内置常量不一致")
	}
	// 内容必须能通过 isInstalled 的检查。
	if !m.isInstalled(entry, dir) {
		t.Error("补齐后应判定为已安装")
	}
	// 确认没有任何 .tmp 残留。
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Error("不应残留 .tmp 文件")
	}
}

// TestBuiltinFileContentsOnlyForLargeV3Preprocessor 确认内置映射范围最小。
//
// 只对 large-v3 的 preprocessor_config.json 生效，其它模型/文件仍走下载。
func TestBuiltinFileContentsOnlyForLargeV3Preprocessor(t *testing.T) {
	largeV3, _ := FindCatalogEntry("large-v3")
	small, _ := FindCatalogEntry("small")

	if got := builtinFileContents(largeV3, "preprocessor_config.json"); got == nil {
		t.Error("large-v3 的 preprocessor_config.json 应有内置内容")
	}
	if got := builtinFileContents(largeV3, "model.bin"); got != nil {
		t.Error("model.bin 不应有内置内容")
	}
	if got := builtinFileContents(largeV3, "config.json"); got != nil {
		t.Error("config.json 不应有内置内容")
	}
	if got := builtinFileContents(small, "preprocessor_config.json"); got != nil {
		t.Error("small 没有该文件，不应有内置内容")
	}
}

// TestBuiltinFileOverwritesWrongContent 确认已有但内容错误的文件会被覆盖。
//
// 场景：用户目录里被代理写进了一个错误页（大小恰好落在容差内）。
func TestBuiltinFileOverwritesWrongContent(t *testing.T) {
	entry, ok := FindCatalogEntry("large-v3")
	if !ok {
		t.Fatal("清单里找不到 large-v3")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "large-v3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}
	for _, f := range entry.Files {
		if f.Path == "preprocessor_config.json" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.Path), make([]byte, f.Size), 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", f.Path, err)
		}
	}

	// 写入一段大小不同、内容错误的占位文件。
	bad := strings.Repeat("x", 100)
	if err := os.WriteFile(filepath.Join(dir, "preprocessor_config.json"), []byte(bad), 0o644); err != nil {
		t.Fatalf("写入错误文件失败：%v", err)
	}

	m := New(Options{
		RootDir:    root,
		Catalog:    DefaultCatalog(),
		MaxRetries: 1,
		Mirrors:    []Mirror{{ID: "dead", Label: "不可达", BaseURL: "http://127.0.0.1:1"}},
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})
	if errs, fatal := m.downloadFiles(context.Background(), entry, dir, func(int64, string) {}); fatal != nil || len(errs) > 0 {
		t.Fatalf("应成功覆盖，实际 fatal=%v errs=%v", fatal, errs)
	}

	content, err := os.ReadFile(filepath.Join(dir, "preprocessor_config.json"))
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if string(content) != preprocessorLargeV3 {
		t.Error("内容错误的旧文件应被内置内容覆盖")
	}
}
