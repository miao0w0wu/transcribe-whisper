package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/repository"
)

// fakeBurner 记录收到的压制请求，并可注入错误。
type fakeBurner struct {
	calls    int
	lastReq  media.BurnRequest
	err      error
	onBurn   func(req media.BurnRequest)
	writeOut bool
}

func (f *fakeBurner) Burn(ctx context.Context, req media.BurnRequest) error {
	f.calls++
	f.lastReq = req
	if f.onBurn != nil {
		f.onBurn(req)
	}
	if f.err != nil {
		return f.err
	}
	if f.writeOut {
		if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(req.OutputPath, []byte("fake mp4"), 0o644)
	}
	return nil
}

// seedSegments 给任务写入两条字幕（一条有译文、一条只有原文）。
func seedSegments(t *testing.T, store *repository.Store, taskID string) {
	t.Helper()
	records := []repository.SegmentRecord{
		{ID: taskID + "-1", TaskID: taskID, Sequence: 1, Start: 0, End: 1.5, SourceText: "Hello", Translated: "你好"},
		{ID: taskID + "-2", TaskID: taskID, Sequence: 2, Start: 1.6, End: 3, SourceText: "World", Translated: ""},
	}
	if err := store.UpsertSegments(context.Background(), taskID, records); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}
}

func TestBurnSubtitlesWritesSubtitleAndCallsBurner(t *testing.T) {
	burner := &fakeBurner{}
	queue := newTestQueue(t, func(deps *Deps) { deps.Burner = burner })
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)

	output := filepath.Join(t.TempDir(), "out.mp4")
	got, err := queue.BurnSubtitles(context.Background(), rec.ID, output, BurnOptions{FontSize: 30, MarginV: 20})
	if err != nil {
		t.Fatalf("压制失败: %v", err)
	}
	if burner.calls != 1 {
		t.Fatalf("应调用一次 Burn，实际 %d", burner.calls)
	}
	if got != burner.lastReq.OutputPath {
		t.Fatalf("返回路径应与传给 FFmpeg 的输出一致: got=%q req=%q", got, burner.lastReq.OutputPath)
	}
	if burner.lastReq.VideoPath != rec.SourceFile {
		t.Fatalf("应压制源视频 %q，实际 %q", rec.SourceFile, burner.lastReq.VideoPath)
	}
	if burner.lastReq.FontSize != 30 || burner.lastReq.MarginV != 20 {
		t.Fatalf("外观参数没有透传: %+v", burner.lastReq)
	}

	// 生成的 SRT 必须与导出策略一致：译文优先、缺失回退原文。
	content, err := os.ReadFile(burner.lastReq.SubtitlePath)
	if err != nil {
		t.Fatalf("读取压制用字幕失败: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "你好") {
		t.Fatalf("字幕应包含译文: %s", text)
	}
	if !strings.Contains(text, "World") {
		t.Fatalf("缺译文的条目应回退原文: %s", text)
	}
	if strings.Contains(text, "1.60") || !strings.Contains(text, "00:00:01,600") {
		t.Fatalf("时间轴格式异常: %s", text)
	}
}

func TestBurnSubtitlesRequiresBurner(t *testing.T) {
	queue := newTestQueue(t, nil)
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)
	_, err := queue.BurnSubtitles(context.Background(), rec.ID, filepath.Join(t.TempDir(), "o.mp4"), BurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "不支持字幕压制") {
		t.Fatalf("缺少 Burner 时应给出中文提示，实际: %v", err)
	}
}

func TestBurnSubtitlesRejectsMissingSubtitle(t *testing.T) {
	burner := &fakeBurner{}
	queue := newTestQueue(t, func(deps *Deps) { deps.Burner = burner })
	rec := createTask(t, queue.store(), "ollama")
	_, err := queue.BurnSubtitles(context.Background(), rec.ID, filepath.Join(t.TempDir(), "o.mp4"), BurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "还没有字幕") {
		t.Fatalf("无字幕时应报错，实际: %v", err)
	}
	if burner.calls != 0 {
		t.Fatalf("校验失败时不该调用 Burn，实际 %d 次", burner.calls)
	}
}

func TestBurnSubtitlesRejectsSameOutputPath(t *testing.T) {
	burner := &fakeBurner{}
	queue := newTestQueue(t, func(deps *Deps) { deps.Burner = burner })
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)
	_, err := queue.BurnSubtitles(context.Background(), rec.ID, rec.SourceFile, BurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "不能与源视频相同") {
		t.Fatalf("应当拒绝就地覆盖源视频，实际: %v", err)
	}
	if burner.calls != 0 {
		t.Fatalf("拒绝后不该调用 Burn，实际 %d 次", burner.calls)
	}
}

func TestBurnSubtitlesRejectsMissingSource(t *testing.T) {
	burner := &fakeBurner{}
	queue := newTestQueue(t, func(deps *Deps) { deps.Burner = burner })
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)
	if err := os.Remove(rec.SourceFile); err != nil {
		t.Fatalf("删除占位源视频失败: %v", err)
	}
	_, err := queue.BurnSubtitles(context.Background(), rec.ID, filepath.Join(t.TempDir(), "o.mp4"), BurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "源视频不存在") {
		t.Fatalf("源视频缺失时应报错，实际: %v", err)
	}
}

func TestBurnSubtitlesPropagatesBurnerError(t *testing.T) {
	burner := &fakeBurner{err: media.ErrSubtitlesUnsupported}
	queue := newTestQueue(t, func(deps *Deps) { deps.Burner = burner })
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)
	_, err := queue.BurnSubtitles(context.Background(), rec.ID, filepath.Join(t.TempDir(), "o.mp4"), BurnOptions{})
	if !errors.Is(err, media.ErrSubtitlesUnsupported) {
		t.Fatalf("应原样透传 FFmpeg 能力错误，实际: %v", err)
	}
}

func TestBurnSubtitlesRejectsWhileTaskRunning(t *testing.T) {
	burner := &fakeBurner{}
	block := make(chan struct{})
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Burner = burner
		deps.Media = &fakeMedia{blockUntil: block}
	})
	rec := createTask(t, queue.store(), "ollama")
	seedSegments(t, queue.store(), rec.ID)

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	// 等任务真的进入执行态。
	waitFor(t, 3*time.Second, func() bool { return queue.Running(rec.ID) })

	_, err := queue.BurnSubtitles(context.Background(), rec.ID, filepath.Join(t.TempDir(), "o.mp4"), BurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "任务正在执行中") {
		t.Fatalf("任务运行中应拒绝压制，实际: %v", err)
	}
	if burner.calls != 0 {
		t.Fatalf("运行中不该调用 Burn，实际 %d 次", burner.calls)
	}

	close(block)
	waitFor(t, 5*time.Second, func() bool { return !queue.Running(rec.ID) })
}
