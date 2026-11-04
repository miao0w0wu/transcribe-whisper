package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/repository"
)

// namedEngine 是一个只用来区分身份的假引擎。
//
// used 用 atomic：它在执行器 goroutine 里写、在测试 goroutine 里读，
// 普通 bool 会触发 -race 报警。
type namedEngine struct {
	name string
	used atomic.Bool
}

func (e *namedEngine) Name() string { return e.name }

func (e *namedEngine) Available(ctx context.Context) (bool, string) { return true, "ok" }

func (e *namedEngine) Transcribe(ctx context.Context, req asr.Request, progress asr.Progress) (*asr.Result, error) {
	e.used.Store(true)
	return &asr.Result{Language: "en", Segments: []asr.Segment{{Sequence: 1, Text: "hi"}}}, nil
}

func TestStaticEngineSelectorPrefersModelEngine(t *testing.T) {
	ct2 := &namedEngine{name: "faster-whisper"}
	st := &namedEngine{name: "transformers"}
	selector := StaticEngineSelector{
		Fallback: ct2,
		Engines: map[string]asr.Engine{
			"faster-whisper": ct2,
			"transformers":   st,
		},
		Lookup: func(modelID string) string {
			if modelID == "cohere-transcribe" {
				return "transformers"
			}
			return ""
		},
	}

	// 模型自带引擎信息时优先，即使设置里写的是 faster-whisper。
	if got := selector.Select("cohere-transcribe", "faster-whisper"); got != asr.Engine(st) {
		t.Fatalf("cohere 应选 transformers，实际 %s", got.Name())
	}
	// 没有模型信息时用设置里的引擎。
	if got := selector.Select("small", "faster-whisper"); got != asr.Engine(ct2) {
		t.Fatalf("small 应选 faster-whisper，实际 %s", got.Name())
	}
	// 设置里写了空值时回退到 Fallback。
	if got := selector.Select("", ""); got != asr.Engine(ct2) {
		t.Fatalf("空值应回退 Fallback，实际 %s", got.Name())
	}
}

func TestStaticEngineSelectorUnknownEngineFallsBack(t *testing.T) {
	ct2 := &namedEngine{name: "faster-whisper"}
	selector := StaticEngineSelector{
		Fallback: ct2,
		Engines:  map[string]asr.Engine{"faster-whisper": ct2},
		Lookup:   func(modelID string) string { return "不存在的引擎" },
	}
	if got := selector.Select("x", ""); got != asr.Engine(ct2) {
		t.Fatalf("未知引擎应回退，实际 %v", got)
	}
}

func TestStaticEngineSelectorNilLookup(t *testing.T) {
	ct2 := &namedEngine{name: "faster-whisper"}
	st := &namedEngine{name: "transformers"}
	selector := StaticEngineSelector{
		Fallback: ct2,
		Engines:  map[string]asr.Engine{"faster-whisper": ct2, "transformers": st},
	}
	if got := selector.Select("anything", "transformers"); got != asr.Engine(st) {
		t.Fatalf("无 Lookup 时应按 prefer 选择，实际 %s", got.Name())
	}
}

// seedTaskWithModel 直接用指定模型 / 引擎写入一条任务。
func seedTaskWithModel(t *testing.T, queue *Queue, model, engine string) *repository.TaskRecord {
	t.Helper()
	rec := &repository.TaskRecord{
		ID:             "task-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Name:           "engine-route",
		SourceFile:     filepath.Join(t.TempDir(), "a.mp4"),
		SourceLanguage: "en",
		TargetLanguage: "zh-CN",
		Status:         repository.TaskStatusCreated,
		Stage:          "created",
		Engine:         engine,
		Model:          model,
		Provider:       "none",
		OutputFormat:   "srt",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := os.WriteFile(rec.SourceFile, []byte("fake"), 0o644); err != nil {
		t.Fatalf("写入占位媒体失败: %v", err)
	}
	if err := queue.store().CreateTask(context.Background(), rec); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return rec
}

// 任务使用 transformers 模型时，实际执行的必须是 transformers 引擎。
func TestTranscribeStageRoutesToModelEngine(t *testing.T) {
	ct2 := &fakeEngine{}
	st := &namedEngine{name: "transformers"}
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Engine = ct2
		deps.Engines = StaticEngineSelector{
			Fallback: ct2,
			Engines: map[string]asr.Engine{
				"faster-whisper": ct2,
				"transformers":   st,
			},
			Lookup: func(modelID string) string {
				if strings.HasPrefix(modelID, "cohere") {
					return "transformers"
				}
				return "faster-whisper"
			},
		}
	})
	// 任务记录里的 engine 是旧值，不应影响路由。
	rec := seedTaskWithModel(t, queue, "cohere-transcribe", "faster-whisper")

	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return st.used.Load() || ct2.called.Load() })
	if !st.used.Load() {
		t.Fatal("cohere 模型应交给 transformers 引擎")
	}
	if ct2.called.Load() {
		t.Fatal("不应同时调用 faster-whisper 引擎")
	}
}

func TestTranscribeStageFallsBackToDefaultEngine(t *testing.T) {
	ct2 := &fakeEngine{}
	queue := newTestQueue(t, func(deps *Deps) {
		deps.Engine = ct2
		deps.Engines = StaticEngineSelector{
			Fallback: ct2,
			Engines:  map[string]asr.Engine{"faster-whisper": ct2},
			Lookup:   func(modelID string) string { return "faster-whisper" },
		}
	})
	rec := seedTaskWithModel(t, queue, "tiny", "faster-whisper")
	if _, err := queue.Enqueue(context.Background(), rec.ID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return ct2.called.Load() })
}
