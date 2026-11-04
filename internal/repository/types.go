// Package repository 提供 SQLite 持久化。
//
// 该文件是共享契约与 schema，由 Lead 维护；实现位于同包其他文件。
// 约束：所有表结构变更必须写在这里的 ensure 语句里，且要能兼容旧库
// （CREATE TABLE IF NOT EXISTS + 必要的 ALTER TABLE ADD COLUMN 迁移）。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("record not found")

// 任务状态机常量，前端与 service 都以这些字符串为准。
const (
	TaskStatusCreated      = "created"
	TaskStatusQueued       = "queued"
	TaskStatusExtracting   = "extracting"   // 正在提取音频
	TaskStatusTranscribing = "transcribing" // 正在识别
	TaskStatusTranslating  = "translating"  // 正在翻译
	TaskStatusPaused       = "paused"
	TaskStatusCompleted    = "completed"
	TaskStatusFailed       = "failed"
	TaskStatusCanceled     = "canceled"
)

// TaskRecord 是 tasks 表的完整行。
type TaskRecord struct {
	ID              string
	Name            string
	SourceFile      string
	SourceLanguage  string
	TargetLanguage  string
	Status          string
	Stage           string
	Progress        int
	CurrentStage    string
	SubtitleCount   int
	TranslatedCount int
	Message         string
	Error           string
	Engine          string
	Model           string
	Provider        string
	OutputFormat    string
	Duration        float64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// DurationMicroseconds 是媒体总时长，用 int64 存储以兼容 SQLite。
// Duration 字段以秒为单位保留给调用方，写入时用该值。

// SegmentRecord 是 subtitle_segments 表的一行。
type SegmentRecord struct {
	ID         string
	TaskID     string
	Sequence   int
	Start      float64
	End        float64
	SourceText string
	Translated string
	Status     string
}

// SettingsRecord 是应用设置（单行）。
type SettingsRecord struct {
	FFmpegPath          string
	PythonPath          string
	ModelDir            string
	Mirror              string
	HuggingFaceEndpoint string
	AsrEngine           string
	AsrDevice           string
	ComputeType         string
	DefaultModel        string
	TranslationProvider string
	TranslationEndpoint string
	TranslationModel    string
	TranslationAPIKey   string
	CloudPrivacyConfirm bool
	// TranslationStyle 是翻译语气，例如 subtitle / formal / casual。
	TranslationStyle string
	// Glossary 是术语表：源语言术语 -> 目标语言译法。
	Glossary map[string]string
}

// Store 封装数据库句柄。所有方法都要是并发安全的。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开数据库并执行 schema 迁移。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// sqlite 单文件建议串行写入，避免 database is locked。
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewStoreWithDB 用于测试注入（例如 sqlmock）。
func NewStoreWithDB(db *sql.DB) *Store { return &Store{db: db} }

// DB 暴露底层句柄，仅测试使用。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭数据库。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			source_file TEXT NOT NULL,
			source_language TEXT NOT NULL,
			target_language TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT NOT NULL DEFAULT 'queued',
			progress INTEGER NOT NULL DEFAULT 0,
			current_stage TEXT NOT NULL DEFAULT '',
			subtitle_count INTEGER NOT NULL DEFAULT 0,
			translated_count INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			engine TEXT NOT NULL DEFAULT 'faster-whisper',
			model TEXT NOT NULL DEFAULT 'small',
			provider TEXT NOT NULL DEFAULT 'ollama',
			output_format TEXT NOT NULL DEFAULT 'srt',
			duration REAL NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS subtitle_segments (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			start_time REAL NOT NULL,
			end_time REAL NOT NULL,
			source_text TEXT NOT NULL,
			translated_text TEXT NOT NULL,
			translation_status TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_subtitle_segments_task ON subtitle_segments(task_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			ffmpeg_path TEXT NOT NULL DEFAULT '',
			python_path TEXT NOT NULL DEFAULT '',
			model_dir TEXT NOT NULL DEFAULT '',
			mirror TEXT NOT NULL DEFAULT 'hf-mirror',
			hugging_face_endpoint TEXT NOT NULL DEFAULT '',
			asr_engine TEXT NOT NULL DEFAULT 'faster-whisper',
			asr_device TEXT NOT NULL DEFAULT 'auto',
			compute_type TEXT NOT NULL DEFAULT 'auto',
			default_model TEXT NOT NULL DEFAULT 'small',
			translation_provider TEXT NOT NULL DEFAULT 'ollama',
			translation_endpoint TEXT NOT NULL DEFAULT '',
			translation_model TEXT NOT NULL DEFAULT '',
			translation_api_key TEXT NOT NULL DEFAULT '',
			cloud_privacy_confirm INTEGER NOT NULL DEFAULT 0,
			translation_style TEXT NOT NULL DEFAULT 'subtitle',
			glossary_json TEXT NOT NULL DEFAULT '{}'
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// 兼容旧库：补齐后续阶段新增的列。
	columns := map[string]string{
		"stage":            "TEXT NOT NULL DEFAULT 'queued'",
		"translated_count": "INTEGER NOT NULL DEFAULT 0",
		"message":          "TEXT NOT NULL DEFAULT ''",
		"error":            "TEXT NOT NULL DEFAULT ''",
		"engine":           "TEXT NOT NULL DEFAULT 'faster-whisper'",
		"model":            "TEXT NOT NULL DEFAULT 'small'",
		"provider":         "TEXT NOT NULL DEFAULT 'ollama'",
		"output_format":    "TEXT NOT NULL DEFAULT 'srt'",
		"duration":         "REAL NOT NULL DEFAULT 0",
		"updated_at":       "TEXT NOT NULL DEFAULT ''",
	}
	existing, err := s.tableColumns("tasks")
	if err != nil {
		return err
	}
	for name, ddl := range columns {
		if existing[name] {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE tasks ADD COLUMN " + name + " " + ddl); err != nil {
			// 并发/重复迁移时忽略重复列错误。
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migrate add column %s: %w", name, err)
			}
		}
	}
	// 设置表同样需要兼容旧库（术语表与风格是后续加入的列）。
	settingsColumns := map[string]string{
		"translation_style": "TEXT NOT NULL DEFAULT 'subtitle'",
		"glossary_json":     "TEXT NOT NULL DEFAULT '{}'",
	}
	existingSettings, err := s.tableColumns("settings")
	if err != nil {
		return err
	}
	for name, ddl := range settingsColumns {
		if existingSettings[name] {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE settings ADD COLUMN " + name + " " + ddl); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migrate add settings column %s: %w", name, err)
			}
		}
	}
	return nil
}

func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		result[strings.ToLower(name)] = true
	}
	return result, rows.Err()
}

// Ping 检查数据库是否可用。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
