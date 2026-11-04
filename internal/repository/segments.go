package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// 字幕状态：识别完成后待翻译 / 已翻译 / 翻译失败 / 无需翻译。
const (
	SegmentStatusPending    = "pending"
	SegmentStatusTranslated = "translated"
	SegmentStatusFailed     = "failed"
	SegmentStatusSkipped    = "skipped"
)

const segmentColumns = `id, task_id, sequence, start_time, end_time, source_text, translated_text, translation_status`

// validateSegments 校验字幕切片，任何一项不合法都返回中文错误。
func validateSegments(segs []SegmentRecord) error {
	ids := make(map[string]struct{}, len(segs))
	sequences := make(map[int]struct{}, len(segs))
	for i, seg := range segs {
		if seg.ID == "" {
			return fmt.Errorf("校验字幕失败: 第 %d 条字幕 ID 为空", i+1)
		}
		if _, dup := ids[seg.ID]; dup {
			return fmt.Errorf("校验字幕失败: 字幕 ID %q 重复", seg.ID)
		}
		ids[seg.ID] = struct{}{}

		if seg.Sequence < 1 {
			return fmt.Errorf("校验字幕失败: 字幕 %q 的序号 %d 非法，必须大于等于 1", seg.ID, seg.Sequence)
		}
		if _, dup := sequences[seg.Sequence]; dup {
			return fmt.Errorf("校验字幕失败: 字幕序号 %d 重复", seg.Sequence)
		}
		sequences[seg.Sequence] = struct{}{}

		if seg.Start < 0 {
			return fmt.Errorf("校验字幕失败: 字幕 %q 的开始时间 %.3f 为负数", seg.ID, seg.Start)
		}
		if seg.End < seg.Start {
			return fmt.Errorf("校验字幕失败: 字幕 %q 的结束时间 %.3f 早于开始时间 %.3f", seg.ID, seg.End, seg.Start)
		}
	}
	return nil
}

// insertSegment 在给定事务/连接上插入一条字幕，使用 INSERT OR REPLACE 以支持分批覆盖写入。
func insertSegment(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, taskID string, seg SegmentRecord) error {
	status := seg.Status
	if status == "" {
		status = SegmentStatusPending
	}
	if _, err := exec.ExecContext(ctx, `INSERT OR REPLACE INTO subtitle_segments
		(id, task_id, sequence, start_time, end_time, source_text, translated_text, translation_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		seg.ID, taskID, seg.Sequence, seg.Start, seg.End, seg.SourceText, seg.Translated, status,
	); err != nil {
		return fmt.Errorf("写入字幕 %q 失败: %w", seg.ID, err)
	}
	return nil
}

// ReplaceSegments 在单个事务内先清空任务字幕再整体写入，任何一条校验失败都整体回滚。
func (s *Store) ReplaceSegments(ctx context.Context, taskID string, segs []SegmentRecord) error {
	if taskID == "" {
		return errors.New("替换字幕失败: 任务 ID 不能为空")
	}
	// 先做纯内存校验，避免无谓地占用事务。
	if err := validateSegments(segs); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("替换字幕失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM subtitle_segments WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("清空旧字幕失败: %w", err)
	}
	for _, seg := range segs {
		if err := insertSegment(ctx, tx, taskID, seg); err != nil {
			return fmt.Errorf("替换字幕失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交替换字幕事务失败: %w", err)
	}
	return nil
}

// ListSegments 按序号升序返回任务的全部字幕。
func (s *Store) ListSegments(ctx context.Context, taskID string) ([]SegmentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+segmentColumns+` FROM subtitle_segments
		WHERE task_id = ? ORDER BY sequence ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("查询字幕失败: %w", err)
	}
	defer rows.Close()

	segs := make([]SegmentRecord, 0)
	for rows.Next() {
		var seg SegmentRecord
		if err := rows.Scan(&seg.ID, &seg.TaskID, &seg.Sequence, &seg.Start, &seg.End,
			&seg.SourceText, &seg.Translated, &seg.Status); err != nil {
			return nil, fmt.Errorf("读取字幕记录失败: %w", err)
		}
		segs = append(segs, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历字幕列表失败: %w", err)
	}
	return segs, nil
}

// UpsertSegments 分批写入识别中间结果，同一 ID 已存在时覆盖。
func (s *Store) UpsertSegments(ctx context.Context, taskID string, segs []SegmentRecord) error {
	if taskID == "" {
		return errors.New("写入字幕失败: 任务 ID 不能为空")
	}
	if len(segs) == 0 {
		return nil
	}
	if err := validateSegments(segs); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("写入字幕失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, seg := range segs {
		if err := insertSegment(ctx, tx, taskID, seg); err != nil {
			return fmt.Errorf("写入字幕失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交批量写入字幕事务失败: %w", err)
	}
	return nil
}

// UpdateSegmentTranslation 更新单条字幕的译文与翻译状态。
func (s *Store) UpdateSegmentTranslation(ctx context.Context, taskID, segmentID, translated, status string) error {
	if taskID == "" || segmentID == "" {
		return errors.New("更新字幕译文失败: 任务 ID 与字幕 ID 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE subtitle_segments SET translated_text = ?, translation_status = ?
		WHERE task_id = ? AND id = ?`, translated, status, taskID, segmentID)
	if err != nil {
		return fmt.Errorf("更新字幕译文失败: %w", err)
	}
	return affectedOrNotFound(res, "更新字幕译文", segmentID)
}

// TranslatePendingSegments 取出待翻译字幕，按序号升序最多 limit 条。limit<=0 表示不限。
func (s *Store) TranslatePendingSegments(ctx context.Context, taskID string, limit int) ([]SegmentRecord, error) {
	query := `SELECT ` + segmentColumns + ` FROM subtitle_segments
		WHERE task_id = ? AND translation_status = ? ORDER BY sequence ASC`
	args := []any{taskID, SegmentStatusPending}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询待翻译字幕失败: %w", err)
	}
	defer rows.Close()

	segs := make([]SegmentRecord, 0)
	for rows.Next() {
		var seg SegmentRecord
		if err := rows.Scan(&seg.ID, &seg.TaskID, &seg.Sequence, &seg.Start, &seg.End,
			&seg.SourceText, &seg.Translated, &seg.Status); err != nil {
			return nil, fmt.Errorf("读取待翻译字幕失败: %w", err)
		}
		segs = append(segs, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待翻译字幕失败: %w", err)
	}
	return segs, nil
}
