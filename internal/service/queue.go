package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/exporter"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
)

// 各阶段在整体进度中的权重，合计 100。
const (
	progressExtractStart = 2
	progressExtractEnd   = 10
	progressEnhanceStart = 10
	progressEnhanceEnd   = 15
	progressAsrEnd       = 80
	progressTranslateEnd = 98
	progressDone         = 100
)

// 阶段标识，供前端展示与状态机判断。
const (
	StageQueued     = "queued"
	StageExtracting = "extracting"
	// StageEnhancing 是提取音频后的降噪增强阶段。
	StageEnhancing    = "enhancing"
	StageTranscribing = "transcribing"
	StageTranslating  = "translating"
	StageCompleted    = "completed"
	StageFailed       = "failed"
	StageCanceled     = "canceled"
	StagePaused       = "paused"
)

// Queue 是任务调度器：维护内存中的执行上下文，把任务阶段写入 SQLite，
// 并通过 Emitter 推送进度事件。
type Queue struct {
	deps Deps

	mu       sync.Mutex
	running  map[string]*taskContext
	slots    chan struct{}
	closed   bool
	wg       sync.WaitGroup
	pendingQ []string
}

// taskContext 保存单个任务的取消与暂停控制。
type taskContext struct {
	taskID string

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	paused   bool
	resumeCh chan struct{}
	// reason 记录取消原因，用于区分「用户取消」与「应用退出」。
	reason string
	// done 在执行器写完最终状态后关闭，供外部等待收尾完成。
	done chan struct{}
}

func newTaskContext(parent context.Context, taskID string) *taskContext {
	ctx, cancel := context.WithCancel(parent)
	return &taskContext{
		taskID:   taskID,
		ctx:      ctx,
		cancel:   cancel,
		resumeCh: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// NewQueue 构造调度器。deps 中的 nil 字段会用安全默认值补齐。
func NewQueue(deps Deps) *Queue {
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.Concurrency <= 0 {
		deps.Concurrency = 1
	}
	if deps.Settings == nil {
		deps.Settings = func(ctx context.Context) (repository.SettingsRecord, error) {
			return deps.Store.LoadSettings(ctx)
		}
	}
	return &Queue{
		deps:    deps,
		running: make(map[string]*taskContext),
		slots:   make(chan struct{}, deps.Concurrency),
	}
}

// SetMedia 替换媒体依赖（提取器、降噪增强器与压制器）。
//
// 用在用户在运行时改了 FFmpeg 路径的场景：队列装配时持有的是旧引用，
// 不同步的话新建任务仍会用旧的（或 nil）实现。传 nil 表示「当前不可用」，
// 此时相关阶段会返回明确的中文错误而不是 panic。
func (q *Queue) SetMedia(ffmpeg *media.FFmpeg) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if ffmpeg == nil {
		q.deps.Media = nil
		q.deps.Enhancer = nil
		q.deps.Burner = nil
		return
	}
	q.deps.Media = ffmpeg
	q.deps.Enhancer = ffmpeg
	q.deps.Burner = ffmpeg
}

// MediaReady 报告当前是否配置了可用的媒体依赖。
func (q *Queue) MediaReady() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.deps.Media != nil
}

// Close 取消所有正在执行的任务，并等待它们退出。
func (q *Queue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	for _, tc := range q.running {
		tc.mu.Lock()
		tc.reason = "应用退出"
		tc.mu.Unlock()
		tc.cancel()
	}
	q.mu.Unlock()
	q.wg.Wait()
}

// Recover 在应用启动时调用：把上次残留的进行中任务标记为失败。
func (q *Queue) Recover(ctx context.Context) (int, error) {
	return q.deps.Store.MarkInterruptedTasks(ctx)
}

// Running 报告任务是否正在执行。
func (q *Queue) Running(taskID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.running[taskID]
	return ok
}

// Enqueue 把任务放入执行队列并在后台启动。
func (q *Queue) Enqueue(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	switch rec.Status {
	case repository.TaskStatusQueued, repository.TaskStatusCreated:
	case repository.TaskStatusExtracting, repository.TaskStatusTranscribing, repository.TaskStatusTranslating:
		return rec, errors.New("任务正在执行中，无需重复启动")
	case repository.TaskStatusPaused:
		return q.Resume(ctx, taskID)
	case repository.TaskStatusCanceled:
		return rec, errors.New("任务已取消，请使用重试重新开始")
	case repository.TaskStatusCompleted:
		return rec, errors.New("任务已完成，请使用重新翻译")
	default:
		return rec, fmt.Errorf("任务当前状态为 %s，无法启动", statusLabel(rec.Status))
	}

	tc, err := q.register(taskID)
	if err != nil {
		return nil, err
	}
	if err := q.updateStatus(tc.ctx, taskID, repository.TaskStatusQueued, StageQueued, "已加入队列", 0, "等待开始", ""); err != nil {
		q.unregister(taskID)
		return nil, err
	}
	// 登记即视为在运行，避免「刚入队但未开始」被误判为已结束。
	q.launch(tc)
	return q.deps.Store.GetTask(ctx, taskID)
}

// Start 是 Enqueue 的别名，供 API 层直接对应前端命名。
func (q *Queue) Start(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	return q.Enqueue(ctx, taskID)
}

// Retry 把失败/取消的任务重置进度后重新入队。
func (q *Queue) Retry(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if q.busy(ctx, taskID) {
		return rec, errors.New("任务正在执行中，不能重试")
	}
	if err := q.deps.Store.UpdateTaskStatus(ctx, taskID, repository.TaskStatusQueued, StageQueued, "重新排队", 0, "准备重试", ""); err != nil {
		return nil, err
	}
	return q.Enqueue(ctx, taskID)
}

// Pause 请求暂停任务。识别阶段的子进程会在下一个进度回调时终止，
// 已经完成的字幕分段会保留在数据库中。
func (q *Queue) Pause(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	tc, ok := q.lookup(taskID)
	if !ok {
		return nil, errors.New("任务没有在运行，无法暂停")
	}
	tc.mu.Lock()
	if tc.paused {
		tc.mu.Unlock()
		return q.deps.Store.GetTask(ctx, taskID)
	}
	tc.paused = true
	// 取消当前执行上下文以打断正在等待的下游调用；恢复时会用新上下文重跑。
	tc.cancel()
	tc.mu.Unlock()

	// 等待执行器真正退出，避免它随后把「已暂停」覆盖成「失败」。
	q.waitStopped(taskID, 3*time.Second)

	if err := q.updateStatus(context.WithoutCancel(ctx), taskID, repository.TaskStatusPaused, StagePaused, "已暂停", 0, "已暂停，可随时继续", ""); err != nil {
		return nil, err
	}
	return q.deps.Store.GetTask(ctx, taskID)
}

// Resume 恢复暂停的任务，从头重跑当前阶段（已完成的部分不会丢失）。
func (q *Queue) Resume(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	if tc, ok := q.lookup(taskID); ok {
		tc.mu.Lock()
		if tc.paused {
			tc.paused = false
			tc.resumeCh <- struct{}{}
		}
		tc.mu.Unlock()
		return q.deps.Store.GetTask(ctx, taskID)
	}
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if rec.Status != repository.TaskStatusPaused {
		return rec, errors.New("任务没有处于暂停状态")
	}
	if err := q.deps.Store.UpdateTaskStatus(ctx, taskID, repository.TaskStatusQueued, StageQueued, "重新排队", 0, "正在恢复", ""); err != nil {
		return nil, err
	}
	return q.Enqueue(ctx, taskID)
}

// Cancel 取消任务：终止子进程并清理中间文件。
func (q *Queue) Cancel(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if _, running := q.lookup(taskID); running {
		q.stop(taskID, "用户取消")
		q.waitStopped(taskID, 3*time.Second)
		// 执行器若已退出，stop 里的状态写入可能被收尾覆盖，这里再保证一次终态。
		if current, err := q.deps.Store.GetTask(context.WithoutCancel(ctx), taskID); err == nil {
			if current.Status != repository.TaskStatusCanceled && current.Status != repository.TaskStatusCompleted {
				if err := q.updateStatus(context.WithoutCancel(ctx), taskID, repository.TaskStatusCanceled, StageCanceled, "已取消", current.Progress, "任务已取消", ""); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if rec.Status == repository.TaskStatusCompleted {
			return rec, errors.New("任务已完成，无法取消")
		}
		if err := q.updateStatus(ctx, taskID, repository.TaskStatusCanceled, StageCanceled, "已取消", rec.Progress, "任务已取消", ""); err != nil {
			return nil, err
		}
	}
	return q.deps.Store.GetTask(ctx, taskID)
}

// Delete 删除任务及其字幕，正在运行的任务需要先取消。
func (q *Queue) Delete(ctx context.Context, taskID string) error {
	if _, running := q.lookup(taskID); running {
		q.stop(taskID, "用户删除任务")
	}
	if err := q.deps.Store.DeleteTask(ctx, taskID); err != nil {
		return err
	}
	q.removeWorkDir(taskID)
	return nil
}

// Retranslate 对已完成任务重新执行翻译阶段。
func (q *Queue) Retranslate(ctx context.Context, taskID string) (*repository.TaskRecord, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	// 允许在「状态刚写完、上下文尚未注销」的瞬间继续操作。
	if q.busy(ctx, taskID) {
		return rec, errors.New("任务正在执行中，请先等待或取消")
	}
	if rec.SubtitleCount == 0 {
		return rec, errors.New("任务还没有字幕，请先完成识别")
	}
	segments, err := q.deps.Store.ListSegments(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for i := range segments {
		segments[i].Translated = ""
		segments[i].Status = repository.SegmentStatusPending
	}
	if err := q.deps.Store.ReplaceSegments(ctx, taskID, segments); err != nil {
		return nil, err
	}
	if err := q.deps.Store.UpdateTaskCounts(ctx, taskID, len(segments), 0, rec.Duration); err != nil {
		return nil, err
	}
	if err := q.deps.Store.UpdateTaskStatus(ctx, taskID, repository.TaskStatusQueued, StageQueued, "重新排队", 0, "准备重新翻译", ""); err != nil {
		return nil, err
	}
	return q.Enqueue(ctx, taskID)
}

// TranslatePending 只翻译当前任务里尚未翻译的条目（校对时的补翻）。
func (q *Queue) TranslatePending(ctx context.Context, taskID string, onProgress func(done, total int)) (int, error) {
	if q.busy(ctx, taskID) {
		return 0, errors.New("任务正在执行中，请稍后再翻译")
	}
	total, err := q.translateStage(ctx, taskID, "", onProgress)
	if err != nil {
		return total, err
	}
	return total, nil
}

// ExportContent 生成指定格式的字幕文本。
func (q *Queue) ExportContent(ctx context.Context, taskID, format string) (string, string, error) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return "", "", err
	}
	segments, err := q.deps.Store.ListSegments(ctx, taskID)
	if err != nil {
		return "", "", err
	}
	if len(segments) == 0 {
		return "", "", errors.New("任务还没有字幕，暂时无法导出")
	}
	normalized := exporter.NormalizeFormat(format)
	if normalized == "" {
		normalized = exporter.NormalizeFormat(rec.OutputFormat)
	}
	if normalized == "" {
		normalized = exporter.FormatSRT
	}
	content, err := exporter.Export(toExporterSegments(segments), exporter.Options{
		Format:        normalized,
		IncludeSource: true,
		UseSource:     true,
	})
	if err != nil {
		return "", "", err
	}
	ext, err := exporter.Extension(normalized)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSpace(rec.Name)
	if name == "" {
		name = "subtitles"
	}
	if !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(ext)) {
		name += "." + ext
	}
	return content, name, nil
}

// toExporterSegments 把仓库记录转换成导出器结构。
func toExporterSegments(records []repository.SegmentRecord) []exporter.Segment {
	out := make([]exporter.Segment, 0, len(records))
	for _, rec := range records {
		out = append(out, exporter.Segment{
			ID:         rec.ID,
			Sequence:   rec.Sequence,
			Start:      rec.Start,
			End:        rec.End,
			Source:     rec.SourceText,
			Translated: rec.Translated,
		})
	}
	return out
}

// register 登记一个执行上下文。
func (q *Queue) register(taskID string) (*taskContext, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, errors.New("应用正在退出，无法启动任务")
	}
	if _, ok := q.running[taskID]; ok {
		return nil, errors.New("任务已经在运行")
	}
	tc := newTaskContext(context.Background(), taskID)
	q.running[taskID] = tc
	return tc, nil
}

// Emit 主动推送一次当前任务状态，供 API 层在启动后刷新界面。
func (q *Queue) Emit(ctx context.Context, taskID string) {
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	q.emit(ctx, rec)
}

// lookup 查询执行上下文。
func (q *Queue) lookup(taskID string) (*taskContext, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	tc, ok := q.running[taskID]
	return tc, ok
}

// unregister 移除执行上下文。
func (q *Queue) unregister(taskID string) {
	q.mu.Lock()
	tc, ok := q.running[taskID]
	if ok {
		delete(q.running, taskID)
	}
	q.mu.Unlock()
	if ok {
		tc.cancel()
	}
}

// waitStopped 等待任务执行完全退出，避免紧随其后的操作与收尾竞争。
//
// 以 stateDone 为准：执行器先写最终状态、再关闭 done、最后才注销上下文，
// 所以能看到「已完成」时也一定等得到收尾。
func (q *Queue) waitStopped(taskID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if q.stateDone(taskID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stateDone 判断执行器的收尾是否已经完成。
func (q *Queue) stateDone(taskID string) bool {
	tc, ok := q.lookup(taskID)
	if !ok {
		return true
	}
	select {
	case <-tc.done:
		return true
	default:
		return false
	}
}

// busy 判断任务是否真的还在执行。
//
// 只看 running 表是不够的：执行器写完终态后、注销上下文前有一段极短窗口，
// 此时任务已经完成，用户理应能立刻重新翻译或删除。因此以数据库里的状态为准——
// 上下文还在但状态已是终态，就认为它已经结束。
func (q *Queue) busy(ctx context.Context, taskID string) bool {
	if _, ok := q.lookup(taskID); !ok {
		return false
	}
	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		// 读不到状态时保守地当作仍在运行，避免两个执行器同时跑同一任务。
		return true
	}
	return !isTerminalStatus(rec.Status)
}

// isTerminalStatus 判断任务状态是否已经结束。
func isTerminalStatus(status string) bool {
	switch status {
	case repository.TaskStatusCompleted, repository.TaskStatusFailed, repository.TaskStatusCanceled:
		return true
	}
	return false
}

// stop 取消任务并记录原因。
func (q *Queue) stop(taskID, reason string) {
	tc, ok := q.lookup(taskID)
	if !ok {
		return
	}
	tc.mu.Lock()
	tc.reason = reason
	tc.mu.Unlock()
	tc.cancel()
	// 立即写入最终状态，避免执行器收尾时把状态覆盖成失败。
	ctx := context.Background()
	current, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	status, stage, label := repository.TaskStatusCanceled, StageCanceled, "已取消"
	if reason != "用户取消" {
		status, stage, label = repository.TaskStatusFailed, StageFailed, reason
	}
	if current.Status == repository.TaskStatusCompleted {
		return
	}
	_ = q.updateStatus(ctx, taskID, status, stage, label, current.Progress, label, "")
}

// launch 在后台执行任务。
func (q *Queue) launch(tc *taskContext) {
	q.wg.Add(1)
	// 先占用并发槽位，保证 Enqueue 返回时任务已进入执行队列。
	q.slots <- struct{}{}
	go func() {
		defer q.wg.Done()
		defer func() { <-q.slots }()
		q.execute(tc)
	}()
}

// updateStatus 写入状态并推送事件。
func (q *Queue) updateStatus(ctx context.Context, taskID, status, stage, currentStage string, progress int, message, errMsg string) error {
	if err := q.deps.Store.UpdateTaskStatus(ctx, taskID, status, stage, currentStage, progress, message, errMsg); err != nil {
		return err
	}
	q.Emit(ctx, taskID)
	return nil
}

// emit 调用所有事件发射器。
func (q *Queue) emit(ctx context.Context, rec *repository.TaskRecord) {
	if rec == nil {
		return
	}
	for _, emitter := range q.deps.Emitters {
		if emitter == nil {
			continue
		}
		emitter(ctx, rec)
	}
}

// workDir 返回任务的临时工作目录。
func (q *Queue) workDir(taskID string) string {
	if q.deps.WorkDir == "" {
		return ""
	}
	return filepath.Join(q.deps.WorkDir, taskID)
}

// removeWorkDir 清理任务临时目录。
func (q *Queue) removeWorkDir(taskID string) {
	dir := q.workDir(taskID)
	if dir == "" {
		return
	}
	_ = os.RemoveAll(dir)
}

// statusLabel 把状态码转成中文。
func statusLabel(status string) string {
	switch status {
	case repository.TaskStatusCreated:
		return "已创建"
	case repository.TaskStatusQueued:
		return "排队中"
	case repository.TaskStatusExtracting:
		return "提取音频"
	case repository.TaskStatusTranscribing:
		return "识别中"
	case repository.TaskStatusTranslating:
		return "翻译中"
	case repository.TaskStatusPaused:
		return "已暂停"
	case repository.TaskStatusCompleted:
		return "已完成"
	case repository.TaskStatusFailed:
		return "失败"
	case repository.TaskStatusCanceled:
		return "已取消"
	default:
		return status
	}
}

// clamp 把进度限制在 [lo, hi] 区间。
func clamp(value, lo, hi int) int {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

// clampFloat 把比例限制在 [0, 1]。
func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// toASRSegments 把识别结果转换成仓库记录。
func toASRSegments(taskID string, result *asr.Result) []repository.SegmentRecord {
	if result == nil {
		return nil
	}
	out := make([]repository.SegmentRecord, 0, len(result.Segments))
	sequence := 0
	for _, seg := range result.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		sequence++
		start, end := normalizeTiming(seg.Start, seg.End)
		id := strings.TrimSpace(seg.ID)
		if id == "" {
			id = fmt.Sprintf("%s-%04d", taskID, sequence)
		}
		out = append(out, repository.SegmentRecord{
			ID:         taskID + ":" + id,
			TaskID:     taskID,
			Sequence:   sequence,
			Start:      start,
			End:        end,
			SourceText: text,
			Translated: "",
			Status:     repository.SegmentStatusPending,
		})
	}
	return out
}

// normalizeTiming 修正非法时间轴：负数归零，结束早于开始时取相同值并留 100ms。
func normalizeTiming(start, end float64) (float64, float64) {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end-start < 0.1 {
		end = start + 0.1
	}
	return start, end
}

// 编译期断言：确保 media.FFmpeg 满足 MediaExtractor。
var _ MediaExtractor = (*media.FFmpeg)(nil)

// ManagerAdapter 把 modelmanager.Manager 适配成 ModelResolver。
type ManagerAdapter struct{ Manager modelmanager.Manager }

// Ensure 实现 ModelResolver。
func (m ManagerAdapter) Ensure(ctx context.Context, modelID string, progress modelmanager.Progress) (string, error) {
	if m.Manager == nil {
		return "", errors.New("模型管理器未初始化")
	}
	return m.Manager.Ensure(ctx, modelID, progress)
}

// 编译期断言：确保 ManagerAdapter 满足 ModelResolver。
var _ ModelResolver = ManagerAdapter{}
