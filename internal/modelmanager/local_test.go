package modelmanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCT2 造一个最小的 CTranslate2 模型目录。
func writeFakeCT2(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	files := map[string][]byte{
		"config.json":    []byte(`{"model": "whisper"}`),
		"model.bin":      make([]byte, 2048),
		"tokenizer.json": []byte(`{}`),
		"vocabulary.txt": []byte("a\nb\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}
}

// writeFakeTransformers 造一个最小的 transformers 模型目录。
func writeFakeTransformers(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	files := map[string][]byte{
		"config.json":       []byte(`{"model_type": "cohere_asr"}`),
		"model.safetensors": make([]byte, 4096),
		"tokenizer.json":    []byte(`{}`),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}
}

func TestDetectLocalModelCT2(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tiny")
	writeFakeCT2(t, dir)
	engine, required, err := DetectLocalModel(dir)
	if err != nil {
		t.Fatalf("识别 CTranslate2 目录失败: %v", err)
	}
	if engine != EngineFasterWhisper {
		t.Fatalf("引擎应为 %s，实际 %s", EngineFasterWhisper, engine)
	}
	if len(required) == 0 {
		t.Fatal("应返回必需文件列表")
	}
}

func TestDetectLocalModelTransformers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cohere")
	writeFakeTransformers(t, dir)
	engine, _, err := DetectLocalModel(dir)
	if err != nil {
		t.Fatalf("识别 transformers 目录失败: %v", err)
	}
	if engine != EngineTransformers {
		t.Fatalf("引擎应为 %s，实际 %s", EngineTransformers, engine)
	}
}

// 用户选中「上一层目录」时应该自动向下找到模型目录。
func TestDetectLocalModelFindsNestedDir(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "models", "small")
	writeFakeCT2(t, nested)
	engine, _, err := DetectLocalModel(root)
	if err != nil {
		t.Fatalf("应能向下找到模型目录: %v", err)
	}
	if engine != EngineFasterWhisper {
		t.Fatalf("引擎应为 %s，实际 %s", EngineFasterWhisper, engine)
	}
}

func TestDetectLocalModelRejectsIncomplete(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(string)
		wantSub string
	}{
		{
			name: "空目录",
			setup: func(dir string) {
				_ = os.MkdirAll(dir, 0o755)
			},
			wantSub: "没有找到模型权重",
		},
		{
			name: "缺少词表",
			setup: func(dir string) {
				writeFakeCT2(t, dir)
				_ = os.Remove(filepath.Join(dir, "vocabulary.txt"))
			},
			wantSub: "词表",
		},
		{
			name: "缺少 tokenizer",
			setup: func(dir string) {
				writeFakeCT2(t, dir)
				_ = os.Remove(filepath.Join(dir, "tokenizer.json"))
			},
			wantSub: "tokenizer.json",
		},
		{
			name: "缺少 config",
			setup: func(dir string) {
				writeFakeCT2(t, dir)
				_ = os.Remove(filepath.Join(dir, "config.json"))
			},
			wantSub: "config.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "m")
			tc.setup(dir)
			_, _, err := DetectLocalModel(dir)
			if err == nil {
				t.Fatal("应返回错误")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误信息应包含 %q，实际：%v", tc.wantSub, err)
			}
		})
	}
}

func TestDetectLocalModelRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, _, err := DetectLocalModel(path); err == nil || !strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("文件路径应被拒绝，实际: %v", err)
	}
}

func TestAddLocalModelEnsureReturnsOriginalDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-model")
	writeFakeCT2(t, dir)
	m := New(Options{RootDir: filepath.Join(t.TempDir(), "root")})

	entry, err := m.AddLocalModel("my-local", dir)
	if err != nil {
		t.Fatalf("注册本地模型失败: %v", err)
	}
	if !entry.Custom {
		t.Fatal("应标记为自定义条目")
	}
	if entry.Engine != EngineFasterWhisper {
		t.Fatalf("引擎识别错误: %s", entry.Engine)
	}

	// Ensure 必须直接返回用户原始目录，而不是 rootDir 下的拼接路径。
	got, err := m.Ensure(context.Background(), "my-local", nil)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if got != dir {
		t.Fatalf("应返回原始目录 %q，实际 %q", dir, got)
	}
	if m.LocalDir("my-local") != dir {
		t.Fatalf("LocalDir 应返回原始目录，实际 %q", m.LocalDir("my-local"))
	}
}

func TestAddLocalModelRejectsDuplicateID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	writeFakeCT2(t, dir)
	m := New(Options{RootDir: t.TempDir()})
	if _, err := m.AddLocalModel("small", dir); err == nil {
		t.Fatal("与内置模型重名时应被拒绝")
	}
	if _, err := m.AddLocalModel("mine", dir); err != nil {
		t.Fatalf("首次注册应成功: %v", err)
	}
	if _, err := m.AddLocalModel("mine", dir); err == nil {
		t.Fatal("重复注册同一 ID 应被拒绝")
	}
}

func TestLocalModelAppearsInstalled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	writeFakeCT2(t, dir)
	m := New(Options{RootDir: filepath.Join(t.TempDir(), "root")})
	if _, err := m.AddLocalModel("mine", dir); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	installed, err := m.Installed(context.Background())
	if err != nil {
		t.Fatalf("扫描已安装失败: %v", err)
	}
	if installed["mine"] != dir {
		t.Fatalf("本地模型应出现在已安装列表，实际 %+v", installed)
	}
}

func TestLocalModelRemovedWhenDirGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	writeFakeCT2(t, dir)
	m := New(Options{RootDir: filepath.Join(t.TempDir(), "root")})
	if _, err := m.AddLocalModel("mine", dir); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}
	installed, err := m.Installed(context.Background())
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if _, ok := installed["mine"]; ok {
		t.Fatal("目录被删后不应仍算已安装")
	}
	// Ensure 也应给出可读错误，而不是返回一个不存在的路径。
	if _, err := m.Ensure(context.Background(), "mine", nil); err == nil {
		t.Fatal("目录不存在时 Ensure 应报错")
	}
}

func TestRemoveLocalModelKeepsFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	writeFakeCT2(t, dir)
	m := New(Options{RootDir: filepath.Join(t.TempDir(), "root")})
	if _, err := m.AddLocalModel("mine", dir); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	m.RemoveLocalModel("mine")
	if _, ok := m.Find("mine"); ok {
		t.Fatal("移除后不应再出现在清单里")
	}
	// 关键：磁盘上的文件必须原样保留。
	if _, err := os.Stat(filepath.Join(dir, "model.bin")); err != nil {
		t.Fatalf("移除本地模型不应删除用户文件: %v", err)
	}
}

func TestParseRepoInput(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"Systran/faster-whisper-small", "Systran/faster-whisper-small", false},
		{"  Systran/faster-whisper-small  ", "Systran/faster-whisper-small", false},
		{"https://hf-mirror.com/Systran/faster-whisper-small", "Systran/faster-whisper-small", false},
		{"https://huggingface.co/CohereLabs/cohere-transcribe-03-2026", "CohereLabs/cohere-transcribe-03-2026", false},
		{"https://huggingface.co/CohereLabs/cohere-transcribe-03-2026/tree/main", "CohereLabs/cohere-transcribe-03-2026", false},
		{"https://hf-mirror.com/viethang/cohere-transcribe-03-2026/tree/main", "viethang/cohere-transcribe-03-2026", false},
		{"", "", true},
		{"onlyname", "", true},
		{"https://huggingface.co", "", true},
	}
	for _, tc := range cases {
		got, err := ParseRepoInput(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q 应报错，实际得到 %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 解析失败: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q 应解析为 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}

func TestEssentialFiles(t *testing.T) {
	ct2Files := []ManifestFile{
		{Path: "config.json"}, {Path: "model.bin"},
		{Path: "tokenizer.json"}, {Path: "vocabulary.txt"}, {Path: "README.md"},
	}
	got := essentialFiles(ct2Files, EngineFasterWhisper)
	want := []string{"config.json", "model.bin", "tokenizer.json", "vocabulary.txt"}
	if len(got) != len(want) {
		t.Fatalf("CTranslate2 必需文件数量错误: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("必需文件顺序/内容错误: %v", got)
		}
	}

	stFiles := []ManifestFile{
		{Path: "config.json"}, {Path: "model.safetensors"},
		{Path: "tokenizer.model"}, {Path: "README.md"},
	}
	got = essentialFiles(stFiles, EngineTransformers)
	for _, name := range []string{"config.json", "model.safetensors", "tokenizer.model"} {
		found := false
		for _, g := range got {
			if g == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("transformers 必需文件应包含 %s，实际 %v", name, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("README 不应列入必需文件: %v", got)
	}
}

func TestIsNonEssentialPath(t *testing.T) {
	for _, p := range []string{"README.md", ".gitattributes", "assets/x.png", "demo/a.wav", ".eval_results/b.yaml"} {
		if !isNonEssentialPath(p) {
			t.Fatalf("%s 应判定为非必需", p)
		}
	}
	for _, p := range []string{"config.json", "model.bin", "onnx/encoder.onnx"} {
		if isNonEssentialPath(p) {
			t.Fatalf("%s 应判定为必需", p)
		}
	}
}
