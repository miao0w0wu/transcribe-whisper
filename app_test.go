package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcribe-whisper/internal/repository"
)

// writeTempMedia 创建一个占位媒体文件，用于满足源文件存在性校验。
func writeTempMedia(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake media"), 0o644); err != nil {
		t.Fatalf("写入测试媒体文件失败: %v", err)
	}
	return path
}

func TestCreateTaskRequiresSourceFile(t *testing.T) {
	app := NewApp()
	if _, err := app.CreateTask(CreateTaskInput{}); err == nil {
		t.Fatal("空源文件应当被拒绝")
	}
}

func TestCreateTaskRejectsMissingFile(t *testing.T) {
	app := NewApp()
	_, err := app.CreateTask(CreateTaskInput{SourceFile: `D:\Media\not-exists.mp4`})
	if err == nil || !strings.Contains(err.Error(), "源文件不存在") {
		t.Fatalf("不存在的源文件应当被拒绝，实际错误: %v", err)
	}
}

func TestCreateTaskFillsDefaults(t *testing.T) {
	app := NewApp()
	source := writeTempMedia(t, t.TempDir(), "episode.mp4")
	task, err := app.CreateTask(CreateTaskInput{SourceFile: source})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.Status != repository.TaskStatusCreated {
		t.Fatalf("新任务状态应为 created，实际 %s", task.Status)
	}
	if task.Name != "episode" {
		t.Fatalf("任务名应取自文件名，实际 %s", task.Name)
	}
	if task.SourceLanguage != "auto" || task.TargetLanguage != "zh-CN" {
		t.Fatalf("语言默认值不正确: %+v", task)
	}
	if task.Model == "" || task.Engine == "" || task.OutputFormat != "srt" {
		t.Fatalf("模型/引擎/输出格式应有默认值: %+v", task)
	}
	// 未选择翻译时 Provider 应归一为 none。
	if task.Provider != "none" {
		t.Fatalf("未开启翻译时 Provider 应为 none，实际 %s", task.Provider)
	}
}

func TestCreateTaskCloudProviderNeedsPrivacyConfirm(t *testing.T) {
	app := NewApp()
	source := writeTempMedia(t, t.TempDir(), "cloud.mp4")
	_, err := app.CreateTask(CreateTaskInput{SourceFile: source, Translate: true, Provider: "openai"})
	if err == nil || !strings.Contains(err.Error(), "隐私") {
		t.Fatalf("云端翻译未确认隐私时应被拒绝，实际错误: %v", err)
	}
}

func TestDashboardListsTasks(t *testing.T) {
	app := NewApp()
	dir := t.TempDir()
	first, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, dir, "a.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, dir, "b.mp4")}); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	dashboard := app.GetDashboard()
	if len(dashboard.Tasks) != 2 {
		t.Fatalf("应有两个任务，实际 %d", len(dashboard.Tasks))
	}
	if dashboard.ActiveTask == nil {
		t.Fatal("应有一个活动任务")
	}
	_ = first
}

func TestSubtitlesRoundTripAndExport(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, t.TempDir(), "episode.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	segments := []SubtitleSegment{
		{ID: task.ID + "-1", Sequence: 1, Start: 1.25, End: 3.5, SourceText: "Hello", Translated: "你好", Status: "translated"},
		{ID: task.ID + "-2", Sequence: 2, Start: 4, End: 6, SourceText: "World", Translated: "世界", Status: "translated"},
	}
	if err := app.UpdateSubtitles(task.ID, segments); err != nil {
		t.Fatalf("保存字幕失败: %v", err)
	}
	got, err := app.ListSubtitles(task.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if len(got) != 2 || got[0].Translated != "你好" {
		t.Fatalf("字幕保存结果不正确: %+v", got)
	}

	content, err := app.ExportSubtitles(task.ID, "srt")
	if err != nil {
		t.Fatalf("导出 SRT 失败: %v", err)
	}
	if !strings.Contains(content, "00:00:01,250 --> 00:00:03,500") || !strings.Contains(content, "你好") {
		t.Fatalf("SRT 内容不正确: %q", content)
	}

	vtt, err := app.ExportSubtitles(task.ID, "vtt")
	if err != nil {
		t.Fatalf("导出 VTT 失败: %v", err)
	}
	if !strings.HasPrefix(vtt, "WEBVTT") {
		t.Fatalf("VTT 应包含 WEBVTT 头: %q", vtt)
	}

	if _, err := app.ExportSubtitles(task.ID, "unknown"); err == nil {
		t.Fatal("未知导出格式应报错")
	}
}

func TestExportRejectsTaskWithoutSubtitles(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, t.TempDir(), "empty.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.ExportSubtitles(task.ID, "srt"); err == nil {
		t.Fatal("没有字幕的任务不应导出成功")
	}
}

func TestTasksPersistAcrossAppInstances(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "tasks.db")

	first := NewApp()
	if err := first.openDatabase(databasePath); err != nil {
		t.Fatalf("打开第一个数据库失败: %v", err)
	}
	task, err := first.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, t.TempDir(), "persisted.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := first.UpdateSubtitles(task.ID, []SubtitleSegment{{
		ID: task.ID + "-1", Sequence: 1, Start: 0.5, End: 2, SourceText: "Persisted", Translated: "已持久化", Status: "translated",
	}}); err != nil {
		t.Fatalf("保存字幕失败: %v", err)
	}
	first.shutdown(context.Background())

	second := NewApp()
	defer second.shutdown(context.Background())
	if err := second.openDatabase(databasePath); err != nil {
		t.Fatalf("打开第二个数据库失败: %v", err)
	}
	dashboard := second.GetDashboard()
	if dashboard.ActiveTask == nil || dashboard.ActiveTask.ID != task.ID {
		t.Fatalf("任务没有被恢复: %+v", dashboard)
	}
	segments, err := second.ListSubtitles(task.ID)
	if err != nil {
		t.Fatalf("读取恢复后的字幕失败: %v", err)
	}
	if len(segments) != 1 || segments[0].Translated != "已持久化" {
		t.Fatalf("字幕没有被恢复: %+v", segments)
	}
}

func TestTasksHaveIndependentSubtitleIDs(t *testing.T) {
	app := NewApp()
	dir := t.TempDir()
	first, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, dir, "first.mp4")})
	if err != nil {
		t.Fatalf("创建第一个任务失败: %v", err)
	}
	second, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, dir, "second.mp4")})
	if err != nil {
		t.Fatalf("创建第二个任务失败: %v", err)
	}
	if err := app.UpdateSubtitles(first.ID, []SubtitleSegment{{ID: first.ID + "-1", Sequence: 1, Start: 0, End: 1, SourceText: "a", Translated: "甲", Status: "translated"}}); err != nil {
		t.Fatalf("保存第一个任务字幕失败: %v", err)
	}
	if err := app.UpdateSubtitles(second.ID, []SubtitleSegment{{ID: second.ID + "-1", Sequence: 1, Start: 0, End: 1, SourceText: "b", Translated: "乙", Status: "translated"}}); err != nil {
		t.Fatalf("保存第二个任务字幕失败: %v", err)
	}
	firstSegments, err := app.ListSubtitles(first.ID)
	if err != nil {
		t.Fatalf("读取第一个任务字幕失败: %v", err)
	}
	secondSegments, err := app.ListSubtitles(second.ID)
	if err != nil {
		t.Fatalf("读取第二个任务字幕失败: %v", err)
	}
	if firstSegments[0].Translated != "甲" || secondSegments[0].Translated != "乙" {
		t.Fatalf("两个任务的字幕互相污染: %+v / %+v", firstSegments, secondSegments)
	}
}

func TestGetSettingsReturnsDefaults(t *testing.T) {
	app := NewApp()
	settings := app.GetSettings()
	if settings.AsrEngine != "faster-whisper" || settings.DefaultModel != "small" {
		t.Fatalf("设置默认值不正确: %+v", settings)
	}
	if settings.TranslationProvider != "ollama" {
		t.Fatalf("翻译默认应为本地 Ollama: %+v", settings)
	}
	if settings.TranslationAPIKeySet {
		t.Fatal("默认不应有 API Key")
	}
}

func TestUpdateSettingsPersistsAndValidates(t *testing.T) {
	app := NewApp()
	device := "cuda"
	privacy := true
	update, err := app.UpdateSettings(SettingsPatch{AsrDevice: &device, CloudPrivacyConfirm: &privacy})
	if err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}
	if update.AsrDevice != "cuda" || !update.CloudPrivacyConfirm {
		t.Fatalf("设置没有生效: %+v", update)
	}

	bad := "tpu"
	if _, err := app.UpdateSettings(SettingsPatch{AsrDevice: &bad}); err == nil {
		t.Fatal("非法设备应被拒绝")
	}

	key := "sk-test"
	withKey, err := app.UpdateSettings(SettingsPatch{TranslationAPIKey: &key})
	if err != nil {
		t.Fatalf("更新 API Key 失败: %v", err)
	}
	if !withKey.TranslationAPIKeySet {
		t.Fatal("设置 API Key 后应标记为已配置")
	}
}

func TestListModelsReturnsCatalog(t *testing.T) {
	app := NewApp()
	models := app.ListModels()
	if len(models) == 0 {
		t.Fatal("模型清单不应为空")
	}
	for _, model := range models {
		if model.ID == "" || model.Name == "" || model.Repo == "" {
			t.Fatalf("模型条目字段不完整: %+v", model)
		}
	}
}

func TestTaskLifecycleRejectsInvalidTransitions(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, t.TempDir(), "lifecycle.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := app.PauseTask(task.ID); err == nil {
		t.Fatal("未运行的任务不应能暂停")
	}
	if _, err := app.ResumeTask(task.ID); err == nil {
		t.Fatal("未暂停的任务不应能恢复")
	}
	if err := app.DeleteTask(task.ID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if _, err := app.ListSubtitles(task.ID); err == nil {
		t.Fatal("删除后的任务不应能读取字幕")
	}
}

func TestUpdateSubtitlesRejectsInvalidSegments(t *testing.T) {
	app := NewApp()
	task, err := app.CreateTask(CreateTaskInput{SourceFile: writeTempMedia(t, t.TempDir(), "invalid.mp4")})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	bad := []SubtitleSegment{{ID: "", Sequence: 1, Start: 0, End: 1, SourceText: "x", Status: "translated"}}
	if err := app.UpdateSubtitles(task.ID, bad); err == nil {
		t.Fatal("空字幕 ID 应被拒绝")
	}
	badTime := []SubtitleSegment{{ID: "a", Sequence: 1, Start: 5, End: 2, SourceText: "x", Status: "translated"}}
	if err := app.UpdateSubtitles(task.ID, badTime); err == nil {
		t.Fatal("结束时间早于开始时间应被拒绝")
	}
}

func TestMapTaskHandlesNil(t *testing.T) {
	task, err := mapTask(nil, errors.New("boom"))
	if task != nil || err == nil {
		t.Fatalf("nil 记录应返回 nil 任务与错误: %+v %v", task, err)
	}
}
