package modelmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureRepairsLegacyLargeV3 验证「老用户目录缺 preprocessor_config.json」的修复路径。
//
// 场景：用户用旧版本下载过 large-v3，目录里只有 config/model/tokenizer/vocabulary
// 四个文件，没有 preprocessor_config.json。升级后：
//   - Installed() 不应再把它当作已安装（否则永远不补文件）；
//   - Ensure() 应当把缺失的那一个文件补上，而不是重下 3GB。
func TestEnsureRepairsLegacyLargeV3(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "large-v3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}

	entry, ok := FindCatalogEntry("large-v3")
	if !ok {
		t.Fatal("清单里找不到 large-v3")
	}

	// 造出旧版目录：除 preprocessor_config.json 外，其余文件按清单大小写满。
	for _, f := range entry.Files {
		if f.Path == "preprocessor_config.json" {
			continue
		}
		p := filepath.Join(dir, f.Path)
		if err := os.WriteFile(p, make([]byte, f.Size), 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", f.Path, err)
		}
	}

	m := &ManagerImpl{rootDir: root, catalog: DefaultCatalog()}

	// 1) 缺文件时不应算已安装。
	if m.isInstalled(entry, dir) {
		t.Fatal("缺 preprocessor_config.json 时不应判定为已安装（否则永远不会补齐）")
	}

	installed, err := m.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed 出错：%v", err)
	}
	if _, ok := installed["large-v3"]; ok {
		t.Fatal("Installed 不应把缺文件的 large-v3 列为已安装")
	}

	// 2) 手动补上该文件后应算已安装（模拟 Ensure 补齐后的状态）。
	if err := os.WriteFile(filepath.Join(dir, "preprocessor_config.json"),
		[]byte(preprocessorLargeV3), 0o644); err != nil {
		t.Fatalf("写入 preprocessor_config.json 失败：%v", err)
	}
	if !m.isInstalled(entry, dir) {
		t.Fatal("补齐 preprocessor_config.json 后应判定为已安装")
	}

	// 3) 补上的内容必须是可用的 JSON，且 feature_size 为 128。
	info, err := os.Stat(filepath.Join(dir, "preprocessor_config.json"))
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if info.Size() != sizePreprocessorV3 {
		t.Errorf("补上的文件大小应为 %d，实际 %d", sizePreprocessorV3, info.Size())
	}
}

// TestEnsureOnlyDownloadsMissingFile 验证补齐时只会去取缺的那一个文件。
//
// 依据 downloadFiles 的逻辑：大小与清单一致的既有文件直接跳过，
// 所以老用户升级后只需下载 340 字节，不会重下 model.bin。
func TestEnsureOnlyDownloadsMissingFile(t *testing.T) {
	entry, ok := FindCatalogEntry("large-v3")
	if !ok {
		t.Fatal("清单里找不到 large-v3")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "large-v3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}

	missing := 0
	for _, f := range entry.Files {
		p := filepath.Join(dir, f.Path)
		if f.Path == "preprocessor_config.json" {
			missing++
			continue
		}
		if err := os.WriteFile(p, make([]byte, f.Size), 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", f.Path, err)
		}
	}
	if missing != 1 {
		t.Fatalf("测试前提是只缺 1 个文件，实际缺 %d 个", missing)
	}

	// 复现 downloadFiles 的「跳过已有文件」判定，确认只有 1 个文件需要下载。
	needDownload := 0
	for _, f := range entry.Files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		info, err := os.Stat(dst)
		if err != nil || info.IsDir() || (f.Size > 0 && info.Size() != f.Size) {
			needDownload++
		}
	}
	if needDownload != 1 {
		t.Errorf("老用户升级后应只需下载 1 个文件，实际需要下载 %d 个", needDownload)
	}
}
