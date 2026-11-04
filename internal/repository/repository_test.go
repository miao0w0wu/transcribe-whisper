package repository

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestStore 打开一个内存库并注册清理。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭数据库失败: %v", err)
		}
	})
	return store
}

// openTestStoreFile 打开一个临时目录下的文件数据库（用于验证真实落盘与迁移）。
func openTestStoreFile(t *testing.T, dir string) *Store {
	t.Helper()
	path := filepath.Join(dir, "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开文件数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTask(id string, createdAt time.Time) *TaskRecord {
	return &TaskRecord{
		ID:             id,
		Name:           "示例任务 " + id,
		SourceFile:     "C:/media/" + id + ".mp4",
		SourceLanguage: "auto",
		TargetLanguage: "zh-CN",
		Status:         TaskStatusCreated,
		Stage:          TaskStatusQueued,
		CurrentStage:   "等待识别",
		Engine:         "faster-whisper",
		Model:          "small",
		Provider:       "ollama",
		OutputFormat:   "srt",
		CreatedAt:      createdAt,
	}
}

func mustCreateTask(t *testing.T, store *Store, rec *TaskRecord) {
	t.Helper()
	if err := store.CreateTask(context.Background(), rec); err != nil {
		t.Fatalf("创建任务 %s 失败: %v", rec.ID, err)
	}
}

func TestCreateAndGetTask(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	created := time.Date(2024, 5, 1, 8, 30, 0, 0, time.UTC)
	rec := newTask("task-1", created)
	rec.Duration = 12.5
	mustCreateTask(t, store, rec)

	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if got.ID != "task-1" || got.Name != rec.Name || got.SourceFile != rec.SourceFile {
		t.Fatalf("基础字段不符: %+v", got)
	}
	if got.SourceLanguage != "auto" || got.TargetLanguage != "zh-CN" || got.Status != TaskStatusCreated {
		t.Fatalf("语言/状态字段不符: %+v", got)
	}
	if got.Engine != "faster-whisper" || got.Model != "small" || got.Provider != "ollama" || got.OutputFormat != "srt" {
		t.Fatalf("引擎相关字段不符: %+v", got)
	}
	if got.Duration != 12.5 {
		t.Fatalf("时长不符: %v", got.Duration)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("创建时间不符: got=%v want=%v", got.CreatedAt, created)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("创建时间应为 UTC: %v", got.CreatedAt.Location())
	}
	if !got.UpdatedAt.Equal(created) {
		t.Fatalf("未显式给更新时间时应回落到创建时间: %v", got.UpdatedAt)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.GetTask(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在任务应返回 ErrNotFound，实际: %v", err)
	}
}

func TestCreateTaskRejectsEmptyID(t *testing.T) {
	store := openTestStore(t)
	if err := store.CreateTask(context.Background(), &TaskRecord{}); err == nil {
		t.Fatal("空任务 ID 应被拒绝")
	}
	if err := store.CreateTask(context.Background(), nil); err == nil {
		t.Fatal("nil 任务应被拒绝")
	}
}

func TestListTasksOrderByCreatedAtDesc(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	base := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)
	mustCreateTask(t, store, newTask("old", base))
	mustCreateTask(t, store, newTask("newest", base.Add(2*time.Hour)))
	mustCreateTask(t, store, newTask("middle", base.Add(1*time.Hour)))

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("列出任务失败: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("任务数量不符: %d", len(tasks))
	}
	wantOrder := []string{"newest", "middle", "old"}
	for i, id := range wantOrder {
		if tasks[i].ID != id {
			t.Fatalf("第 %d 个任务应为 %s，实际 %s（顺序 %v）", i, id, tasks[i].ID, ids(tasks))
		}
	}
}

func TestListTasksEmpty(t *testing.T) {
	store := openTestStore(t)
	tasks, err := store.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("列出空任务失败: %v", err)
	}
	if tasks == nil {
		t.Fatal("空列表应返回空切片而不是 nil")
	}
	if len(tasks) != 0 {
		t.Fatalf("空库不应有任务: %d", len(tasks))
	}
}

func ids(tasks []*TaskRecord) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}

func TestUpdateTaskStatus(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	created := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)
	mustCreateTask(t, store, newTask("task-1", created))

	before, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}

	if err := store.UpdateTaskStatus(ctx, "task-1", TaskStatusTranscribing, "asr", "正在识别音频", 45, "识别中", ""); err != nil {
		t.Fatalf("更新任务状态失败: %v", err)
	}
	after, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if after.Status != TaskStatusTranscribing || after.Stage != "asr" || after.CurrentStage != "正在识别音频" {
		t.Fatalf("状态字段未更新: %+v", after)
	}
	if after.Progress != 45 || after.Message != "识别中" || after.Error != "" {
		t.Fatalf("进度/提示字段未更新: %+v", after)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at 应被刷新: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
	if !after.CreatedAt.Equal(created) {
		t.Fatalf("created_at 不应被修改: %v", after.CreatedAt)
	}
}

func TestUpdateTaskStatusClampsProgress(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	if err := store.UpdateTaskStatus(ctx, "task-1", TaskStatusFailed, "asr", "失败", -20, "err", "boom"); err != nil {
		t.Fatalf("更新任务状态失败: %v", err)
	}
	got, _ := store.GetTask(ctx, "task-1")
	if got.Progress != 0 {
		t.Fatalf("负进度应被夹到 0，实际 %d", got.Progress)
	}
	if err := store.UpdateTaskStatus(ctx, "task-1", TaskStatusCompleted, "done", "完成", 250, "ok", ""); err != nil {
		t.Fatalf("更新任务状态失败: %v", err)
	}
	got, _ = store.GetTask(ctx, "task-1")
	if got.Progress != 100 {
		t.Fatalf("超过 100 的进度应被夹到 100，实际 %d", got.Progress)
	}
}

func TestUpdateTaskStatusNotFound(t *testing.T) {
	store := openTestStore(t)
	err := store.UpdateTaskStatus(context.Background(), "missing", TaskStatusFailed, "", "", 0, "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的任务应返回 ErrNotFound，实际: %v", err)
	}
}

func TestUpdateTaskCounts(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	if err := store.UpdateTaskCounts(ctx, "task-1", 12, 5, 63.25); err != nil {
		t.Fatalf("更新任务统计失败: %v", err)
	}
	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if got.SubtitleCount != 12 || got.TranslatedCount != 5 || got.Duration != 63.25 {
		t.Fatalf("统计字段不符: %+v", got)
	}

	if err := store.UpdateTaskCounts(ctx, "missing", 1, 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在任务的统计应返回 ErrNotFound，实际: %v", err)
	}
}

func TestDeleteTaskCascade(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	segs := []SegmentRecord{
		{ID: "s1", Sequence: 1, Start: 0, End: 1, SourceText: "A", Translated: "甲", Status: SegmentStatusTranslated},
		{ID: "s2", Sequence: 2, Start: 1, End: 2, SourceText: "B", Translated: "乙", Status: SegmentStatusTranslated},
	}
	if err := store.ReplaceSegments(ctx, "task-1", segs); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}

	if err := store.DeleteTask(ctx, "task-1"); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if _, err := store.GetTask(ctx, "task-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后任务应不存在，实际: %v", err)
	}
	remaining, err := store.ListSegments(ctx, "task-1")
	if err != nil {
		t.Fatalf("查询字幕失败: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("删除任务应级联删除字幕，残留 %d 条", len(remaining))
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	store := openTestStore(t)
	if err := store.DeleteTask(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在任务应返回 ErrNotFound，实际: %v", err)
	}
}

func TestMarkInterruptedTasks(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)

	states := map[string]string{
		"extracting":   TaskStatusExtracting,
		"transcribing": TaskStatusTranscribing,
		"translating":  TaskStatusTranslating,
		"queued":       TaskStatusQueued,
		"paused":       TaskStatusPaused,
		"completed":    TaskStatusCompleted,
		"failed":       TaskStatusFailed,
		"canceled":     TaskStatusCanceled,
	}
	i := 0
	for id, status := range states {
		rec := newTask(id, base.Add(time.Duration(i)*time.Minute))
		rec.Status = status
		i++
		mustCreateTask(t, store, rec)
	}

	affected, err := store.MarkInterruptedTasks(ctx)
	if err != nil {
		t.Fatalf("标记中断任务失败: %v", err)
	}
	if affected != 3 {
		t.Fatalf("应只影响 3 个进行中任务，实际 %d", affected)
	}

	for id, status := range states {
		got, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("读取任务 %s 失败: %v", id, err)
		}
		switch status {
		case TaskStatusExtracting, TaskStatusTranscribing, TaskStatusTranslating:
			if got.Status != TaskStatusFailed {
				t.Fatalf("进行中任务 %s 应标记为 failed，实际 %s", id, got.Status)
			}
			if got.Error != "应用退出导致任务中断" {
				t.Fatalf("任务 %s 的错误信息不符: %q", id, got.Error)
			}
		default:
			if got.Status != status {
				t.Fatalf("非进行中任务 %s 不应被修改，实际 %s", id, got.Status)
			}
			if got.Error != "" {
				t.Fatalf("非进行中任务 %s 不应写入错误信息: %q", id, got.Error)
			}
		}
	}

	// 再次执行不应有新的影响。
	again, err := store.MarkInterruptedTasks(ctx)
	if err != nil {
		t.Fatalf("再次标记中断任务失败: %v", err)
	}
	if again != 0 {
		t.Fatalf("重复标记应影响 0 行，实际 %d", again)
	}
}

func TestReplaceSegmentsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	segs := []SegmentRecord{
		{ID: "s2", Sequence: 2, Start: 3.5, End: 5.25, SourceText: "world", Translated: "世界", Status: SegmentStatusTranslated},
		{ID: "s1", Sequence: 1, Start: 0, End: 3.5, SourceText: "hello", Translated: "你好", Status: SegmentStatusTranslated},
	}
	if err := store.ReplaceSegments(ctx, "task-1", segs); err != nil {
		t.Fatalf("替换字幕失败: %v", err)
	}

	got, err := store.ListSegments(ctx, "task-1")
	if err != nil {
		t.Fatalf("查询字幕失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("字幕数量不符: %d", len(got))
	}
	// 按 sequence 升序返回，与写入顺序无关。
	if got[0].ID != "s1" || got[1].ID != "s2" {
		t.Fatalf("字幕应按序号升序返回: %+v", got)
	}
	if got[0].TaskID != "task-1" || got[0].SourceText != "hello" || got[0].Translated != "你好" {
		t.Fatalf("字幕字段不符: %+v", got[0])
	}
	if got[0].Start != 0 || got[0].End != 3.5 || got[1].Start != 3.5 || got[1].End != 5.25 {
		t.Fatalf("时间字段不符: %+v", got)
	}
	if got[0].Status != SegmentStatusTranslated {
		t.Fatalf("状态字段不符: %q", got[0].Status)
	}

	// 再次替换应整体覆盖旧内容（segs[:1] 是切片首元素 s2）。
	if err := store.ReplaceSegments(ctx, "task-1", segs[:1]); err != nil {
		t.Fatalf("再次替换字幕失败: %v", err)
	}
	got, _ = store.ListSegments(ctx, "task-1")
	if len(got) != 1 || got[0].ID != "s2" {
		t.Fatalf("替换后应只剩 1 条 s2，实际 %+v", got)
	}
}

func TestReplaceSegmentsEmptyClears(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))
	segs := []SegmentRecord{{ID: "s1", Sequence: 1, Start: 0, End: 1, SourceText: "a", Status: SegmentStatusPending}}
	if err := store.ReplaceSegments(ctx, "task-1", segs); err != nil {
		t.Fatalf("替换字幕失败: %v", err)
	}
	if err := store.ReplaceSegments(ctx, "task-1", nil); err != nil {
		t.Fatalf("清空字幕失败: %v", err)
	}
	got, _ := store.ListSegments(ctx, "task-1")
	if len(got) != 0 {
		t.Fatalf("空切片替换应清空字幕，残留 %d", len(got))
	}
}

func TestReplaceSegmentsValidation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		segs []SegmentRecord
		want string
	}{
		{
			name: "字幕 ID 为空",
			segs: []SegmentRecord{{ID: "", Sequence: 1, Start: 0, End: 1}},
			want: "字幕 ID 为空",
		},
		{
			name: "字幕 ID 重复",
			segs: []SegmentRecord{
				{ID: "dup", Sequence: 1, Start: 0, End: 1},
				{ID: "dup", Sequence: 2, Start: 1, End: 2},
			},
			want: "重复",
		},
		{
			name: "序号为 0",
			segs: []SegmentRecord{{ID: "s1", Sequence: 0, Start: 0, End: 1}},
			want: "必须大于等于 1",
		},
		{
			name: "序号重复",
			segs: []SegmentRecord{
				{ID: "s1", Sequence: 1, Start: 0, End: 1},
				{ID: "s2", Sequence: 1, Start: 1, End: 2},
			},
			want: "序号 1 重复",
		},
		{
			name: "开始时间为负",
			segs: []SegmentRecord{{ID: "s1", Sequence: 1, Start: -0.1, End: 1}},
			want: "开始时间",
		},
		{
			name: "结束早于开始",
			segs: []SegmentRecord{{ID: "s1", Sequence: 1, Start: 2, End: 1}},
			want: "结束时间",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := openTestStore(t)
			mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))
			// 先写入一批合法数据，验证校验失败时不会破坏已有数据（整体回滚）。
			existing := []SegmentRecord{{ID: "keep", Sequence: 1, Start: 0, End: 1, SourceText: "保留", Status: SegmentStatusPending}}
			if err := store.ReplaceSegments(ctx, "task-1", existing); err != nil {
				t.Fatalf("预置字幕失败: %v", err)
			}

			err := store.ReplaceSegments(ctx, "task-1", c.segs)
			if err == nil {
				t.Fatalf("非法字幕应被拒绝")
			}
			if !contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际: %v", c.want, err)
			}
			if !contains(err.Error(), "校验字幕失败") {
				t.Fatalf("错误信息应为中文包装，实际: %v", err)
			}
			got, err := store.ListSegments(ctx, "task-1")
			if err != nil {
				t.Fatalf("查询字幕失败: %v", err)
			}
			if len(got) != 1 || got[0].ID != "keep" {
				t.Fatalf("校验失败后应保留原有字幕，实际 %+v", got)
			}
		})
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestReplaceSegmentsEmptyTaskID(t *testing.T) {
	store := openTestStore(t)
	if err := store.ReplaceSegments(context.Background(), "", nil); err == nil {
		t.Fatal("空任务 ID 应被拒绝")
	}
}

func TestUpsertSegmentsBatches(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	batch1 := []SegmentRecord{
		{ID: "s1", Sequence: 1, Start: 0, End: 1, SourceText: "第一段", Status: SegmentStatusPending},
		{ID: "s2", Sequence: 2, Start: 1, End: 2, SourceText: "第二段", Status: SegmentStatusPending},
	}
	if err := store.UpsertSegments(ctx, "task-1", batch1); err != nil {
		t.Fatalf("分批写入失败: %v", err)
	}
	batch2 := []SegmentRecord{
		{ID: "s3", Sequence: 3, Start: 2, End: 3, SourceText: "第三段", Status: SegmentStatusPending},
	}
	if err := store.UpsertSegments(ctx, "task-1", batch2); err != nil {
		t.Fatalf("第二批写入失败: %v", err)
	}
	got, _ := store.ListSegments(ctx, "task-1")
	if len(got) != 3 {
		t.Fatalf("两批写入后应有 3 条，实际 %d", len(got))
	}

	// 同一 ID 再写入应覆盖而不是插入新行。
	overwrite := []SegmentRecord{
		{ID: "s2", Sequence: 2, Start: 1, End: 2.5, SourceText: "第二段（修正）", Translated: "第二段译文", Status: SegmentStatusTranslated},
	}
	if err := store.UpsertSegments(ctx, "task-1", overwrite); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}
	got, _ = store.ListSegments(ctx, "task-1")
	if len(got) != 3 {
		t.Fatalf("覆盖写入不应新增行，实际 %d", len(got))
	}
	if got[1].SourceText != "第二段（修正）" || got[1].Translated != "第二段译文" || got[1].End != 2.5 {
		t.Fatalf("覆盖写入字段不符: %+v", got[1])
	}
	if got[1].Status != SegmentStatusTranslated {
		t.Fatalf("覆盖写入状态不符: %q", got[1].Status)
	}
}

func TestUpsertSegmentsEmptyAndValidation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	if err := store.UpsertSegments(ctx, "task-1", nil); err != nil {
		t.Fatalf("空切片应视为无操作，实际: %v", err)
	}
	if err := store.UpsertSegments(ctx, "task-1", []SegmentRecord{{ID: "s1", Sequence: 2, Start: 5, End: 4}}); err == nil {
		t.Fatal("非法时间应被拒绝")
	}
	if err := store.UpsertSegments(ctx, "task-1", []SegmentRecord{{ID: "s1", Sequence: 1, Start: 0, End: 1}}); err != nil {
		t.Fatalf("合法写入失败: %v", err)
	}
	got, _ := store.ListSegments(ctx, "task-1")
	if len(got) != 1 {
		t.Fatalf("校验失败不应留下脏数据，实际 %d 条", len(got))
	}
}

func TestUpdateSegmentTranslation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))
	segs := []SegmentRecord{
		{ID: "s1", Sequence: 1, Start: 0, End: 1, SourceText: "hello", Status: SegmentStatusPending},
		{ID: "s2", Sequence: 2, Start: 1, End: 2, SourceText: "world", Status: SegmentStatusPending},
	}
	if err := store.ReplaceSegments(ctx, "task-1", segs); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}

	if err := store.UpdateSegmentTranslation(ctx, "task-1", "s1", "你好", SegmentStatusTranslated); err != nil {
		t.Fatalf("更新译文失败: %v", err)
	}
	got, _ := store.ListSegments(ctx, "task-1")
	if got[0].Translated != "你好" || got[0].Status != SegmentStatusTranslated {
		t.Fatalf("译文字段不符: %+v", got[0])
	}
	if got[1].Translated != "" || got[1].Status != SegmentStatusPending {
		t.Fatalf("未更新的字幕不应变化: %+v", got[1])
	}

	if err := store.UpdateSegmentTranslation(ctx, "task-1", "missing", "x", SegmentStatusTranslated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在字幕应返回 ErrNotFound，实际: %v", err)
	}
	// 任务 ID 与字幕 ID 必须同时匹配，避免跨任务误改。
	if err := store.UpdateSegmentTranslation(ctx, "other-task", "s1", "x", SegmentStatusTranslated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨任务更新应返回 ErrNotFound，实际: %v", err)
	}
}

func TestTranslatePendingSegments(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))
	mustCreateTask(t, store, newTask("task-2", time.Now().UTC()))

	segs := []SegmentRecord{
		{ID: "a3", Sequence: 3, Start: 2, End: 3, SourceText: "c", Status: SegmentStatusPending},
		{ID: "a1", Sequence: 1, Start: 0, End: 1, SourceText: "a", Translated: "甲", Status: SegmentStatusTranslated},
		{ID: "a2", Sequence: 2, Start: 1, End: 2, SourceText: "b", Status: SegmentStatusPending},
		{ID: "a4", Sequence: 4, Start: 3, End: 4, SourceText: "d", Status: SegmentStatusFailed},
		{ID: "a5", Sequence: 5, Start: 4, End: 5, SourceText: "e", Status: SegmentStatusPending},
	}
	if err := store.ReplaceSegments(ctx, "task-1", segs); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}
	// 另一个任务的待翻译字幕不应混入。
	if err := store.ReplaceSegments(ctx, "task-2", []SegmentRecord{
		{ID: "b1", Sequence: 1, Start: 0, End: 1, SourceText: "x", Status: SegmentStatusPending},
	}); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}

	got, err := store.TranslatePendingSegments(ctx, "task-1", 10)
	if err != nil {
		t.Fatalf("查询待翻译字幕失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("待翻译字幕应有 3 条，实际 %d", len(got))
	}
	wantOrder := []string{"a2", "a3", "a5"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Fatalf("第 %d 条待翻译字幕应为 %s，实际 %s", i, id, got[i].ID)
		}
		if got[i].Status != SegmentStatusPending {
			t.Fatalf("只应返回 pending 状态，实际 %q", got[i].Status)
		}
	}

	limited, err := store.TranslatePendingSegments(ctx, "task-1", 2)
	if err != nil {
		t.Fatalf("查询待翻译字幕失败: %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "a2" || limited[1].ID != "a3" {
		t.Fatalf("limit 生效结果不符: %+v", limited)
	}

	// 全部翻译完后应为空。
	for _, seg := range got {
		if err := store.UpdateSegmentTranslation(ctx, "task-1", seg.ID, "译文", SegmentStatusTranslated); err != nil {
			t.Fatalf("更新译文失败: %v", err)
		}
	}
	empty, err := store.TranslatePendingSegments(ctx, "task-1", 10)
	if err != nil {
		t.Fatalf("查询待翻译字幕失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("翻译完成后不应还有待翻译字幕: %+v", empty)
	}
}

func TestLoadSettingsDefaults(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.AsrEngine != "faster-whisper" || got.AsrDevice != "auto" || got.ComputeType != "auto" {
		t.Fatalf("识别默认值不符: %+v", got)
	}
	if got.DefaultModel != "small" || got.Mirror != "hf-mirror" || got.TranslationProvider != "ollama" {
		t.Fatalf("模型/镜像/翻译默认值不符: %+v", got)
	}
	if got.HuggingFaceEndpoint != "" {
		t.Fatalf("HuggingFaceEndpoint 默认应为空，实际 %q", got.HuggingFaceEndpoint)
	}
	if got.CloudPrivacyConfirm {
		t.Fatal("CloudPrivacyConfirm 默认应为 false")
	}

	// 默认值应已落库：直接查询也能读到同一行。
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(1) FROM settings").Scan(&count); err != nil {
		t.Fatalf("查询设置行数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("默认设置应落库为 1 行，实际 %d", count)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	want := SettingsRecord{
		FFmpegPath:          `C:\tools\ffmpeg.exe`,
		PythonPath:          `C:\Python311\python.exe`,
		ModelDir:            `D:\models\whisper`,
		Mirror:              "huggingface",
		HuggingFaceEndpoint: "https://hf-mirror.com",
		AsrEngine:           "openai-whisper",
		AsrDevice:           "cuda",
		ComputeType:         "float16",
		DefaultModel:        "medium",
		TranslationProvider: "openai",
		TranslationEndpoint: "https://api.example.com/v1",
		TranslationModel:    "gpt-4o-mini",
		TranslationAPIKey:   "sk-test",
		CloudPrivacyConfirm: true,
		TranslationStyle:    "subtitle",
		Glossary:            map[string]string{"API": "接口"},
	}
	if err := store.SaveSettings(ctx, want); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}

	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("设置往返不一致\n got=%+v\nwant=%+v", got, want)
	}

	// 再保存一次应走 UPDATE 分支，仍然是单行。
	want.DefaultModel = "large-v3"
	want.CloudPrivacyConfirm = false
	if err := store.SaveSettings(ctx, want); err != nil {
		t.Fatalf("再次保存设置失败: %v", err)
	}
	got, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("二次保存结果不符\n got=%+v\nwant=%+v", got, want)
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(1) FROM settings").Scan(&count); err != nil {
		t.Fatalf("查询设置行数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("UPSERT 应保持单行，实际 %d 行", count)
	}

	// CloudPrivacyConfirm 以 0/1 存储。
	var privacy int
	if err := store.DB().QueryRow("SELECT cloud_privacy_confirm FROM settings WHERE id = 1").Scan(&privacy); err != nil {
		t.Fatalf("查询隐私确认字段失败: %v", err)
	}
	if privacy != 0 {
		t.Fatalf("CloudPrivacyConfirm=false 应存为 0，实际 %d", privacy)
	}
}

func TestLoadSettingsDetectsLegacyRow(t *testing.T) {
	// 已存在设置行时不应被默认值覆盖。
	store := openTestStore(t)
	ctx := context.Background()
	custom := DefaultSettings()
	custom.DefaultModel = "large-v3"
	if err := store.SaveSettings(ctx, custom); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.DefaultModel != "large-v3" {
		t.Fatalf("已有设置被默认值覆盖: %+v", got)
	}
}

func TestLegacyDatabaseMigration(t *testing.T) {
	// 手工建一个只有旧列的 tasks 表（模拟 app.go 早期版本创建的库），
	// 再通过 Open 打开，应当成功补齐新列且不丢数据。
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		source_file TEXT NOT NULL,
		source_language TEXT NOT NULL,
		target_language TEXT NOT NULL,
		status TEXT NOT NULL,
		progress INTEGER NOT NULL DEFAULT 0,
		current_stage TEXT NOT NULL,
		subtitle_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("创建旧 tasks 表失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO tasks (id, name, source_file, source_language, target_language, status, progress, current_stage, subtitle_count, created_at)
		VALUES ('legacy-1', '旧任务', 'C:/old.mp4', 'auto', 'zh-CN', 'transcribing', 40, '正在识别', 3, '2024-01-02T03:04:05Z')`); err != nil {
		t.Fatalf("写入旧数据失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开旧库并迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	got, err := store.GetTask(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("读取迁移后的任务失败: %v", err)
	}
	if got.Name != "旧任务" || got.Status != TaskStatusTranscribing || got.SubtitleCount != 3 {
		t.Fatalf("旧数据字段不符: %+v", got)
	}
	// 新列应使用建表时的默认值。
	if got.Engine != "faster-whisper" || got.Model != "small" || got.Provider != "ollama" || got.OutputFormat != "srt" {
		t.Fatalf("新增列默认值不符: %+v", got)
	}
	if got.Duration != 0 || got.TranslatedCount != 0 || got.Message != "" || got.Error != "" {
		t.Fatalf("新增数值/文本列默认值不符: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		// 旧库补列的 updated_at 默认是空串，解析为空时间属于预期行为。
		t.Log("旧库 updated_at 为空，已按零值处理")
	}

	// 迁移后的库应能正常写入新列。
	if err := store.UpdateTaskStatus(ctx, "legacy-1", TaskStatusFailed, "asr", "已中断", 40, "", interruptedMessage); err != nil {
		t.Fatalf("迁移后更新任务失败: %v", err)
	}
	got, err = store.GetTask(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if got.Status != TaskStatusFailed || got.Error != interruptedMessage || got.UpdatedAt.IsZero() {
		t.Fatalf("迁移后写入新列失败: %+v", got)
	}

	// 旧的 subtitle_segments 表也应在迁移时被创建。
	if err := store.ReplaceSegments(ctx, "legacy-1", []SegmentRecord{
		{ID: "seg-1", Sequence: 1, Start: 0, End: 1, SourceText: "旧字幕", Status: SegmentStatusPending},
	}); err != nil {
		t.Fatalf("迁移后写入字幕失败: %v", err)
	}
	segs, err := store.ListSegments(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("查询字幕失败: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("字幕数量不符: %d", len(segs))
	}

	// 幂等：重复打开同一个库不应报错。
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开数据库失败: %v", err)
	}
	_ = store2.Close()
}

func TestFileStorePersistence(t *testing.T) {
	// 文件库验证：写入后关闭再打开，数据还在。
	dir := t.TempDir()
	store := openTestStoreFile(t, dir)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("persist-1", time.Now().UTC()))
	if err := store.SaveSettings(ctx, DefaultSettings()); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	reopened, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("重新打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.GetTask(ctx, "persist-1"); err != nil {
		t.Fatalf("重新打开后任务丢失: %v", err)
	}
	if _, err := reopened.LoadSettings(ctx); err != nil {
		t.Fatalf("重新打开后设置丢失: %v", err)
	}
}

func TestParseTimeRejectsGarbage(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))
	// 直接写入非法时间字符串，读回应报中文错误而不是静默返回零值。
	if _, err := store.DB().Exec("UPDATE tasks SET created_at = 'not-a-time' WHERE id = ?", "task-1"); err != nil {
		t.Fatalf("写入非法时间失败: %v", err)
	}
	_, err := store.GetTask(ctx, "task-1")
	if err == nil {
		t.Fatal("非法时间应返回错误")
	}
	if !contains(err.Error(), "解析任务创建时间") {
		t.Fatalf("错误信息应为中文包装，实际: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, newTask("task-1", time.Now().UTC()))

	const workers = 8
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			seg := SegmentRecord{
				ID:         "seg-" + string(rune('a'+n)),
				Sequence:   n + 1,
				Start:      float64(n),
				End:        float64(n) + 0.5,
				SourceText: "并发写入",
				Status:     SegmentStatusPending,
			}
			upsertErr := store.UpsertSegments(ctx, "task-1", []SegmentRecord{seg})
			_, listErr := store.ListSegments(ctx, "task-1")
			if upsertErr != nil {
				done <- upsertErr
				return
			}
			done <- listErr
		}(i)
	}
	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发访问出错: %v", err)
		}
	}
	segs, err := store.ListSegments(ctx, "task-1")
	if err != nil {
		t.Fatalf("查询字幕失败: %v", err)
	}
	if len(segs) != workers {
		t.Fatalf("并发写入后应有 %d 条字幕，实际 %d", workers, len(segs))
	}
}
