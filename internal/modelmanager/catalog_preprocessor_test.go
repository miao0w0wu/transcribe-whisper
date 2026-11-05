package modelmanager

import "testing"

// TestLargeV3PreprocessorSizeMatchesConstant 防止常量与真实文件字节数不一致。
//
// 下载校验按清单里的 Size 做 ±1% 比对，若常量写错，用户下到的文件会被判
// 「大小不符」而反复重下，或者更糟：通过校验但内容被截断。
func TestLargeV3PreprocessorSizeMatchesConstant(t *testing.T) {
	got := int64(len(preprocessorLargeV3))
	if got != sizePreprocessorV3 {
		t.Fatalf("preprocessorLargeV3 实际 %d 字节，常量 sizePreprocessorV3 是 %d 字节", got, sizePreprocessorV3)
	}
}

// TestLargeV3CatalogRequiresPreprocessor 确认 large-v3 把 preprocessor_config.json 列为必需。
func TestLargeV3CatalogRequiresPreprocessor(t *testing.T) {
	entry, ok := FindCatalogEntry("large-v3")
	if !ok {
		t.Fatal("清单里找不到 large-v3")
	}
	found := false
	for _, rel := range entry.Required {
		if rel == "preprocessor_config.json" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("large-v3 的 Required 必须包含 preprocessor_config.json，实际：%v", entry.Required)
	}
	// 文件清单里也要有，否则 Ensure 不会去下载它。
	inFiles := false
	for _, f := range entry.Files {
		if f.Path == "preprocessor_config.json" {
			inFiles = true
			if f.Size != sizePreprocessorV3 {
				t.Errorf("清单里 preprocessor_config.json 大小应为 %d，实际 %d", sizePreprocessorV3, f.Size)
			}
		}
	}
	if !inFiles {
		t.Fatal("large-v3 的 Files 必须包含 preprocessor_config.json")
	}
}

// TestSmallModelsDoNotRequirePreprocessor 确认 tiny/base/small/medium 不受影响。
//
// 这四个仓库没有 preprocessor_config.json（实测 404），若把它们也列为必需，
// 下载会一直 404 失败。
func TestSmallModelsDoNotRequirePreprocessor(t *testing.T) {
	for _, id := range []string{"tiny", "base", "small", "medium"} {
		entry, ok := FindCatalogEntry(id)
		if !ok {
			t.Fatalf("清单里找不到 %s", id)
		}
		for _, rel := range entry.Required {
			if rel == "preprocessor_config.json" {
				t.Errorf("%s 不应要求 preprocessor_config.json（该仓库没有此文件）", id)
			}
		}
	}
}
