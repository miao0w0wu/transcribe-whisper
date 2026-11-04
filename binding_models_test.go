package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcribe-whisper/internal/modelmanager"
)

// writeLocalModelDir 造一个可被识别的本地 CTranslate2 模型目录。
func writeLocalModelDir(t *testing.T, dir string) {
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

func TestBindingAddLocalModel(t *testing.T) {
	app := newFrontendApp(t)
	dir := filepath.Join(t.TempDir(), "my-local-model")
	writeLocalModelDir(t, dir)

	info, err := app.AddLocalModel(AddLocalModelInput{Directory: dir, ID: "my-local"})
	if err != nil {
		t.Fatalf("添加本地模型失败: %v", err)
	}
	if info.ID != "my-local" || !info.Local || !info.Custom {
		t.Fatalf("返回信息不符合预期: %+v", info)
	}
	if info.Engine != modelmanager.EngineFasterWhisper {
		t.Fatalf("引擎应识别为 faster-whisper，实际 %q", info.Engine)
	}
	if !info.InstalledOK {
		t.Fatal("目录有效时应标记为可用")
	}

	// 列表里应能看到它，且落到了设置中（重启后可恢复）。
	list := app.ListModels()
	found := false
	for _, m := range list {
		if m.ID == "my-local" {
			found = true
			if !m.Local || m.LocalPath != dir {
				t.Fatalf("列表中本地模型信息不对: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("ListModels 应包含新增的本地模型")
	}
	if app.currentSettings().LocalModels["my-local"] != dir {
		t.Fatalf("本地模型应写入设置，实际 %+v", app.currentSettings().LocalModels)
	}
}

func TestBindingAddLocalModelRejectsBadDir(t *testing.T) {
	app := newFrontendApp(t)
	if _, err := app.AddLocalModel(AddLocalModelInput{Directory: ""}); err == nil {
		t.Fatal("空目录应被拒绝")
	}
	if _, err := app.AddLocalModel(AddLocalModelInput{Directory: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("不存在的目录应被拒绝")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if _, err := app.AddLocalModel(AddLocalModelInput{Directory: empty}); err == nil {
		t.Fatal("没有权重文件的目录应被拒绝")
	}
}

func TestBindingRemoveLocalModelKeepsFiles(t *testing.T) {
	app := newFrontendApp(t)
	dir := filepath.Join(t.TempDir(), "keep-me")
	writeLocalModelDir(t, dir)
	if _, err := app.AddLocalModel(AddLocalModelInput{Directory: dir, ID: "keep-me"}); err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	if err := app.RemoveModel("keep-me"); err != nil {
		t.Fatalf("移除失败: %v", err)
	}
	// 磁盘文件必须保留。
	if _, err := os.Stat(filepath.Join(dir, "model.bin")); err != nil {
		t.Fatalf("移除本地模型不应删除文件: %v", err)
	}
	// 设置里也应清掉。
	if _, ok := app.currentSettings().LocalModels["keep-me"]; ok {
		t.Fatal("移除后设置里不应保留记录")
	}
	for _, m := range app.ListModels() {
		if m.ID == "keep-me" {
			t.Fatal("移除后列表里不应再出现")
		}
	}
}

func TestBindingDownloadLocalModelRejected(t *testing.T) {
	app := newFrontendApp(t)
	dir := filepath.Join(t.TempDir(), "m")
	writeLocalModelDir(t, dir)
	if _, err := app.AddLocalModel(AddLocalModelInput{Directory: dir, ID: "m"}); err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	_, err := app.DownloadModel("m")
	if err == nil || !strings.Contains(err.Error(), "不需要下载") {
		t.Fatalf("本地模型不应可下载，实际: %v", err)
	}
}

// 内置模型的 ID 与仓库名不一致时也必须能发起下载（cohere 属于这种情况）。
func TestBindingDownloadCohereResolvesByID(t *testing.T) {
	app := newFrontendApp(t)
	// 拿不到网络也没关系：这里只验证「能通过清单校验并进入下载流程」，
	// 校验失败的报错会包含「未知的模型」，那是我们要排除的。
	info, err := app.DownloadModel("cohere-transcribe")
	if err != nil {
		if strings.Contains(err.Error(), "未知的模型") {
			t.Fatalf("清单里的模型不应报未知: %v", err)
		}
		// 其他错误（例如受限仓库需要令牌）是可接受的。
		t.Logf("下载返回可接受错误: %v", err)
		return
	}
	if info == nil || info.ID != "cohere-transcribe" {
		t.Fatalf("应返回 cohere-transcribe，实际 %+v", info)
	}
}

func TestBindingCohereIsTransformersEngine(t *testing.T) {
	app := newFrontendApp(t)
	entry, ok := modelmanager.FindCatalogEntry("cohere-transcribe")
	if !ok {
		t.Fatal("清单应包含 cohere-transcribe")
	}
	if entry.Engine != modelmanager.EngineTransformers {
		t.Fatalf("cohere 应使用 transformers 引擎，实际 %q", entry.Engine)
	}
	// 真实体积来自仓库，约 4.13GB。
	if entry.SizeBytes < 4_000_000_000 {
		t.Fatalf("体积不合理: %d", entry.SizeBytes)
	}
	// 通过 Binding 暴露给前端时也要带上引擎信息。
	found := false
	for _, m := range app.ListModels() {
		if m.ID == "cohere-transcribe" {
			found = true
			if m.Engine != modelmanager.EngineTransformers {
				t.Fatalf("ModelInfo.Engine 应为 transformers，实际 %q", m.Engine)
			}
		}
	}
	if !found {
		t.Fatal("ListModels 应包含 cohere-transcribe")
	}
}

func TestBindingRepoModelRejectsBadInput(t *testing.T) {
	app := newFrontendApp(t)
	if _, err := app.AddRepoModel(AddRepoModelInput{Repo: ""}); err == nil {
		t.Fatal("空仓库应被拒绝")
	}
	if _, err := app.AddRepoModel(AddRepoModelInput{Repo: "onlyname"}); err == nil {
		t.Fatal("缺少 org 的仓库应被拒绝")
	}
}

// snapshotDump 把设置快照序列化成 JSON 文本，用于确认敏感字段不外泄。
func snapshotDump(t *testing.T, snapshot *SettingsSnapshot) string {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化设置快照失败: %v", err)
	}
	return string(data)
}

func TestBindingHFWordTokenRoundTrip(t *testing.T) {
	app := newFrontendApp(t)
	// 默认未配置。
	if app.GetSettings().HuggingFaceTokenSet {
		t.Fatal("默认不应标记为已配置令牌")
	}
	token := "hf_testtoken"
	updated, err := app.UpdateSettings(SettingsPatch{HuggingFaceToken: &token})
	if err != nil {
		t.Fatalf("保存令牌失败: %v", err)
	}
	if !updated.HuggingFaceTokenSet {
		t.Fatal("保存后应标记为已配置")
	}
	// 前端绝不能拿到明文：快照结构里只有布尔标记，没有令牌字段。
	if strings.Contains(snapshotDump(t, updated), token) {
		t.Fatal("不允许把令牌明文透给前端")
	}
	if app.currentSettings().HuggingFaceToken != token {
		t.Fatal("后端应保存令牌原文")
	}
	// 空串表示保持不变。
	empty := ""
	if _, err := app.UpdateSettings(SettingsPatch{HuggingFaceToken: &empty}); err != nil {
		t.Fatalf("空串更新失败: %v", err)
	}
	if app.currentSettings().HuggingFaceToken != token {
		t.Fatal("空串应表示保持不变")
	}
	// "-" 表示清除。
	clear := "-"
	cleared, err := app.UpdateSettings(SettingsPatch{HuggingFaceToken: &clear})
	if err != nil {
		t.Fatalf("清除令牌失败: %v", err)
	}
	if cleared.HuggingFaceTokenSet || app.currentSettings().HuggingFaceToken != "" {
		t.Fatal("清除后应不再配置")
	}
}

func TestBindingExplicitStyleRoundTrip(t *testing.T) {
	app := newFrontendApp(t)
	style := "explicit"
	updated, err := app.UpdateSettings(SettingsPatch{TranslationStyle: &style})
	if err != nil {
		t.Fatalf("保存风格失败: %v", err)
	}
	if updated.TranslationStyle != "explicit" {
		t.Fatalf("露骨风格应被保留，实际 %q", updated.TranslationStyle)
	}
	if app.currentSettings().TranslationStyle != "explicit" {
		t.Fatal("设置里应保存 explicit")
	}
}
