package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/subtitleclean"
	"transcribe-whisper/internal/translation"
)

// execute 执行一个任务的完整流水线：提取音频 → 识别 → 翻译。
func (q *Queue) execute(tc *taskContext) {
	defer q.finish(tc)

	taskID := tc.taskID
	rec, err := q.deps.Store.GetTask(context.WithoutCancel(tc.ctx), taskID)
	if err != nil {
		q.markFailed(tc, fmt.Errorf("读取任务失败: %w", err))
		return
	}

	settings, err := q.deps.Settings(tc.ctx)
	if err != nil {
		q.markFailed(tc, fmt.Errorf("读取应用设置失败: %w", err))
		return
	}

	// 阶段一：探测媒体信息。
	info, probeErr := q.probe(tc.ctx, rec.SourceFile)
	if err := tc.check(); err != nil {
		q.finishAborted(tc, err, rec.Progress)
		return
	}
	if probeErr == nil && info.Duration > 0 {
		rec.Duration = info.Duration
		_ = q.deps.Store.UpdateTaskCounts(tc.ctx, taskID, rec.SubtitleCount, rec.TranslatedCount, rec.Duration)
	}

	// 阶段二：提取音频。
	audioPath, err := q.extractAudio(tc, rec, settings)
	if err != nil {
		if aborted := tc.check(); aborted != nil {
			q.finishAborted(tc, aborted, rec.Progress)
			return
		}
		q.markFailed(tc, err)
		return
	}
	defer func() {
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
	}()

	// 阶段二点五：降噪增强。失败时降级用原始音频继续，不让整条任务失败。
	enhancedPath, cleanupEnhanced := q.enhanceAudio(tc, rec, settings, audioPath)
	if cleanupEnhanced != nil {
		defer cleanupEnhanced()
	}
	if enhancedPath != "" {
		audioPath = enhancedPath
	}

	// 阶段三：语音识别。
	segments, lang, err := q.transcribeStage(tc, rec, settings, audioPath)
	if err != nil {
		if aborted := tc.check(); aborted != nil {
			q.finishAborted(tc, aborted, rec.Progress)
			return
		}
		q.markFailed(tc, err)
		return
	}
	if len(segments) == 0 {
		q.markFailed(tc, errors.New("识别结果为空：音频中可能没有人声，或语言设置不匹配"))
		return
	}

	ctx := tc.ctx
	sourceLang := rec.SourceLanguage
	if lang != "" && (sourceLang == "" || sourceLang == "auto") {
		sourceLang = lang
	}
	_ = q.deps.Store.UpdateTaskStatus(ctx, taskID, repository.TaskStatusTranscribing, StageTranscribing, "识别完成", progressAsrEnd,
		fmt.Sprintf("识别完成，共 %d 条字幕", len(segments)), "")

	// 阶段四：翻译（创建时选择不翻译则直接完成）。
	translatedCount := 0
	if rec.Provider != "" && rec.Provider != "none" && settings.TranslationProvider != "none" {
		done, err := q.translateStage(ctx, taskID, sourceLang, nil)
		if err != nil {
			if aborted := tc.check(); aborted != nil {
				q.finishAborted(tc, aborted, rec.Progress)
				return
			}
			// 翻译失败不丢弃识别结果：任务标记为完成但附带错误提示，便于重试翻译。
			message := fmt.Sprintf("识别已完成，翻译失败：%s", err.Error())
			if markErr := q.updateStatus(context.WithoutCancel(ctx), taskID, repository.TaskStatusCompleted, StageTranslated,
				"翻译未完成", progressTranslateEnd, message, message); markErr != nil {
				q.markFailed(tc, markErr)
			}
			return
		}
		translatedCount = done
	}

	if err := q.updateStatus(context.WithoutCancel(ctx), taskID, repository.TaskStatusCompleted, StageCompleted, "已完成", progressDone,
		fmt.Sprintf("共 %d 条字幕，已翻译 %d 条", len(segments), translatedCount), ""); err != nil {
		q.markFailed(tc, fmt.Errorf("保存任务完成状态失败: %w", err))
		return
	}
}

// StageTranslated 是「识别完成但翻译未完成」的阶段标识。
const StageTranslated = "translated-partial"

// enhanceAudio 执行降噪增强，返回增强后的音频路径。
//
// 返回值语义：
//   - 返回空串且 cleanup 为 nil：降噪关闭或本机无可用滤镜，调用方继续用原始音频；
//   - 返回非空路径：调用方改用该文件，并在任务结束时调用 cleanup 清理；
//   - 降噪失败不返回错误：降噪是「锦上添花」，失败应降级为用原始音频继续识别，
//     而不是让整条任务失败（用户至少还能拿到一份字幕）。
func (q *Queue) enhanceAudio(tc *taskContext, rec *repository.TaskRecord, settings repository.SettingsRecord, audioPath string) (string, func()) {
	preset := strings.TrimSpace(settings.NoiseReduction)
	if preset == "" || strings.EqualFold(preset, media.PresetOff) {
		return "", nil
	}
	if q.deps.Enhancer == nil {
		log.Printf("任务 %s：降噪已开启（%s），但当前没有可用的音频增强器，跳过降噪", rec.ID, preset)
		return "", nil
	}

	dir := filepath.Dir(audioPath)
	output := filepath.Join(dir, "audio-enhanced.wav")
	sampleRate := settings.NoiseReductionSampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	_ = q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusExtracting, StageEnhancing,
		"正在降噪", progressEnhanceStart,
		fmt.Sprintf("正在消除背景杂音（%s）", noiseReductionLabel(preset)), "")

	result, err := q.deps.Enhancer.Enhance(tc.ctx, media.EnhanceRequest{
		InputPath:   audioPath,
		OutputPath:  output,
		Preset:      preset,
		FilterChain: settings.NoiseReductionFilter,
		SampleRate:  sampleRate,
		Channels:    1,
	})
	if err != nil {
		// 取消/暂停要如实上报，不能吞掉。
		if aborted := tc.check(); aborted != nil {
			return "", nil
		}
		log.Printf("任务 %s：降噪失败，改用原始音频继续识别：%v", rec.ID, err)
		_ = q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusExtracting, StageExtracting,
			"降噪失败，改用原始音频", progressEnhanceEnd,
			"降噪失败，已改用原始音频继续识别", "")
		return "", nil
	}

	// Filter 为空表示没有任何滤镜生效（预设为 off，或本机 FFmpeg 缺少全部滤镜）。
	if strings.TrimSpace(result.Filter) == "" {
		log.Printf("任务 %s：降噪预设 %s 未产生可用滤镜，跳过降噪", rec.ID, preset)
		return "", nil
	}
	if info, statErr := os.Stat(output); statErr != nil || info.Size() == 0 {
		log.Printf("任务 %s：降噪输出无效，改用原始音频：%v", rec.ID, statErr)
		return "", nil
	}

	message := fmt.Sprintf("降噪完成（%s）", noiseReductionLabel(preset))
	if len(result.Skipped) > 0 {
		message += fmt.Sprintf("；本机 FFmpeg 不支持 %d 个滤镜，已自动降级", len(result.Skipped))
	}
	_ = q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusExtracting, StageEnhancing,
		"降噪完成", progressEnhanceEnd, message, "")

	cleanup := func() {
		if output != "" {
			_ = os.Remove(output)
		}
	}
	return output, cleanup
}

// noiseReductionLabel 把降噪预设名转成中文标签，用于进度提示。
func noiseReductionLabel(preset string) string {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case media.PresetLight:
		return "轻度"
	case media.PresetStandard:
		return "标准"
	case media.PresetStrong:
		return "强力"
	case media.PresetCustom:
		return "自定义"
	default:
		return "关闭"
	}
}

// probe 读取媒体元数据，失败时返回零值但不上报致命错误。
func (q *Queue) probe(ctx context.Context, sourceFile string) (media.Info, error) {
	if q.deps.Media == nil {
		return media.Info{}, errors.New("媒体服务未配置")
	}
	return q.deps.Media.Probe(ctx, sourceFile)
}

// extractAudio 提取 16kHz 单声道 wav 供 ASR 使用。
func (q *Queue) extractAudio(tc *taskContext, rec *repository.TaskRecord, settings repository.SettingsRecord) (string, error) {
	if q.deps.Media == nil {
		return "", errors.New("媒体服务未配置，无法提取音频")
	}
	if _, err := os.Stat(rec.SourceFile); err != nil {
		return "", fmt.Errorf("源文件不存在或不可读: %s", rec.SourceFile)
	}
	if err := q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusExtracting, StageExtracting, "正在提取音频", progressExtractStart,
		"调用 FFmpeg 提取音频", ""); err != nil {
		return "", err
	}

	output := ""
	if dir := q.workDir(rec.ID); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("创建任务工作目录失败: %w", err)
		}
		output = filepath.Join(dir, "audio.wav")
	} else {
		tmp, err := os.MkdirTemp("", "transcribe-whisper-*")
		if err != nil {
			return "", fmt.Errorf("创建临时目录失败: %w", err)
		}
		output = filepath.Join(tmp, "audio.wav")
	}

	req := media.ExtractRequest{InputPath: rec.SourceFile, OutputPath: output, SampleRate: 16000, Channels: 1}
	if err := q.deps.Media.Extract(tc.ctx, req); err != nil {
		return "", fmt.Errorf("提取音频失败: %w", err)
	}
	if err := q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusExtracting, StageExtracting, "音频就绪", progressExtractEnd,
		"音频提取完成", ""); err != nil {
		return output, err
	}
	return output, nil
}

// transcribeStage 执行识别并把结果写入数据库。
func (q *Queue) transcribeStage(tc *taskContext, rec *repository.TaskRecord, settings repository.SettingsRecord, audioPath string) ([]repository.SegmentRecord, string, error) {
	engineName := strings.TrimSpace(rec.Engine)
	if engineName == "" {
		engineName = settings.AsrEngine
	}
	modelID := strings.TrimSpace(rec.Model)
	if modelID == "" {
		modelID = settings.DefaultModel
	}
	if modelID == "" {
		modelID = "small"
	}

	// 按模型格式挑引擎：cohere 这类 transformers 模型不能交给 faster-whisper。
	engine := q.deps.Engine
	if q.deps.Engines != nil {
		if picked := q.deps.Engines.Select(modelID, engineName); picked != nil {
			engine = picked
		}
	}
	if engine == nil {
		return nil, "", errors.New("识别引擎未配置")
	}
	// 实际使用的引擎名以选中结果为准：任务记录里的 engine 可能是旧值。
	engineName = engine.Name()

	if err := q.updateStatus(tc.ctx, rec.ID, repository.TaskStatusTranscribing, StageTranscribing, "正在识别", progressExtractEnd,
		"加载识别模型 "+modelID, ""); err != nil {
		return nil, "", err
	}

	// 所有引擎都需要模型目录：faster-whisper 要 CTranslate2 目录，
	// transformers 要 safetensors 目录。自选本地模型也是通过 Ensure 解析的。
	modelPath := ""
	modelRoot := ""
	if q.deps.Resolver != nil {
		manager, ok := q.deps.Resolver.(interface{ RootDir() string })
		if ok {
			modelRoot = manager.RootDir()
		}
		path, err := q.deps.Resolver.Ensure(tc.ctx, modelID, func(ratio float64, message string) {
			p := progressExtractEnd + int(clampFloat(ratio)*float64(progressAsrEnd-progressExtractEnd-5))
			_ = q.updateStatus(context.WithoutCancel(tc.ctx), rec.ID, repository.TaskStatusTranscribing, StageTranscribing, "正在下载模型", p, message, "")
		})
		if err != nil {
			if aborted := tc.check(); aborted != nil {
				return nil, "", aborted
			}
			if strings.Contains(err.Error(), "未知的模型") {
				return nil, "", fmt.Errorf("模型 %s 不在模型清单中，请先在「模型管理」下载可用模型或自选本地模型，或到设置里更换默认模型", modelID)
			}
			return nil, "", fmt.Errorf("准备识别模型失败: %w", err)
		}
		modelPath = path
	}

	language := strings.TrimSpace(rec.SourceLanguage)
	if language == "" || language == "auto" {
		// 交给引擎自动检测。
		language = ""
	}

	req := asr.Request{
		AudioPath:    audioPath,
		ModelPath:    modelPath,
		ModelName:    modelID,
		DownloadRoot: modelRoot,
		Language:     language,
		Device:       settings.AsrDevice,
		ComputeType:  settings.ComputeType,
		BeamSize:     resolveBeamSize(settings.BeamSize),
		VadFilter:    settings.VadFilter,
		// 以下参数提高识别准确性；设置层把零值视为「未设置」，这里回落默认值。
		VadThreshold:              resolveFloat(settings.VadThreshold, 0.5),
		VadMinSilenceMs:           resolveInt(settings.VadMinSilenceMs, 500),
		VadPaddingMs:              resolveInt(settings.VadPaddingMs, 400),
		ConditionOnPreviousText:   settings.ConditionOnPreviousText,
		NoSpeechThreshold:         resolveFloat(settings.NoSpeechThreshold, 0.6),
		CompressionRatioThreshold: resolveFloat(settings.CompressionRatioThreshold, 2.4),
	}

	progressFn := func(ratio float64, message string) {
		// 识别阶段占 15%..80% 的进度区间。
		p := progressExtractEnd + int(clampFloat(ratio)*float64(progressAsrEnd-progressExtractEnd-2))
		if p > progressAsrEnd-2 {
			p = progressAsrEnd - 2
		}
		if message == "" {
			message = "正在识别"
		}
		_ = q.updateStatus(context.WithoutCancel(tc.ctx), rec.ID, repository.TaskStatusTranscribing, StageTranscribing, message, p, message, "")
		// 让暂停/取消尽快生效。
		if tc.interrupted() {
			return
		}
	}

	result, err := engine.Transcribe(tc.ctx, req, progressFn)
	if err != nil {
		return nil, "", fmt.Errorf("语音识别失败: %w", err)
	}

	// 清洗幻觉字幕：识别结果落库前先按设置的严格程度过滤一遍。
	if result != nil && len(result.Segments) > 0 {
		cleaned := subtitleclean.Clean(result.Segments, subtitleclean.Options{
			Level: subtitleclean.Level(settings.HallucinationFilter),
			// 传入源语言用于「跨语种幻觉」判定：日语视频里整条变成纯英文
			// 基本可断定是识别幻觉。优先用任务上指定的语言，其次是引擎
			// 自动检测出来的语言（result.Language）。
			ExpectedLanguage: cleanLanguage(language, result.Language),
		})
		if cleaned.Dropped > 0 {
			log.Printf("任务 %s：过滤掉 %d 条疑似幻觉字幕（等级：%s）",
				rec.ID, cleaned.Dropped, settings.HallucinationFilter)
		}
		result.Segments = cleaned.Segments
	}

	segments := toASRSegments(rec.ID, result)
	if err := q.deps.Store.ReplaceSegments(context.WithoutCancel(tc.ctx), rec.ID, segments); err != nil {
		return nil, "", fmt.Errorf("保存识别结果失败: %w", err)
	}
	if err := q.deps.Store.UpdateTaskCounts(context.WithoutCancel(tc.ctx), rec.ID, len(segments), 0, result.Duration); err != nil {
		return nil, "", fmt.Errorf("更新任务统计失败: %w", err)
	}
	return segments, result.Language, nil
}

// cleanLanguage 决定清洗器该按哪种语言判断「跨语种幻觉」。
//
// 优先用用户在任务上显式指定的语言（requested）；为空或是 auto 时退回
// 引擎自动检测的结果（detected）。两者都不可用时返回空串，此时清洗器
// 不会启用跨语种规则，行为与改动前一致。
func cleanLanguage(requested, detected string) string {
	for _, candidate := range []string{requested, detected} {
		value := strings.TrimSpace(candidate)
		if value == "" || strings.EqualFold(value, "auto") {
			continue
		}
		return value
	}
	return ""
}

// resolveBeamSize 返回可用的 beam_size。
//
// 设置层把 0 视为「未设置」（保持零值往返），所以默认值在这里补：
// 低于 1 用 5，高于 10 用 10，避免用户填了离谱数字让识别慢到不可用。
func resolveBeamSize(value int) int {
	if value < 1 {
		return 5
	}
	if value > 10 {
		return 10
	}
	return value
}

// resolveFloat 在值为零（未设置）时返回默认值。
func resolveFloat(value, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	return value
}

// resolveInt 在值为零（未设置）时返回默认值。
func resolveInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// translateStage 批量翻译任务里 pending 的字幕，返回已翻译条目数。
func (q *Queue) translateStage(ctx context.Context, taskID, sourceLanguage string, onProgress func(done, total int)) (int, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return 0, err
	}
	settings, err := q.deps.Settings(ctx)
	if err != nil {
		return 0, fmt.Errorf("读取应用设置失败: %w", err)
	}
	if q.deps.Translator == nil {
		return 0, errors.New("翻译服务未配置")
	}
	if sourceLanguage == "" {
		sourceLanguage = rec.SourceLanguage
	}

	providerName := strings.TrimSpace(rec.Provider)
	if providerName == "" || providerName == "none" {
		providerName = settings.TranslationProvider
	}
	if providerName == "" || providerName == "none" {
		return 0, errors.New("当前设置为不翻译")
	}

	apiKey := ""
	if !isLocalProvider(providerName) {
		apiKey = settings.TranslationAPIKey
		if !settings.CloudPrivacyConfirm {
			return 0, errors.New("使用云端翻译前需要在设置中确认隐私提示")
		}
	}

	pipeline, err := q.deps.Translator.New(ctx, TranslationJob{
		TaskID:         taskID,
		Provider:       providerName,
		Endpoint:       settings.TranslationEndpoint,
		APIKey:         apiKey,
		Model:          settings.TranslationModel,
		SourceLanguage: sourceLanguage,
		TargetLanguage: rec.TargetLanguage,
	})
	if err != nil {
		return 0, fmt.Errorf("初始化翻译服务失败: %w", err)
	}

	total := rec.SubtitleCount
	pending, err := q.deps.Store.TranslatePendingSegments(ctx, taskID, 100000)
	if err != nil {
		return 0, fmt.Errorf("读取待翻译字幕失败: %w", err)
	}
	if len(pending) == 0 {
		return total, nil
	}

	done := total - len(pending)
	items := make([]translation.Item, 0, len(pending))
	for _, seg := range pending {
		items = append(items, translation.Item{ID: seg.ID, Text: seg.SourceText})
	}

	_ = q.updateStatus(ctx, taskID, repository.TaskStatusTranslating, StageTranslating, "正在翻译",
		progressAsrEnd+int(clampFloat(float64(done)/float64(max(total, 1)))*float64(progressTranslateEnd-progressAsrEnd)),
		fmt.Sprintf("翻译 %d / %d 条", done, total), "")

	choices, translateErr := pipeline.Translate(ctx, items, func(d, t int) {
		if onProgress != nil {
			onProgress(d, t)
		}
		base := progressAsrEnd
		ratio := 0.0
		if t > 0 {
			ratio = float64(d) / float64(t)
		}
		p := base + int(clampFloat(ratio)*float64(progressTranslateEnd-base))
		_ = q.updateStatus(context.WithoutCancel(ctx), taskID, repository.TaskStatusTranslating, StageTranslating, "正在翻译", p,
			fmt.Sprintf("翻译 %d / %d 条", done+d, max(total, t)), "")
	})

	translated := done
	for _, choice := range choices {
		text := strings.TrimSpace(choice.Text)
		if text == "" {
			continue
		}
		status := repository.SegmentStatusTranslated
		if err := q.deps.Store.UpdateSegmentTranslation(context.WithoutCancel(ctx), taskID, choice.ID, text, status); err != nil {
			return translated, fmt.Errorf("保存译文失败: %w", err)
		}
		translated++
	}

	// 把翻译失败的条目标记出来，便于界面提示。
	if translateErr != nil {
		for _, seg := range pending {
			if !containsChoice(choices, seg.ID) {
				_ = q.deps.Store.UpdateSegmentTranslation(context.WithoutCancel(ctx), taskID, seg.ID, "", repository.SegmentStatusFailed)
			}
		}
	}

	if err := q.deps.Store.UpdateTaskCounts(context.WithoutCancel(ctx), taskID, total, translated, rec.Duration); err != nil {
		return translated, fmt.Errorf("更新翻译统计失败: %w", err)
	}
	q.emit(context.WithoutCancel(ctx), mustTask(context.WithoutCancel(ctx), q.deps.Store, taskID))
	return translated, translateErr
}

// containsChoice 判断某个 id 是否出现在译文列表中。
func containsChoice(choices []translation.Choice, id string) bool {
	for _, choice := range choices {
		if choice.ID == id {
			return true
		}
	}
	return false
}

// mustTask 读取任务，失败返回 nil（事件推送允许失败）。
func mustTask(ctx context.Context, store *repository.Store, taskID string) *repository.TaskRecord {
	rec, err := store.GetTask(ctx, taskID)
	if err != nil {
		return nil
	}
	return rec
}

// isLocalProvider 判断 Provider 是否是本机服务。
func isLocalProvider(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ollama", "llama-server", "localhost":
		return true
	default:
		return false
	}
}

// markFailed 把任务标记为失败。
func (q *Queue) markFailed(tc *taskContext, err error) {
	ctx := context.WithoutCancel(tc.ctx)
	rec := mustTask(ctx, q.deps.Store, tc.taskID)
	progress := 0
	if rec != nil {
		progress = rec.Progress
	}
	msg := err.Error()
	_ = q.deps.Store.UpdateTaskStatus(ctx, tc.taskID, repository.TaskStatusFailed, StageFailed, "执行失败", progress, msg, msg)
	q.emit(ctx, mustTask(ctx, q.deps.Store, tc.taskID))
}

// finishAborted 处理暂停/取消导致的中止。
func (q *Queue) finishAborted(tc *taskContext, err error, progress int) {
	ctx := context.WithoutCancel(tc.ctx)
	tc.mu.Lock()
	paused := tc.paused
	reason := tc.reason
	tc.mu.Unlock()

	if paused {
		// 状态已由 Pause 写入，不再覆盖。
		return
	}
	if reason == "用户取消" {
		_ = q.deps.Store.UpdateTaskStatus(ctx, tc.taskID, repository.TaskStatusCanceled, StageCanceled, "已取消", progress, "任务已取消", "")
		q.emit(ctx, mustTask(ctx, q.deps.Store, tc.taskID))
		return
	}
	msg := "任务已中止"
	if err != nil && !errors.Is(err, context.Canceled) {
		msg = err.Error()
	}
	_ = q.deps.Store.UpdateTaskStatus(ctx, tc.taskID, repository.TaskStatusFailed, StageFailed, "已中止", progress, msg, msg)
	q.emit(ctx, mustTask(ctx, q.deps.Store, tc.taskID))
}

// finish 收尾：先标记状态写完，再注销执行上下文。
func (q *Queue) finish(tc *taskContext) {
	tc.cancel()
	close(tc.done)
	q.unregister(tc.taskID)
}

// check 判断任务是否被暂停或取消；被取消返回 context.Canceled。
func (tc *taskContext) check() error {
	tc.mu.Lock()
	paused := tc.paused
	tc.mu.Unlock()
	if paused {
		return errPaused
	}
	if err := tc.ctx.Err(); err != nil {
		return err
	}
	return nil
}

// interrupted 报告任务是否已请求中断（暂停或取消）。
func (tc *taskContext) interrupted() bool {
	tc.mu.Lock()
	paused := tc.paused
	tc.mu.Unlock()
	return paused || tc.ctx.Err() != nil
}

// errPaused 表示任务被暂停。
var errPaused = errors.New("任务已暂停")

// nowUTC 返回统一的时间格式，便于测试替换。
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
