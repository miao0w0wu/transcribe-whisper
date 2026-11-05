package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/repository"
)

// isTaskFinished 判断任务是否已进入终态。
//
// 主包有同名辅助函数，但它在 package main 里不可见，这里按同样语义重写一份。
func isTaskFinished(status string) bool {
	switch status {
	case repository.TaskStatusCompleted, repository.TaskStatusFailed, repository.TaskStatusCanceled:
		return true
	}
	return false
}

// fakeEnhancer 模拟降噪增强器，记录收到的请求并产出占位文件。
type fakeEnhancer struct {
	mu     sync.Mutex
	reqs   []media.EnhanceRequest
	result media.EnhanceResult
	err    error
	// writeOutput 为 true 时按 OutputPath 写一个非空文件（模拟 FFmpeg 产出）。
	writeOutput bool
}

func (f *fakeEnhancer) Enhance(ctx context.Context, req media.EnhanceRequest) (media.EnhanceResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return media.EnhanceResult{}, f.err
	}
	if f.writeOutput && strings.TrimSpace(req.OutputPath) != "" {
		if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
			return media.EnhanceResult{}, err
		}
		if err := os.WriteFile(req.OutputPath, []byte("enhanced audio"), 0o644); err != nil {
			return media.EnhanceResult{}, err
		}
	}
	return f.result, nil
}

func (f *fakeEnhancer) requests() []media.EnhanceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]media.EnhanceRequest(nil), f.reqs...)
}

// recordingEngine 记录识别请求，并返回固定结果。
type recordingEngine struct {
	mu     sync.Mutex
	reqs   []asr.Request
	result *asr.Result
}

func (e *recordingEngine) Name() string { return "faster-whisper" }

func (e *recordingEngine) Available(ctx context.Context) (bool, string) { return true, "ok" }

func (e *recordingEngine) Transcribe(ctx context.Context, req asr.Request, progress asr.Progress) (*asr.Result, error) {
	e.mu.Lock()
	e.reqs = append(e.reqs, req)
	result := e.result
	e.mu.Unlock()
	if result != nil {
		return result, nil
	}
	return &asr.Result{Language: "en", Duration: 10, Segments: []asr.Segment{
		{Sequence: 1, Start: 0, End: 1, Text: "正常对话"},
	}}, nil
}

func (e *recordingEngine) requests() []asr.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]asr.Request(nil), e.reqs...)
}

// settingsWith 返回一个在默认设置上打补丁的 Settings 函数。
func settingsWith(base repository.SettingsRecord, mutate func(*repository.SettingsRecord)) func(context.Context) (repository.SettingsRecord, error) {
	return func(ctx context.Context) (repository.SettingsRecord, error) {
		rec := base
		if mutate != nil {
			mutate(&rec)
		}
		return rec, nil
	}
}

// runTask 创建并执行一个任务，返回最终记录。
func runTask(t *testing.T, q *Queue) *repository.TaskRecord {
	t.Helper()
	rec := createTask(t, q.store(), "none")
	if _, err := q.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, err := q.store().GetTask(context.Background(), rec.ID)
		return err == nil && isTaskFinished(got.Status)
	})
	got, err := q.store().GetTask(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return got
}

func TestEnhanceStageRunsWhenNoiseReductionEnabled(t *testing.T) {
	enhancer := &fakeEnhancer{
		result:      media.EnhanceResult{Filter: "highpass=f=80,afftdn=nf=-25", Applied: []string{"highpass=f=80", "afftdn=nf=-25"}},
		writeOutput: true,
	}
	engine := &recordingEngine{}
	var capturedAudio string
	engine.result = &asr.Result{Language: "en", Duration: 10, Segments: []asr.Segment{
		{Sequence: 1, Start: 0, End: 1, Text: "正常对话"},
	}}

	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = enhancer
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetStandard
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)

	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	reqs := enhancer.requests()
	if len(reqs) != 1 {
		t.Fatalf("应调用一次降噪，实际 %d 次", len(reqs))
	}
	if reqs[0].Preset != media.PresetStandard {
		t.Errorf("降噪预设应为 standard，实际 %q", reqs[0].Preset)
	}
	if reqs[0].SampleRate != 16000 || reqs[0].Channels != 1 {
		t.Errorf("降噪输出参数应为 16000Hz 单声道，实际 %d Hz / %d 声道",
			reqs[0].SampleRate, reqs[0].Channels)
	}
	// 识别阶段拿到的应是降噪产物，而不是原始提取音频。
	asrReqs := engine.requests()
	if len(asrReqs) != 1 {
		t.Fatalf("应调用一次识别，实际 %d 次", len(asrReqs))
	}
	capturedAudio = asrReqs[0].AudioPath
	if !strings.Contains(capturedAudio, "audio-enhanced.wav") {
		t.Errorf("识别应使用降噪后的音频，实际 %q", capturedAudio)
	}
}

func TestEnhanceStageSkippedWhenDisabled(t *testing.T) {
	enhancer := &fakeEnhancer{writeOutput: true}
	engine := &recordingEngine{}

	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = enhancer
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetOff
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)

	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	if got := enhancer.requests(); len(got) != 0 {
		t.Fatalf("降噪关闭时不应调用增强器，实际 %d 次", len(got))
	}
	asrReqs := engine.requests()
	if len(asrReqs) != 1 || !strings.HasSuffix(asrReqs[0].AudioPath, "audio.wav") {
		t.Fatalf("识别应使用原始提取音频，实际 %+v", asrReqs)
	}
}

func TestEnhanceFailureFallsBackToOriginalAudio(t *testing.T) {
	enhancer := &fakeEnhancer{err: errors.New("模拟 FFmpeg 失败")}
	engine := &recordingEngine{}

	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = enhancer
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetStrong
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)

	// 降噪失败不能让整条任务失败：用户至少应拿到字幕。
	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("降噪失败应降级继续，任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	asrReqs := engine.requests()
	if len(asrReqs) != 1 {
		t.Fatalf("应仍执行识别，实际 %d 次", len(asrReqs))
	}
	if !strings.HasSuffix(asrReqs[0].AudioPath, "audio.wav") {
		t.Errorf("降噪失败后应使用原始音频，实际 %q", asrReqs[0].AudioPath)
	}
}

func TestEnhanceEmptyFilterFallsBackToOriginalAudio(t *testing.T) {
	// Filter 为空表示本机 FFmpeg 缺少全部滤镜，应视为未处理。
	enhancer := &fakeEnhancer{result: media.EnhanceResult{Filter: ""}}
	engine := &recordingEngine{}

	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = enhancer
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetLight
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)

	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	asrReqs := engine.requests()
	if len(asrReqs) != 1 || !strings.HasSuffix(asrReqs[0].AudioPath, "audio.wav") {
		t.Fatalf("无可用滤镜时应使用原始音频，实际 %+v", asrReqs)
	}
}

func TestEnhancerNilSkipsStage(t *testing.T) {
	engine := &recordingEngine{}
	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = nil
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetStrong
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)
	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("没有增强器时不应失败，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
}

func TestHallucinationFilterAppliedBeforePersist(t *testing.T) {
	engine := &recordingEngine{result: &asr.Result{
		Language: "zh",
		Duration: 10,
		Segments: []asr.Segment{
			{Sequence: 1, Start: 0, End: 1, Text: "真正的对话内容"},
			{Sequence: 2, Start: 1, End: 2, Text: "感谢观看"},
			{Sequence: 3, Start: 2, End: 3, Text: "😀"},
			{Sequence: 4, Start: 3, End: 4, Text: "（音乐）"},
		},
	}}

	q := newTestQueue(t, func(d *Deps) {
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetOff
			r.HallucinationFilter = "high"
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)

	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	segments, err := q.store().ListSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("严格过滤后应只剩 1 条，实际 %d 条", len(segments))
	}
	if segments[0].SourceText != "真正的对话内容" {
		t.Errorf("保留的应是正常对话，实际 %q", segments[0].SourceText)
	}
}

func TestHallucinationFilterOffKeepsAll(t *testing.T) {
	engine := &recordingEngine{result: &asr.Result{
		Language: "zh",
		Duration: 10,
		Segments: []asr.Segment{
			{Sequence: 1, Start: 0, End: 1, Text: "正常内容"},
			{Sequence: 2, Start: 1, End: 2, Text: "感谢观看"},
		},
	}}
	q := newTestQueue(t, func(d *Deps) {
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.HallucinationFilter = "off"
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)
	segments, err := q.store().ListSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("关闭过滤时应保留全部 2 条，实际 %d 条", len(segments))
	}
}

func TestASRRequestCarriesAccuracySettings(t *testing.T) {
	engine := &recordingEngine{}
	q := newTestQueue(t, func(d *Deps) {
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.BeamSize = 8
			r.VadFilter = true
			r.VadThreshold = 0.35
			r.VadMinSilenceMs = 700
			r.VadPaddingMs = 250
			r.ConditionOnPreviousText = true
			r.NoSpeechThreshold = 0.75
			r.CompressionRatioThreshold = 3.1
			r.TranslationProvider = "none"
		})
	})
	rec := runTask(t, q)
	if rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	reqs := engine.requests()
	if len(reqs) != 1 {
		t.Fatalf("应调用一次识别，实际 %d 次", len(reqs))
	}
	req := reqs[0]
	if req.BeamSize != 8 {
		t.Errorf("BeamSize 应为 8，实际 %d", req.BeamSize)
	}
	if !req.VadFilter {
		t.Error("VadFilter 应为 true")
	}
	if req.VadThreshold != 0.35 {
		t.Errorf("VadThreshold 应为 0.35，实际 %v", req.VadThreshold)
	}
	if req.VadMinSilenceMs != 700 {
		t.Errorf("VadMinSilenceMs 应为 700，实际 %d", req.VadMinSilenceMs)
	}
	if req.VadPaddingMs != 250 {
		t.Errorf("VadPaddingMs 应为 250，实际 %d", req.VadPaddingMs)
	}
	if !req.ConditionOnPreviousText {
		t.Error("ConditionOnPreviousText 应为 true")
	}
	if req.NoSpeechThreshold != 0.75 {
		t.Errorf("NoSpeechThreshold 应为 0.75，实际 %v", req.NoSpeechThreshold)
	}
	if req.CompressionRatioThreshold != 3.1 {
		t.Errorf("CompressionRatioThreshold 应为 3.1，实际 %v", req.CompressionRatioThreshold)
	}
}

func TestASRRequestFallsBackToDefaultsForZeroSettings(t *testing.T) {
	// 设置层把零值视为「未设置」，service 必须回落默认值。
	engine := &recordingEngine{}
	q := newTestQueue(t, func(d *Deps) {
		d.Engine = engine
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.BeamSize = 0
			r.VadThreshold = 0
			r.VadMinSilenceMs = 0
			r.VadPaddingMs = 0
			r.NoSpeechThreshold = 0
			r.CompressionRatioThreshold = 0
			r.TranslationProvider = "none"
		})
	})
	if rec := runTask(t, q); rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	req := engine.requests()[0]
	if req.BeamSize != 5 {
		t.Errorf("BeamSize 零值应回落 5，实际 %d", req.BeamSize)
	}
	if req.VadThreshold != 0.5 {
		t.Errorf("VadThreshold 零值应回落 0.5，实际 %v", req.VadThreshold)
	}
	if req.VadMinSilenceMs != 500 {
		t.Errorf("VadMinSilenceMs 零值应回落 500，实际 %d", req.VadMinSilenceMs)
	}
	if req.VadPaddingMs != 400 {
		t.Errorf("VadPaddingMs 零值应回落 400，实际 %d", req.VadPaddingMs)
	}
	if req.NoSpeechThreshold != 0.6 {
		t.Errorf("NoSpeechThreshold 零值应回落 0.6，实际 %v", req.NoSpeechThreshold)
	}
	if req.CompressionRatioThreshold != 2.4 {
		t.Errorf("CompressionRatioThreshold 零值应回落 2.4，实际 %v", req.CompressionRatioThreshold)
	}
}

func TestCustomNoiseReductionFilterPassedThrough(t *testing.T) {
	enhancer := &fakeEnhancer{
		result:      media.EnhanceResult{Filter: "anlmdn=s=7"},
		writeOutput: true,
	}
	q := newTestQueue(t, func(d *Deps) {
		d.Enhancer = enhancer
		d.Engine = &recordingEngine{}
		d.Settings = settingsWith(repository.DefaultSettings(), func(r *repository.SettingsRecord) {
			r.NoiseReduction = media.PresetCustom
			r.NoiseReductionFilter = "anlmdn=s=7"
			r.NoiseReductionSampleRate = 48000
			r.TranslationProvider = "none"
		})
	})
	if rec := runTask(t, q); rec.Status != repository.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s（错误：%s）", rec.Status, rec.Error)
	}
	reqs := enhancer.requests()
	if len(reqs) != 1 {
		t.Fatalf("应调用一次降噪，实际 %d 次", len(reqs))
	}
	if reqs[0].FilterChain != "anlmdn=s=7" {
		t.Errorf("自定义滤镜链应透传，实际 %q", reqs[0].FilterChain)
	}
	if reqs[0].SampleRate != 48000 {
		t.Errorf("采样率应为 48000，实际 %d", reqs[0].SampleRate)
	}
}
