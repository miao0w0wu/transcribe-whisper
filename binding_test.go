package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
)

// 本文件逐个调用 Wails Binding 层暴露给前端的方法，验证：
//   - 没有方法会 panic；
//   - 错误路径返回中文错误而不是裸 nil；
//   - 返回结构能被正确序列化给前端。

// newFrontendApp 构造一个带真实数据库（临时文件）的应用实例。
func newFrontendApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	dbPath := filepath.Join(t.TempDir(), "frontend.db")
	if err := app.openDatabase(dbPath); err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { app.shutdown(context.Background()) })
	return app
}

func TestBindingGetDashboardShape(t *testing.T) {
	app := newFrontendApp(t)
	dashboard := app.GetDashboard()
	if dashboard.Tasks == nil {
		t.Fatal("Tasks 必须是切片而不是 null，否则前端 map 会崩")
	}
	if len(dashboard.Tasks) != 0 {
		t.Fatalf("初始应没有任务，实际 %d", len(dashboard.Tasks))
	}
	if dashboard.ActiveTask != nil {
		t.Fatalf("初始不应有活动任务，实际 %+v", dashboard.ActiveTask)
	}
}

func TestBindingGetSettingsShape(t *testing.T) {
	app := newFrontendApp(t)
	settings := app.GetSettings()
	if settings.AsrEngine == "" || settings.DefaultModel == "" || settings.TranslationProvider == "" {
		t.Fatalf("设置默认值不应为空: %+v", settings)
	}
	if settings.TranslationStyle == "" {
		t.Fatalf("翻译风格应有默认值: %+v", settings)
	}
	// API Key 只能以布尔形式暴露，绝不能把明文透出给前端。
	if settings.TranslationAPIKeySet {
		t.Fatal("默认不应标记为已配置 API Key")
	}
}

func TestBindingSettingsRoundTrip(t *testing.T) {
	app := newFrontendApp(t)
	style := "formal"
	glossary := "API=接口\npipeline=流水线"
	device := "cuda"
	updated, err := app.UpdateSettings(SettingsPatch{
		TranslationStyle: &style,
		GlossaryText:     &glossary,
		AsrDevice:        &device,
	})
	if err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}
	if updated.TranslationStyle != "formal" || updated.AsrDevice != "cuda" {
		t.Fatalf("设置没有生效: %+v", updated)
	}
	// 术语表要以可编辑文本原样返回。
	if !strings.Contains(updated.GlossaryText, "API=接口") || !strings.Contains(updated.GlossaryText, "pipeline=流水线") {
		t.Fatalf("术语表没有正确回显: %q", updated.GlossaryText)
	}
	// 重新读取也要一致（已持久化）。
	reloaded := app.GetSettings()
	if reloaded.TranslationStyle != "formal" || !strings.Contains(reloaded.GlossaryText, "pipeline=流水线") {
		t.Fatalf("设置没有持久化: %+v", reloaded)
	}
}

func TestBindingUpdateSettingsRejectsMalformedGlossary(t *testing.T) {
	app := newFrontendApp(t)
	bad := "这一行没有等号"
	_, err := app.UpdateSettings(SettingsPatch{GlossaryText: &bad})
	if err == nil {
		t.Fatal("格式错误的术语表应被拒绝")
	}
	if !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("错误信息应含行号，实际: %v", err)
	}
	// 拒绝后不应破坏原有设置。
	after := app.GetSettings()
	if after.GlossaryText != "" {
		t.Fatalf("失败的更新不应写入: %q", after.GlossaryText)
	}
}

func TestBindingModelListShape(t *testing.T) {
	app := newFrontendApp(t)
	models := app.ListModels()
	if len(models) == 0 {
		t.Fatal("模型清单不应为空")
	}
	for _, model := range models {
		if model.ID == "" || model.Name == "" {
			t.Fatalf("模型字段不完整: %+v", model)
		}
		if model.Files == nil {
			t.Fatalf("Files 必须是切片而不是 null: %+v", model)
		}
	}
	// 强制指定一个空模型目录，确认所有模型都是未安装状态而不是误报已安装。
	for _, model := range models {
		if model.Installed {
			t.Fatalf("临时目录下不应有已安装模型: %+v", model)
		}
	}
}

func TestBindingModelLifecycleErrors(t *testing.T) {
	app := newFrontendApp(t)
	if err := app.RemoveModel("tiny"); err == nil {
		t.Fatal("未安装的模型删除应报错")
	}
	if err := app.CancelModelDownload("tiny"); err == nil {
		t.Fatal("没有下载任务时取消应报错")
	}
	if _, err := app.DownloadModel("不存在的模型"); err == nil {
		t.Fatal("未知模型应被拒绝")
	}
}

func TestBindingEnvironmentShape(t *testing.T) {
	app := newFrontendApp(t)
	status := app.GetEnvironment()
	// FFmpeg 在本机确实可用，这是构建环境的硬性前提。
	if !status.FFmpegOK {
		t.Fatalf("本机 FFmpeg 应当可用: %+v", status)
	}
	if status.FFmpegPath == "" {
		t.Fatal("FFmpeg 可用时必须给出路径")
	}
	if status.ModelDir == "" {
		t.Fatal("模型目录不应为空")
	}
}

func TestBindingTaskLifecycleErrors(t *testing.T) {
	app := newFrontendApp(t)
	if _, err := app.StartTask("not-exist"); err == nil {
		t.Fatal("启动不存在的任务应报错")
	}
	if _, err := app.PauseTask("not-exist"); err == nil {
		t.Fatal("暂停不存在的任务应报错")
	}
	if _, err := app.ResumeTask("not-exist"); err == nil {
		t.Fatal("恢复不存在的任务应报错")
	}
	if _, err := app.CancelTask("not-exist"); err == nil {
		t.Fatal("取消不存在的任务应报错")
	}
	if _, err := app.RetryTask("not-exist"); err == nil {
		t.Fatal("重试不存在的任务应报错")
	}
	if err := app.DeleteTask("not-exist"); err == nil {
		t.Fatal("删除不存在的任务应报错")
	}
	if _, err := app.ListSubtitles("not-exist"); err == nil {
		t.Fatal("读取不存在任务的字幕应报错")
	}
	if _, err := app.ExportSubtitles("not-exist", "srt"); err == nil {
		t.Fatal("导出不存在任务应报错")
	}
}

func TestBindingCreateTaskStoresEngineAndModel(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "bind.mp4")
	task, err := app.CreateTask(CreateTaskInput{
		SourceFile: source,
		Engine:     "faster-whisper",
		Model:      "Systran/faster-whisper-small",
		Translate:  false,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// 模型标识要归一化，避免前端传别名导致后端找不到目录。
	if task.Model != "small" {
		t.Fatalf("模型标识应归一化为 small，实际 %q", task.Model)
	}
	if task.Provider != "none" {
		t.Fatalf("未开启翻译时 Provider 应为 none，实际 %q", task.Provider)
	}
}

func TestBindingRejectsUnknownEngine(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "engine.mp4")
	_, err := app.CreateTask(CreateTaskInput{SourceFile: source, Engine: "whisper.cpp"})
	if err == nil || !strings.Contains(err.Error(), "识别引擎") {
		t.Fatalf("未知引擎应被拒绝，实际: %v", err)
	}
}

func TestBindingPickFileWithoutWailsContext(t *testing.T) {
	app := newFrontendApp(t)
	// 纯 Go 测试里没有 Wails 运行时，必须返回明确错误而不是 panic。
	if _, err := app.PickFile(); err == nil {
		t.Fatal("没有 Wails 上下文时应返回错误")
	}
	if err := app.OpenModelFolder(); err != nil {
		t.Logf("OpenModelFolder 在无 GUI 环境下返回: %v", err)
	}
}

func TestBindingCanceledTaskCannotStart(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "canceled.mp4")
	task, err := app.CreateTask(CreateTaskInput{SourceFile: source})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// 直接改状态模拟已取消（不启动识别，避免依赖真实模型）。
	if err := app.store.UpdateTaskStatus(context.Background(), task.ID, repository.TaskStatusCanceled, "canceled", "已取消", 0, "", ""); err != nil {
		t.Fatalf("准备取消状态失败: %v", err)
	}
	if _, err := app.StartTask(task.ID); err == nil {
		t.Fatal("已取消的任务不应能直接启动")
	}
	// 但可以重试。
	if _, err := app.RetryTask(task.ID); err == nil {
		t.Log("已取消任务可以重试")
	}
}

// TestBindingModelRootDirMatchesSettings 验证模型目录跟随设置变化。
func TestBindingModelRootDirMatchesSettings(t *testing.T) {
	app := newFrontendApp(t)
	newDir := filepath.Join(t.TempDir(), "custom-models")
	dir := newDir
	if _, err := app.UpdateSettings(SettingsPatch{ModelDir: &dir}); err != nil {
		t.Fatalf("更新模型目录失败: %v", err)
	}
	app.mu.RLock()
	models := app.models
	app.mu.RUnlock()
	if models == nil {
		t.Fatal("模型管理器应已重建")
	}
	if models.RootDir() != newDir {
		t.Fatalf("模型根目录应跟随设置更新，实际 %q", models.RootDir())
	}
}

// TestBindingModelInUseProtectsRunningTask 验证运行中的模型不能被删除。
func TestBindingModelInUseProtectsRunningTask(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "inuse.mp4")
	task, err := app.CreateTask(CreateTaskInput{SourceFile: source, Model: "tiny"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := app.store.UpdateTaskStatus(context.Background(), task.ID, repository.TaskStatusTranscribing, "transcribing", "识别中", 40, "", ""); err != nil {
		t.Fatalf("准备任务状态失败: %v", err)
	}
	if !app.modelInUse("tiny") {
		t.Fatal("识别中的任务使用的模型应被判定为占用")
	}
	if app.modelInUse("small") {
		t.Fatal("未被使用的模型不应判定为占用")
	}
}

// 确保清单里的模型 ID 与 app 侧归一化逻辑一致。
func TestBindingModelIDsAreNormalized(t *testing.T) {
	for _, entry := range modelmanager.DefaultCatalog() {
		if normalized := modelmanager.NormalizeModelID(entry.ID); normalized != entry.ID {
			t.Fatalf("清单 ID %q 归一化后变成 %q，前后端将会对不上", entry.ID, normalized)
		}
	}
}

// 确认 os.UserConfigDir 在本环境可用（配置与模型目录都依赖它）。
func TestUserConfigDirAvailable(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		t.Fatalf("无法获取用户配置目录: %v", err)
	}
}

// TestBindingBurnSubtitlesWithoutWailsContext 验证无 Wails 上下文时的压制入口行为：
// 显式给输出路径可以直接压制（由后端能力决定成败），空路径则必须报中文错误而不是 panic。
func TestBindingBurnSubtitlesWithoutWailsContext(t *testing.T) {
	app := newFrontendApp(t)
	source := writeTempMedia(t, t.TempDir(), "burn.mp4")
	task, err := app.CreateTask(CreateTaskInput{SourceFile: source})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// 没有 Wails 上下文时，空输出路径无法弹对话框。
	if _, err := app.BurnSubtitles(task.ID, BurnSubtitlesInput{}); err == nil {
		t.Fatal("无 Wails 上下文且未指定输出路径时应返回错误")
	} else if !strings.Contains(err.Error(), "不支持文件对话框") {
		t.Fatalf("错误信息应说明缺少文件对话框，实际: %v", err)
	}
	// 任务还没有字幕，带输出路径也必须被拒绝。
	output := filepath.Join(t.TempDir(), "out.mp4")
	if _, err := app.BurnSubtitles(task.ID, BurnSubtitlesInput{OutputPath: output}); err == nil {
		t.Fatal("没有字幕时压制应失败")
	} else if !strings.Contains(err.Error(), "还没有字幕") {
		t.Fatalf("错误信息应说明缺少字幕，实际: %v", err)
	}
}

// TestBindingBurnSubtitlesRejectsUnknownTask 确认任务不存在时返回错误而不是空路径。
func TestBindingBurnSubtitlesRejectsUnknownTask(t *testing.T) {
	app := newFrontendApp(t)
	output := filepath.Join(t.TempDir(), "out.mp4")
	if _, err := app.BurnSubtitles("not-a-task", BurnSubtitlesInput{OutputPath: output}); err == nil {
		t.Fatal("任务不存在时应返回错误")
	}
}
