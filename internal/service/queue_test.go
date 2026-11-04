package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/translation"
)

// fakeMedia 模拟 FFmpeg：探测返回固定时长，提取时按需阻塞。
type fakeMedia struct {
	probeErr   error
	extractErr error
	blockUntil chan struct{}
}

func (f *fakeMedia) Probe(ctx context.Context, inputPath string) (media.Info, error) {
	if f.probeErr != nil {
		return media.Info{}, f.probeErr
	}
	return media.Info{Path: inputPath, Duration: 42.5, HasAudio: true}, nil
}

func (f *fakeMedia) Extract(ctx context.Context, req media.ExtractRequest) error {
	if f.blockUntil != nil {
		select {
		case <-f.blockUntil:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.extractErr != nil {
		return f.extractErr
	}
	return nil
}

// fakeEngine 模拟识别引擎。
//
// called 用原子变量：执行器 goroutine 写、测试 goroutine 读，
// 普通 bool 会让 -race 报警。
type fakeEngine struct {
	result  *asr.Result
	err     error
	onStart func(ctx context.Context)
	called  atomic.Bool
}

func (f *fakeEngine) Name() string { return "faster-whisper" }

func (f *fakeEngine) Available(ctx context.Context) (bool, string) { return true, "ok" }

func (f *fakeEngine) Transcribe(ctx context.Context, req asr.Request, progress asr.Progress) (*asr.Result, error) {
	f.called.Store(true)
	if f.onStart != nil {
		f.onStart(ctx)
	}
	if progress != nil {
		progress(0.5, "识别中")
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &asr.Result{
		Language: "en",
		Duration: 42.5,
		Segments: []asr.Segment{
			{Sequence: 1, Start: 0, End: 1.2, Text: "Hello there"},
			{Sequence: 2, Start: 1.4, End: 2.8, Text: "General Kenobi"},
			{Sequence: 3, Start: 3, End: 4, Text: "   "},
		},
	}, nil
}

// fakeResolver 模拟模型下载。
type fakeResolver struct {
	dir string
	err error
}

func (f *fakeResolver) Ensure(ctx context.Context, modelID string, progress modelmanager.Progress) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if progress != nil {
		progress(0.5, "下载模型")
	}
	if f.dir == "" {
		return "/tmp/model-" + modelID, nil
	}
	return f.dir, nil
}

// fakeTranslator 模拟翻译流水线。
type fakeTranslator struct {
	err     error
	missing bool
}

func (f *fakeTranslator) New(ctx context.Context, job TranslationJob) (Pipeline, error) {
	return f, nil
}

func (f *fakeTranslator) Translate(ctx context.Context, items []translation.Item, progress func(done, total int)) ([]translation.Choice, error) {
	choices := make([]translation.Choice, 0, len(items))
	for i, item := range items {
		if f.missing && i == 0 {
			continue
		}
		choices = append(choices, translation.Choice{ID: item.ID, Text: "译:" + item.Text})
		if progress != nil {
			progress(i+1, len(items))
		}
	}
	if f.err != nil {
		return choices, f.err
	}
	return choices, nil
}

// newTestQueue 构造带内存数据库的队列。
func newTestQueue(t *testing.T, mutate func(*Deps)) *Queue {
	t.Helper()
	store, err := repository.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deps := Deps{
		Store:      store,
		Media:      &fakeMedia{},
		Engine:     &fakeEngine{},
		Resolver:   &fakeResolver{},
		Translator: &fakeTranslator{},
		WorkDir:    t.TempDir(),
		Settings: func(ctx context.Context) (repository.SettingsRecord, error) {
			rec, err := store.LoadSettings(ctx)
			if err != nil {
				return rec, err
			}
			rec.TranslationProvider = "ollama"
			return rec, nil
		},
	}
	if mutate != nil {
		mutate(&deps)
	}
	queue := NewQueue(deps)
	t.Cleanup(queue.Close)
	return queue
}

// createTask 直接写入一条任务记录。
func createTask(t *testing.T, store *repository.Store, provider string) *repository.TaskRecord {
	t.Helper()
	rec := &repository.TaskRecord{
		ID:             "task-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Name:           "episode",
		SourceFile:     filepath.Join(t.TempDir(), "episode.mp4"),
		SourceLanguage: "auto",
		TargetLanguage: "zh-CN",
		Status:         repository.TaskStatusCreated,
		Stage:          "created",
		Engine:         "faster-whisper",
		Model:          "tiny",
		Provider:       provider,
		OutputFormat:   "srt",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := os.WriteFile(rec.SourceFile, []byte("fake media"), 0o644); err != nil {
		t.Fatalf("写入占位媒体文件失败: %v", err)
	}
	if err := store.CreateTask(context.Background(), rec); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return rec
}

// waitFor 轮询等待条件成立。
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func (q *Queue) store() *repository.Store { return q.deps.Store }

func TestRunCompletesWithTranslation(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "ollama")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})

	current, err := queue.store().GetTask(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if current.Progress != progressDone {
		t.Fatalf("完成进度应为 100，实际 %d", current.Progress)
	}
	if current.SubtitleCount != 2 {
		t.Fatalf("空白识别结果应被过滤，期望 2 条，实际 %d", current.SubtitleCount)
	}
	if current.TranslatedCount != 2 {
		t.Fatalf("应翻译 2 条，实际 %d", current.TranslatedCount)
	}
	if current.Duration <= 0 {
		t.Fatalf("媒体时长应被写入，实际 %f", current.Duration)
	}
	segments, err := queue.store().ListSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if len(segments) != 2 || !strings.HasPrefix(segments[0].Translated, "译:") {
		t.Fatalf("字幕翻译结果不正确: %+v", segments)
	}
}

func TestRunWithoutTranslation(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "none")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
	segments, err := queue.store().ListSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if segments[0].Status != repository.SegmentStatusPending || segments[0].Translated != "" {
		t.Fatalf("未翻译的字幕应保持 pending: %+v", segments[0])
	}
}

func TestRunFailsWhenExtractionFails(t *testing.T) {
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Media = &fakeMedia{extractErr: errors.New("ffmpeg 崩溃")}
	})
	rec := createTask(t, queue.store(), "none")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusFailed
	})
	current, _ := queue.store().GetTask(context.Background(), rec.ID)
	if !strings.Contains(current.Error, "ffmpeg") {
		t.Fatalf("错误信息应包含底层原因，实际 %q", current.Error)
	}
}

func TestRunKeepsSubtitlesWhenTranslationFails(t *testing.T) {
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Translator = &fakeTranslator{err: &translation.Error{Kind: translation.KindFatal, Message: "模型不存在"}}
	})
	rec := createTask(t, queue.store(), "ollama")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
	current, _ := queue.store().GetTask(context.Background(), rec.ID)
	if !strings.Contains(current.Error, "翻译") {
		t.Fatalf("翻译失败应写入错误提示，实际 %q", current.Error)
	}
	segments, err := queue.store().ListSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("翻译失败不应丢失识别结果，实际 %d 条", len(segments))
	}
}

func TestCancelStopsTask(t *testing.T) {
	blocker := make(chan struct{})
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Media = &fakeMedia{blockUntil: blocker}
	})
	rec := createTask(t, queue.store(), "none")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return queue.Running(rec.ID) })

	if _, err := queue.Cancel(context.Background(), rec.ID); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCanceled
	})
	if queue.Running(rec.ID) {
		t.Fatal("取消后任务不应再处于运行状态")
	}
}

func TestPauseAndResume(t *testing.T) {
	blocker := make(chan struct{})
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Media = &fakeMedia{blockUntil: blocker}
	})
	rec := createTask(t, queue.store(), "none")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return queue.Running(rec.ID) })

	if _, err := queue.Pause(context.Background(), rec.ID); err != nil {
		t.Fatalf("暂停任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusPaused
	})
	if queue.Running(rec.ID) {
		t.Fatal("暂停后执行应结束")
	}

	// 让阻塞的提取立即返回，验证恢复后能跑到完成。
	close(blocker)
	if _, err := queue.Resume(context.Background(), rec.ID); err != nil {
		t.Fatalf("恢复任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
}

func TestPauseRejectsIdleTask(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "none")
	if _, err := queue.Pause(context.Background(), rec.ID); err == nil {
		t.Fatal("没有运行的任务不应能暂停")
	}
}

func TestRetryAfterFailure(t *testing.T) {
	failing := &fakeMedia{extractErr: errors.New("一次性故障")}
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Media = failing
	})
	rec := createTask(t, queue.store(), "none")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusFailed
	})

	failing.extractErr = nil
	if _, err := queue.Retry(context.Background(), rec.ID); err != nil {
		t.Fatalf("重试任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
}

func TestRetranslateResetsTranslations(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "ollama")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})

	if _, err := queue.Retranslate(context.Background(), rec.ID); err != nil {
		t.Fatalf("重新翻译失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted && current.TranslatedCount == 2
	})
}

func TestTranslationJobCarriesPrivacyGate(t *testing.T) {
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Settings = func(ctx context.Context) (repository.SettingsRecord, error) {
			return repository.SettingsRecord{TranslationProvider: "openai", CloudPrivacyConfirm: false}, nil
		}
	})
	rec := createTask(t, queue.store(), "openai")
	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
	current, _ := queue.store().GetTask(context.Background(), rec.ID)
	if !strings.Contains(current.Error, "隐私") {
		t.Fatalf("云端翻译未确认隐私时应跳过并提示，实际 %q", current.Error)
	}
}

func TestEmitterReceivesProgress(t *testing.T) {
	var mu sync.Mutex
	var stages []string
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Emitters = []Emitter{func(ctx context.Context, rec *repository.TaskRecord) {
			mu.Lock()
			stages = append(stages, rec.Status)
			mu.Unlock()
		}}
	})
	rec := createTask(t, queue.store(), "none")
	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	// 不能只等「数据库里状态变成 completed」：emit 是异步的，最后那个
	// completed 事件可能还没送达就被断言了，用例会随机失败。
	// 这里直接等事件里确实收到 completed。
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range stages {
			if s == repository.TaskStatusCompleted {
				return true
			}
		}
		return false
	})

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(stages, ",")
	for _, want := range []string{repository.TaskStatusQueued, repository.TaskStatusExtracting, repository.TaskStatusTranscribing, repository.TaskStatusCompleted} {
		if !strings.Contains(joined, want) {
			t.Fatalf("事件序列缺少 %s：%s", want, joined)
		}
	}
}

func TestRecoverMarksInterruptedTasks(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "none")
	if err := queue.store().UpdateTaskStatus(context.Background(), rec.ID, repository.TaskStatusTranscribing, "transcribing", "识别中", 40, "", ""); err != nil {
		t.Fatalf("准备任务状态失败: %v", err)
	}
	affected, err := queue.Recover(context.Background())
	if err != nil {
		t.Fatalf("恢复清理失败: %v", err)
	}
	if affected != 1 {
		t.Fatalf("应标记 1 个中断任务，实际 %d", affected)
	}
	current, _ := queue.store().GetTask(context.Background(), rec.ID)
	if current.Status != repository.TaskStatusFailed {
		t.Fatalf("中断任务应标记为失败，实际 %s", current.Status)
	}
}

func TestDeleteRemovesTaskAndSubtitles(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "none")
	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})
	if err := queue.Delete(context.Background(), rec.ID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if _, err := queue.store().GetTask(context.Background(), rec.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("删除后应查不到任务，实际错误: %v", err)
	}
}

func TestExportContentUsesRequestedFormat(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "ollama")
	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, err := queue.store().GetTask(context.Background(), rec.ID)
		return err == nil && current.Status == repository.TaskStatusCompleted
	})

	content, name, err := queue.ExportContent(context.Background(), rec.ID, "vtt")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !strings.HasPrefix(content, "WEBVTT") {
		t.Fatalf("VTT 内容不正确: %q", content)
	}
	if !strings.HasSuffix(name, ".vtt") {
		t.Fatalf("导出文件名应以 .vtt 结尾，实际 %s", name)
	}
}

func TestNormalizeTiming(t *testing.T) {
	start, end := normalizeTiming(-5, -1)
	if start != 0 || end <= start {
		t.Fatalf("负时间应归零并保证结束晚于开始: %f %f", start, end)
	}
	start, end = normalizeTiming(10, 9)
	if end-start < 0.099 {
		t.Fatalf("结束早于开始时应拉开最小间隔: %f %f", start, end)
	}
}

func TestClamp(t *testing.T) {
	if clamp(-1, 0, 100) != 0 || clamp(101, 0, 100) != 100 || clamp(50, 0, 100) != 50 {
		t.Fatal("clamp 行为不正确")
	}
	if clampFloat(-0.5) != 0 || clampFloat(1.5) != 1 || clampFloat(0.3) != 0.3 {
		t.Fatal("clampFloat 行为不正确")
	}
}

func TestIsLocalProvider(t *testing.T) {
	for _, name := range []string{"ollama", "OLLAMA", "llama-server"} {
		if !isLocalProvider(name) {
			t.Fatalf("%s 应被识别为本机服务", name)
		}
	}
	if isLocalProvider("openai") {
		t.Fatal("openai 不应被识别为本机服务")
	}
}
