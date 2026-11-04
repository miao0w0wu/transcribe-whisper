package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 说明：本文件中所有写操作都在 s.mu 保护下执行，配合 Open 里的
// SetMaxOpenConns(1)，保证同一时刻只有一个事务占用连接；读取不取锁，
// 因为连接池只有一个连接，读操作本身也是串行执行的。

const interruptedMessage = "应用退出导致任务中断"

// taskColumns 是 tasks 表查询列，顺序必须与 scanTask 的扫描顺序一致。
const taskColumns = `id, name, source_file, source_language, target_language, status, stage,
	progress, current_stage, subtitle_count, translated_count, message, error, engine, model,
	provider, output_format, duration, created_at, updated_at`

// scanTask 从一行结果里读取 TaskRecord。
func scanTask(scanner interface{ Scan(...any) error }) (*TaskRecord, error) {
	var (
		rec      TaskRecord
		created  string
		updated  string
		duration float64
	)
	if err := scanner.Scan(
		&rec.ID, &rec.Name, &rec.SourceFile, &rec.SourceLanguage, &rec.TargetLanguage, &rec.Status,
		&rec.Stage, &rec.Progress, &rec.CurrentStage, &rec.SubtitleCount, &rec.TranslatedCount,
		&rec.Message, &rec.Error, &rec.Engine, &rec.Model, &rec.Provider, &rec.OutputFormat,
		&duration, &created, &updated,
	); err != nil {
		return nil, err
	}
	rec.Duration = duration
	createdAt, err := parseTime(created)
	if err != nil {
		return nil, fmt.Errorf("解析任务创建时间: %w", err)
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return nil, fmt.Errorf("解析任务更新时间: %w", err)
	}
	rec.CreatedAt = createdAt
	rec.UpdatedAt = updatedAt
	return &rec, nil
}

// CreateTask 插入一条任务记录。CreatedAt/UpdatedAt 为空时自动填当前 UTC 时间。
func (s *Store) CreateTask(ctx context.Context, rec *TaskRecord) error {
	if rec == nil {
		return errors.New("创建任务失败: 记录不能为空")
	}
	if rec.ID == "" {
		return errors.New("创建任务失败: 任务 ID 不能为空")
	}
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = rec.CreatedAt
	}
	if rec.Status == "" {
		rec.Status = TaskStatusCreated
	}
	if rec.OutputFormat == "" {
		rec.OutputFormat = "srt"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (
		id, name, source_file, source_language, target_language, status, stage, progress,
		current_stage, subtitle_count, translated_count, message, error, engine, model, provider,
		output_format, duration, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Name, rec.SourceFile, rec.SourceLanguage, rec.TargetLanguage, rec.Status,
		rec.Stage, rec.Progress, rec.CurrentStage, rec.SubtitleCount, rec.TranslatedCount,
		rec.Message, rec.Error, rec.Engine, rec.Model, rec.Provider, rec.OutputFormat,
		rec.Duration, formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("创建任务失败: %w", err)
	}
	return nil
}

// GetTask 按 id 查询任务，不存在返回 ErrNotFound。
func (s *Store) GetTask(ctx context.Context, id string) (*TaskRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	rec, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("查询任务 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("查询任务 %s 失败: %w", id, err)
	}
	return rec, nil
}

// ListTasks 列出全部任务，按创建时间倒序。
func (s *Store) ListTasks(ctx context.Context) ([]*TaskRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY created_at DESC, rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %w", err)
	}
	defer rows.Close()

	tasks := make([]*TaskRecord, 0)
	for rows.Next() {
		rec, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("读取任务记录失败: %w", err)
		}
		tasks = append(tasks, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历任务列表失败: %w", err)
	}
	return tasks, nil
}

// UpdateTaskStatus 更新任务状态、阶段、进度、提示信息与错误信息，并刷新 updated_at。
func (s *Store) UpdateTaskStatus(ctx context.Context, id, status, stage, currentStage string, progress int, message, errMsg string) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, stage = ?, current_stage = ?,
		progress = ?, message = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, stage, currentStage, progress, message, errMsg, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}
	return affectedOrNotFound(res, "更新任务状态", id)
}

// UpdateTaskCounts 更新字数统计与媒体时长。
func (s *Store) UpdateTaskCounts(ctx context.Context, id string, subtitleCount, translatedCount int, duration float64) error {
	if subtitleCount < 0 {
		subtitleCount = 0
	}
	if translatedCount < 0 {
		translatedCount = 0
	}
	if duration < 0 {
		duration = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET subtitle_count = ?, translated_count = ?,
		duration = ?, updated_at = ? WHERE id = ?`,
		subtitleCount, translatedCount, duration, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("更新任务统计失败: %w", err)
	}
	return affectedOrNotFound(res, "更新任务统计", id)
}

// DeleteTask 删除任务及其全部字幕。
//
// SQLite 的 ON DELETE CASCADE 需要 PRAGMA foreign_keys=ON 才生效，而契约文件里
// 没有开启该编译开关，因此这里在同一个事务内显式删除字幕，保证级联语义。
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("删除任务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 注意：事务持有期间只能使用 tx 句柄查询，否则单连接池会死锁。
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE id = ?`, id).Scan(&exists); err != nil {
		return fmt.Errorf("删除任务前检查任务是否存在失败: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("删除任务 %s: %w", id, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM subtitle_segments WHERE task_id = ?`, id); err != nil {
		return fmt.Errorf("删除任务字幕失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除任务失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交删除任务事务失败: %w", err)
	}
	return nil
}

// MarkInterruptedTasks 把上次异常退出时残留的进行中任务标记为失败，返回受影响行数。
func (s *Store) MarkInterruptedTasks(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, error = ?, message = ?, updated_at = ?
		WHERE status IN (?, ?, ?)`,
		TaskStatusFailed, interruptedMessage, interruptedMessage, formatTime(time.Now().UTC()),
		TaskStatusExtracting, TaskStatusTranscribing, TaskStatusTranslating)
	if err != nil {
		return 0, fmt.Errorf("标记中断任务失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("统计中断任务数量失败: %w", err)
	}
	return int(affected), nil
}

// affectedOrNotFound 统一把「影响行数为 0」转换成 ErrNotFound。
func affectedOrNotFound(res sql.Result, action, id string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s失败: %w", action, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s %s: %w", action, id, ErrNotFound)
	}
	return nil
}
