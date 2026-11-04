package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/service"
)

// supportedEngines 与 supportedProviders 是合法取值集合。
var (
	supportedEngines    = map[string]bool{"faster-whisper": true, "funasr": true}
	supportedProviders  = map[string]bool{"ollama": true, "openai": true, "none": true}
	supportedAsrDevices = map[string]bool{"auto": true, "cpu": true, "cuda": true}
)

// GetDashboard 返回全部任务与当前活动任务。
func (a *App) GetDashboard() Dashboard {
	store, err := a.storeHandle()
	if err != nil {
		return Dashboard{Tasks: []*Task{}}
	}
	records, err := store.ListTasks(a.contextOrBackground())
	if err != nil {
		return Dashboard{Tasks: []*Task{}}
	}
	tasks := make([]*Task, 0, len(records))
	var active *Task
	for _, rec := range records {
		task := toPublicTask(rec)
		tasks = append(tasks, task)
		if active == nil && !isFinished(rec.Status) {
			active = task
		}
	}
	if active == nil && len(tasks) > 0 {
		active = tasks[0]
	}
	return Dashboard{ActiveTask: active, Tasks: tasks}
}

// CreateTask 校验入参并写入数据库。
func (a *App) CreateTask(input CreateTaskInput) (*Task, error) {
	store, err := a.storeHandle()
	if err != nil {
		return nil, err
	}
	sourceFile := strings.TrimSpace(input.SourceFile)
	if sourceFile == "" {
		return nil, errors.New("请先选择音视频文件")
	}
	if info, statErr := os.Stat(sourceFile); statErr != nil || info.IsDir() {
		return nil, errors.New("源文件不存在或不可读：" + sourceFile)
	}

	name := filepath.Base(sourceFile)
	if name == "." || name == string(filepath.Separator) {
		return nil, errors.New("源文件名无效")
	}

	sourceLanguage := strings.TrimSpace(input.SourceLanguage)
	if sourceLanguage == "" {
		sourceLanguage = "auto"
	}
	targetLanguage := strings.TrimSpace(input.TargetLanguage)
	if targetLanguage == "" {
		targetLanguage = "zh-CN"
	}
	settings := a.currentSettings()

	engine := strings.TrimSpace(input.Engine)
	if engine == "" {
		engine = settings.AsrEngine
	}
	if engine == "" {
		engine = "faster-whisper"
	}
	if !supportedEngines[engine] {
		return nil, errors.New("不支持的识别引擎：" + engine)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = settings.DefaultModel
	}
	if model == "" {
		model = "small"
	}
	if normalized := modelmanager.NormalizeModelID(model); normalized != "" {
		model = normalized
	}

	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = settings.TranslationProvider
	}
	if provider == "" {
		provider = "ollama"
	}
	if !supportedProviders[provider] {
		return nil, errors.New("不支持的翻译服务：" + provider)
	}
	if !input.Translate {
		provider = "none"
	}
	if provider != "none" && provider != "ollama" && !settings.CloudPrivacyConfirm {
		return nil, errors.New("选择云端翻译前，请先在设置中确认隐私提示")
	}

	outputFormat := strings.TrimSpace(strings.ToLower(input.OutputFormat))
	if outputFormat == "" {
		outputFormat = "srt"
	}

	now := time.Now().UTC()
	rec := &repository.TaskRecord{
		ID:             uuid.NewString(),
		Name:           strings.TrimSuffix(name, filepath.Ext(name)),
		SourceFile:     sourceFile,
		SourceLanguage: sourceLanguage,
		TargetLanguage: targetLanguage,
		Status:         repository.TaskStatusCreated,
		Stage:          "created",
		Progress:       0,
		CurrentStage:   "等待开始",
		Message:        "",
		Engine:         engine,
		Model:          model,
		Provider:       provider,
		OutputFormat:   outputFormat,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateTask(a.contextOrBackground(), rec); err != nil {
		return nil, err
	}
	return toPublicTask(rec), nil
}

// StartTask 把任务加入队列开始执行。
func (a *App) StartTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Enqueue(a.contextOrBackground(), strings.TrimSpace(taskID))
	if rec != nil && err != nil {
		// 状态说明类错误也返回当前任务，前端可刷新界面。
		return toPublicTask(rec), err
	}
	if err != nil {
		return nil, err
	}
	return toPublicTask(rec), nil
}

// PauseTask 暂停任务。
func (a *App) PauseTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Pause(a.contextOrBackground(), strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// ResumeTask 恢复暂停的任务。
func (a *App) ResumeTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Resume(a.contextOrBackground(), strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// CancelTask 取消任务。
func (a *App) CancelTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Cancel(a.contextOrBackground(), strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// RetryTask 重试失败或取消的任务。
func (a *App) RetryTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Retry(a.contextOrBackground(), strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// RetranslateTask 重新翻译已完成任务。
func (a *App) RetranslateTask(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	rec, err := queue.Retranslate(a.contextOrBackground(), strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// DeleteTask 删除任务及其字幕。
func (a *App) DeleteTask(taskID string) error {
	queue, err := a.queueHandle()
	if err != nil {
		return err
	}
	return queue.Delete(a.contextOrBackground(), strings.TrimSpace(taskID))
}

// ListSubtitles 查询任务字幕。
func (a *App) ListSubtitles(taskID string) ([]SubtitleSegment, error) {
	store, err := a.storeHandle()
	if err != nil {
		return nil, err
	}
	taskID = strings.TrimSpace(taskID)
	if _, err := store.GetTask(a.contextOrBackground(), taskID); err != nil {
		return nil, errors.New("任务不存在")
	}
	records, err := store.ListSegments(a.contextOrBackground(), taskID)
	if err != nil {
		return nil, err
	}
	segments := make([]SubtitleSegment, 0, len(records))
	for _, rec := range records {
		segments = append(segments, SubtitleSegment{
			ID:         rec.ID,
			Sequence:   rec.Sequence,
			Start:      rec.Start,
			End:        rec.End,
			SourceText: rec.SourceText,
			Translated: rec.Translated,
			Status:     rec.Status,
		})
	}
	return segments, nil
}

// UpdateSubtitles 保存字幕编辑结果。
func (a *App) UpdateSubtitles(taskID string, segments []SubtitleSegment) error {
	store, err := a.storeHandle()
	if err != nil {
		return err
	}
	taskID = strings.TrimSpace(taskID)
	if _, err := store.GetTask(a.contextOrBackground(), taskID); err != nil {
		return errors.New("任务不存在")
	}
	records := make([]repository.SegmentRecord, 0, len(segments))
	for _, segment := range segments {
		status := strings.TrimSpace(segment.Status)
		if status == "" {
			if strings.TrimSpace(segment.Translated) == "" {
				status = repository.SegmentStatusPending
			} else {
				status = repository.SegmentStatusTranslated
			}
		}
		records = append(records, repository.SegmentRecord{
			ID:         segment.ID,
			TaskID:     taskID,
			Sequence:   segment.Sequence,
			Start:      segment.Start,
			End:        segment.End,
			SourceText: segment.SourceText,
			Translated: segment.Translated,
			Status:     status,
		})
	}
	if err := store.ReplaceSegments(a.contextOrBackground(), taskID, records); err != nil {
		return err
	}
	translated := 0
	for _, rec := range records {
		if strings.TrimSpace(rec.Translated) != "" {
			translated++
		}
	}
	task, err := store.GetTask(a.contextOrBackground(), taskID)
	if err != nil {
		return err
	}
	return store.UpdateTaskCounts(a.contextOrBackground(), taskID, len(records), translated, task.Duration)
}

// TranslatePending 翻译当前任务里待翻译的字幕。
func (a *App) TranslatePending(taskID string) (*Task, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return nil, err
	}
	ctx := a.contextOrBackground()
	if _, err := queue.TranslatePending(ctx, strings.TrimSpace(taskID), nil); err != nil {
		return nil, err
	}
	store, err := a.storeHandle()
	if err != nil {
		return nil, err
	}
	rec, err := store.GetTask(ctx, strings.TrimSpace(taskID))
	return mapTask(rec, err)
}

// ExportSubtitles 返回指定格式的字幕文本。
func (a *App) ExportSubtitles(taskID string, format string) (string, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return "", err
	}
	content, _, err := queue.ExportContent(a.contextOrBackground(), strings.TrimSpace(taskID), format)
	return content, err
}

// SaveExport 弹窗选择保存位置并写入字幕文件。
func (a *App) SaveExport(taskID string, format string) (string, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return "", err
	}
	content, filename, err := queue.ExportContent(a.contextOrBackground(), strings.TrimSpace(taskID), format)
	if err != nil {
		return "", err
	}
	if a.ctx == nil {
		return "", errors.New("当前环境不支持文件对话框")
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "保存字幕",
		DefaultFilename: filename,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", errors.New("写入字幕文件失败：" + err.Error())
	}
	return path, nil
}

// PickFile 打开系统文件选择框，返回用户选中的音视频绝对路径。
func (a *App) PickFile() (string, error) {
	if a.ctx == nil {
		return "", errors.New("当前环境不支持文件对话框，请直接填写本机绝对路径")
	}
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择音视频文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "音视频文件", Pattern: "*.mp4;*.mkv;*.avi;*.mov;*.webm;*.flv;*.ts;*.m4v;*.mp3;*.wav;*.m4a;*.flac;*.aac;*.ogg"},
			{DisplayName: "全部文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

// ListModels 返回内置模型清单与本地安装状态。
func (a *App) ListModels() []ModelInfo {
	a.mu.RLock()
	models := a.models
	a.mu.RUnlock()
	if models == nil {
		return []ModelInfo{}
	}
	ctx := a.contextOrBackground()
	installed, err := models.Installed(ctx)
	if err != nil {
		installed = map[string]string{}
	}
	catalog := models.Catalog()
	result := make([]ModelInfo, 0, len(catalog))
	for _, entry := range catalog {
		localPath, ok := installed[entry.ID]
		info := ModelInfo{
			ID:          entry.ID,
			Name:        entry.Name,
			Repo:        entry.Repo,
			SizeBytes:   entry.SizeBytes,
			Installed:   ok,
			LocalPath:   localPath,
			Description: entry.Description,
			Files:       requiredFiles(entry),
		}
		if models.IsDownloading(entry.ID) {
			info.Installing = true
			info.Progress = models.Progress(entry.ID)
		}
		result = append(result, info)
	}
	return result
}

// DownloadModel 下载模型并推送进度事件。
func (a *App) DownloadModel(modelID string) (*ModelInfo, error) {
	a.mu.RLock()
	models := a.models
	a.mu.RUnlock()
	if models == nil {
		return nil, errors.New("模型管理器未就绪")
	}
	normalized := modelmanager.NormalizeModelID(strings.TrimSpace(modelID))
	if normalized == "" {
		return nil, errors.New("模型标识无效")
	}
	// 只允许下载内置清单里的模型，避免任意仓库名被直接拼进下载 URL。
	if _, ok := models.Find(normalized); !ok {
		return nil, errors.New("未知的模型：" + modelID + "，可在模型管理页查看可用模型")
	}
	if models.IsDownloading(normalized) {
		return nil, errors.New("该模型正在下载中，请等待完成或先取消")
	}
	ctx := a.contextOrBackground()
	go func() {
		_, err := models.Ensure(context.WithoutCancel(ctx), normalized, func(ratio float64, message string) {
			a.emitModel(ModelProgressEventPayload{
				ModelID:  normalized,
				Progress: int(ratio * 100),
				Message:  message,
			})
		})
		event := ModelProgressEventPayload{ModelID: normalized, Progress: 100, Done: true}
		if err != nil {
			event.Error = err.Error()
			event.Progress = models.Progress(normalized)
		}
		a.emitModel(event)
	}()
	return &ModelInfo{ID: normalized, Installing: true}, nil
}

// ModelProgressEventPayload 是模型下载进度事件结构。
type ModelProgressEventPayload struct {
	ModelID  string `json:"modelId"`
	Progress int    `json:"progress"`
	Message  string `json:"message"`
	Done     bool   `json:"done"`
	Error    string `json:"error"`
}

// CancelModelDownload 取消模型下载。
func (a *App) CancelModelDownload(modelID string) error {
	a.mu.RLock()
	models := a.models
	a.mu.RUnlock()
	if models == nil {
		return errors.New("模型管理器未就绪")
	}
	if !models.Cancel(modelmanager.NormalizeModelID(strings.TrimSpace(modelID))) {
		return errors.New("该模型当前没有进行中的下载")
	}
	return nil
}

// RemoveModel 删除本地模型。
func (a *App) RemoveModel(modelID string) error {
	a.mu.RLock()
	models := a.models
	a.mu.RUnlock()
	if models == nil {
		return errors.New("模型管理器未就绪")
	}
	return models.Remove(a.contextOrBackground(), modelmanager.NormalizeModelID(strings.TrimSpace(modelID)))
}

// OpenModelFolder 在文件管理器中打开模型缓存目录。
func (a *App) OpenModelFolder() error {
	a.mu.RLock()
	models := a.models
	a.mu.RUnlock()
	if models == nil {
		return errors.New("模型管理器未就绪")
	}
	dir := models.RootDir()
	if strings.TrimSpace(dir) == "" {
		return errors.New("模型目录未配置")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.New("创建模型目录失败：" + err.Error())
	}
	if a.ctx != nil {
		wailsruntime.BrowserOpenURL(a.ctx, "file:///"+filepath.ToSlash(dir))
		return nil
	}
	return openPath(dir)
}

// GetSettings 返回设置快照（不含 API Key 明文）。
func (a *App) GetSettings() SettingsSnapshot {
	settings := a.currentSettings()
	mirror := settings.Mirror
	if mirror == "" {
		mirror = "hf-mirror"
	}
	return SettingsSnapshot{
		FFmpegPath:           settings.FFmpegPath,
		PythonPath:           settings.PythonPath,
		ModelDir:             settings.ModelDir,
		Mirror:               mirror,
		HuggingFaceEndpoint:  settings.HuggingFaceEndpoint,
		AsrEngine:            settings.AsrEngine,
		AsrDevice:            settings.AsrDevice,
		ComputeType:          settings.ComputeType,
		DefaultModel:         settings.DefaultModel,
		TranslationProvider:  settings.TranslationProvider,
		TranslationEndpoint:  settings.TranslationEndpoint,
		TranslationModel:     settings.TranslationModel,
		TranslationAPIKeySet: strings.TrimSpace(settings.TranslationAPIKey) != "",
		CloudPrivacyConfirm:  settings.CloudPrivacyConfirm,
		TranslationStyle:     defaultStyle(settings.TranslationStyle),
		GlossaryText:         formatGlossary(settings.Glossary),
	}
}

// UpdateSettings 部分更新设置。
func (a *App) UpdateSettings(patch SettingsPatch) (*SettingsSnapshot, error) {
	store, err := a.storeHandle()
	if err != nil {
		return nil, err
	}
	ctx := a.contextOrBackground()
	current, err := store.LoadSettings(ctx)
	if err != nil {
		return nil, err
	}
	apply := func(target *string, value *string) {
		if value != nil {
			*target = strings.TrimSpace(*value)
		}
	}
	apply(&current.FFmpegPath, patch.FFmpegPath)
	apply(&current.PythonPath, patch.PythonPath)
	apply(&current.ModelDir, patch.ModelDir)
	apply(&current.Mirror, patch.Mirror)
	apply(&current.HuggingFaceEndpoint, patch.HuggingFaceEndpoint)
	apply(&current.AsrEngine, patch.AsrEngine)
	apply(&current.AsrDevice, patch.AsrDevice)
	apply(&current.ComputeType, patch.ComputeType)
	apply(&current.DefaultModel, patch.DefaultModel)
	apply(&current.TranslationProvider, patch.TranslationProvider)
	apply(&current.TranslationEndpoint, patch.TranslationEndpoint)
	apply(&current.TranslationModel, patch.TranslationModel)
	if patch.TranslationAPIKey != nil {
		current.TranslationAPIKey = strings.TrimSpace(*patch.TranslationAPIKey)
	}
	if patch.CloudPrivacyConfirm != nil {
		current.CloudPrivacyConfirm = *patch.CloudPrivacyConfirm
	}
	if patch.TranslationStyle != nil {
		current.TranslationStyle = defaultStyle(*patch.TranslationStyle)
	}
	if patch.GlossaryText != nil {
		glossary, err := parseGlossary(*patch.GlossaryText)
		if err != nil {
			return nil, err
		}
		current.Glossary = glossary
	}

	if current.AsrEngine != "" && !supportedEngines[current.AsrEngine] {
		return nil, errors.New("不支持的识别引擎：" + current.AsrEngine)
	}
	if current.AsrDevice != "" && !supportedAsrDevices[current.AsrDevice] {
		return nil, errors.New("不支持的运行设备：" + current.AsrDevice)
	}
	if current.TranslationProvider != "" && !supportedProviders[current.TranslationProvider] {
		return nil, errors.New("不支持的翻译服务：" + current.TranslationProvider)
	}
	if current.DefaultModel != "" {
		current.DefaultModel = modelmanager.NormalizeModelID(current.DefaultModel)
	}

	if err := store.SaveSettings(ctx, current); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.settings = current
	a.mu.Unlock()

	// 设置变化后重建模型管理器，使镜像与目录立即生效。
	if models := a.models; models != nil {
		newRoot := strings.TrimSpace(current.ModelDir)
		if newRoot != "" && newRoot != models.RootDir() {
			a.mu.Lock()
			a.models = modelmanager.New(modelmanager.Options{
				RootDir:   newRoot,
				Mirrors:   mirrorsFor(current.Mirror, current.HuggingFaceEndpoint),
				InUse:     a.modelInUse,
				Catalog:   modelmanager.DefaultCatalog(),
				UserAgent: "transcribe-whisper/1.0",
			})
			a.mu.Unlock()
		}
	}

	snapshot := a.GetSettings()
	return &snapshot, nil
}

// TestTranslationProvider 检查翻译服务是否可用。
func (a *App) TestTranslationProvider() (string, error) {
	settings := a.currentSettings()
	kind := settings.TranslationProvider
	if kind == "" {
		kind = "ollama"
	}
	if !isLocalProviderKind(kind) && !settings.CloudPrivacyConfirm {
		return "", errors.New("云端翻译需要先在设置中确认隐私提示")
	}
	client, err := newTranslationClient(settings)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), 20*time.Second)
	defer cancel()
	if err := pingProvider(ctx, client); err != nil {
		return "", err
	}
	return "翻译服务连接正常（" + kind + " · " + settings.TranslationModel + "）", nil
}

// GetEnvironment 探测本机运行环境。
func (a *App) GetEnvironment() EnvironmentStatus {
	settings := a.currentSettings()
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), 30*time.Second)
	defer cancel()

	status := EnvironmentStatus{ModelDir: settings.ModelDir, GPU: detectGPU()}

	ffmpegPath := settings.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	a.mu.RLock()
	ffmpeg := a.media
	a.mu.RUnlock()
	if ffmpeg != nil {
		caps := ffmpeg.Available(ctx)
		status.FFmpegOK = caps.OK
		status.FFmpegPath = caps.Path
		status.FFmpegDetail = strings.TrimSpace(caps.Version + " " + caps.Detail)
		status.FfmpegSubtitle = caps.SupportsSubtitlesBurn
	} else {
		status.FFmpegDetail = "未找到 FFmpeg，请安装后重启应用或在设置中指定路径"
	}

	pythonPath := settings.PythonPath
	if pythonPath == "" {
		pythonPath = "python"
	}
	if resolved, ok := ensureExecutable(pythonPath); ok {
		status.PythonPath = resolved
		status.PythonOK = true
	} else {
		status.PythonOK = false
		status.PythonDetail = "未找到 Python，请安装 3.9 以上版本并安装 faster-whisper"
	}

	a.mu.RLock()
	engine := a.engine
	a.mu.RUnlock()
	if engine != nil {
		ok, detail := engine.Available(ctx)
		status.FasterWhisper = ok
		if !ok {
			if status.PythonOK {
				status.PythonDetail = detail
			} else {
				status.PythonDetail = strings.TrimSpace(status.PythonDetail + " " + detail)
			}
		} else if status.PythonDetail == "" {
			status.PythonDetail = detail
		}
	}
	if status.FFmpegPath == "" && status.FFmpegOK {
		if resolved, ok := ensureExecutable("ffmpeg"); ok {
			status.FFmpegPath = resolved
		}
	}
	return status
}

// BurnSubtitles 把任务字幕压制进源视频，返回产物绝对路径。
//
// 未指定 OutputPath 时弹系统「另存为」对话框；用户取消则返回空路径与 nil 错误。
func (a *App) BurnSubtitles(taskID string, input BurnSubtitlesInput) (string, error) {
	queue, err := a.queueHandle()
	if err != nil {
		return "", err
	}
	taskID = strings.TrimSpace(taskID)
	outputPath := strings.TrimSpace(input.OutputPath)
	if outputPath == "" {
		if a.ctx == nil {
			return "", errors.New("当前环境不支持文件对话框，请显式填写输出路径")
		}
		store, storeErr := a.storeHandle()
		if storeErr != nil {
			return "", storeErr
		}
		rec, recErr := store.GetTask(a.contextOrBackground(), taskID)
		if recErr != nil {
			return "", recErr
		}
		base := strings.TrimSpace(rec.Name)
		if base == "" {
			base = "subtitled"
		}
		// 输出必须是 mp4 容器：burnArgs 用的 movflags=+faststart 只对 mp4/mov 生效。
		dialogPath, dialogErr := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
			Title:           "保存压制后的视频",
			DefaultFilename: base + "-subtitled.mp4",
			Filters: []wailsruntime.FileFilter{
				{DisplayName: "MP4 视频", Pattern: "*.mp4"},
			},
		})
		if dialogErr != nil {
			return "", dialogErr
		}
		outputPath = strings.TrimSpace(dialogPath)
		if outputPath == "" {
			return "", nil
		}
	}
	return queue.BurnSubtitles(a.contextOrBackground(), taskID, outputPath, service.BurnOptions{
		FontName: strings.TrimSpace(input.FontName),
		FontSize: input.FontSize,
		MarginV:  input.MarginV,
	})
}

// detectGPU 读取显卡型号。
func detectGPU() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	cmd := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	return line
}

// openPath 用系统默认方式打开目录。
func openPath(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// isFinished 判断任务是否结束。
func isFinished(status string) bool {
	switch status {
	case repository.TaskStatusCompleted, repository.TaskStatusFailed, repository.TaskStatusCanceled:
		return true
	}
	return false
}

// toPublicTask 把仓库记录映射成前端结构。
func toPublicTask(rec *repository.TaskRecord) *Task {
	if rec == nil {
		return nil
	}
	return &Task{
		ID:              rec.ID,
		Name:            rec.Name,
		SourceFile:      rec.SourceFile,
		SourceLanguage:  rec.SourceLanguage,
		TargetLanguage:  rec.TargetLanguage,
		Status:          rec.Status,
		Stage:           rec.Stage,
		Progress:        rec.Progress,
		CurrentStage:    rec.CurrentStage,
		SubtitleCount:   rec.SubtitleCount,
		TranslatedCount: rec.TranslatedCount,
		Message:         rec.Message,
		Error:           rec.Error,
		Engine:          rec.Engine,
		Model:           rec.Model,
		Provider:        rec.Provider,
		OutputFormat:    rec.OutputFormat,
		Duration:        rec.Duration,
		CreatedAt:       rec.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       rec.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// mapTask 统一处理 (记录, 错误) 到 (公共结构, 错误) 的转换。
func mapTask(rec *repository.TaskRecord, err error) (*Task, error) {
	return toPublicTask(rec), err
}

// requiredFiles 返回模型必需文件名列表。
func requiredFiles(entry modelmanager.CatalogEntry) []string {
	if len(entry.Required) > 0 {
		return entry.Required
	}
	files := make([]string, 0, len(entry.Files))
	for _, file := range entry.Files {
		files = append(files, file.Path)
	}
	return files
}
