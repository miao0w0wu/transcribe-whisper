package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"transcribe-whisper/internal/asr"
	"transcribe-whisper/internal/media"
	"transcribe-whisper/internal/modelmanager"
	"transcribe-whisper/internal/repository"
	"transcribe-whisper/internal/service"
	"transcribe-whisper/internal/translation"
)

// 事件名，前端通过 EventsOn 订阅。
const (
	eventTaskProgress  = "task:progress"
	eventModelProgress = "model:progress"
)

// App 是 Wails Binding 主体，只做参数校验、领域调用与结果映射。
type App struct {
	ctx context.Context

	mu       sync.RWMutex
	db       *database
	store    *repository.Store
	queue    *service.Queue
	models   *modelmanager.ManagerImpl
	media    *media.FFmpeg
	engine   asr.Engine
	dataDir  string
	settings repository.SettingsRecord
	// tempDataDir 表示数据目录是测试临时目录，shutdown 时要清理。
	tempDataDir bool
	// deepUpgradeEnabled 表示允许在装配时做磁盘深度扫描升级 FFmpeg。
	// 只有真实应用启动（startup）开启；测试与 e2e 关闭，避免拖慢启动。
	deepUpgradeEnabled bool
}

// database 包装连接句柄，保留旧版本 openDatabase 语义以便测试。
type database struct {
	store *repository.Store
	path  string
}

// NewApp 构造应用实例。
//
// 它只在测试里使用：数据目录指向系统临时目录，避免读取或写入用户的
// 真实配置（模型目录、设置），保证测试之间以及测试与真实安装互不污染。
// 真实应用走 startup，由 Wails 调用。
func NewApp() *App {
	app := &App{}
	if dir, err := os.MkdirTemp("", "transcribe-whisper-app-*"); err == nil {
		app.dataDir = dir
		app.tempDataDir = true
	}
	if err := app.openDatabase(":memory:"); err != nil {
		panic(err)
	}
	return app
}

// startup 由 Wails 调用，负责打开真实数据库并装配依赖。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.deepUpgradeEnabled = true
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Printf("无法定位应用配置目录: %v", err)
		return
	}
	a.dataDir = filepath.Join(configDir, "transcribe-whisper")
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		log.Printf("无法创建应用数据目录: %v", err)
		return
	}
	databasePath := filepath.Join(a.dataDir, "transcribe-whisper.db")
	if err := a.openDatabase(databasePath); err != nil {
		log.Printf("无法打开任务数据库: %v", err)
		return
	}
}

// shutdown 关闭数据库与后台任务，并清理测试用的临时目录。
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	queue := a.queue
	store := a.store
	a.queue = nil
	a.store = nil
	a.mu.Unlock()

	if queue != nil {
		queue.Close()
	}
	if store != nil {
		if err := store.Close(); err != nil {
			log.Printf("关闭任务数据库失败: %v", err)
		}
	}

	a.mu.RLock()
	tempDir := a.dataDir
	isTemp := a.tempDataDir
	a.mu.RUnlock()
	if isTemp && tempDir != "" {
		_ = os.RemoveAll(tempDir)
	}
}

// openDatabase 打开数据库并刷新依赖，测试会直接调用它。
func (a *App) openDatabase(path string) error {
	store, err := repository.Open(path)
	if err != nil {
		return err
	}

	a.mu.Lock()
	old := a.db
	a.db = &database{store: store, path: path}
	a.store = store
	a.mu.Unlock()
	if old != nil {
		_ = old.store.Close()
	}
	a.bootstrap(context.Background())
	return nil
}

// bootstrap 装配下游服务与任务队列。
func (a *App) bootstrap(ctx context.Context) {
	store := a.store
	if store == nil {
		return
	}

	a.mu.RLock()
	dataDir := a.dataDir
	existingQueue := a.queue
	a.mu.RUnlock()
	if dataDir == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			dataDir = filepath.Join(dir, "transcribe-whisper")
		}
	}

	settings, err := store.LoadSettings(ctx)
	if err != nil {
		log.Printf("读取设置失败: %v", err)
		settings = service.SettingsDefaults("")
	}

	modelDir := strings.TrimSpace(settings.ModelDir)
	if modelDir == "" {
		modelDir = filepath.Join(dataDir, "models")
		settings.ModelDir = modelDir
	}

	// FFmpeg 选择：用户显式指定的路径最优先；未指定时自动在 PATH 与常见
	// 安装位置里挑能力最强的构建（字幕压制需要 subtitles 滤镜，很多精简
	// 构建没有编译 libass，会导致压制功能不可用）。quick 选择拿不到压制
	// 能力、且允许深度扫描时，再限深扫一次磁盘，把便携工具包自带的完整
	// 构建找出来（真实启动才开启，测试与 e2e 不被拖慢）。
	selectedFFmpeg := selectFFmpeg(ctx, settings.FFmpegPath)
	if selectedFFmpeg != nil && a.deepUpgradeEnabled {
		if upgraded := upgradeBurnCapability(ctx, selectedFFmpeg, &settings); upgraded != nil {
			selectedFFmpeg = upgraded
			_ = store.SaveSettings(ctx, settings)
		}
	}
	if selectedFFmpeg == nil {
		log.Printf("未找到可用的 FFmpeg，媒体相关功能将不可用；可在设置中手动指定路径")
	}
	if selectedFFmpeg != nil && strings.TrimSpace(settings.FFmpegPath) == "" {
		// 自动发现的结果写回设置，让设置页展示实际使用的路径；
		// 用户手动配置过的路径不动。
		settings.FFmpegPath = selectedFFmpeg.Path()
	}

	engine := asr.NewFasterWhisperEngine()

	models := modelmanager.New(modelmanager.Options{
		RootDir:   modelDir,
		Mirrors:   mirrorsFor(settings.Mirror, settings.HuggingFaceEndpoint),
		InUse:     a.modelInUse,
		Catalog:   modelmanager.DefaultCatalog(),
		UserAgent: "transcribe-whisper/1.0",
	})

	queue := service.NewQueue(service.Deps{
		Store:       store,
		Media:       selectedFFmpeg,
		Burner:      selectedFFmpeg,
		Engine:      engine,
		Resolver:    service.ManagerAdapter{Manager: models},
		Translator:  translationFactory{settings: func() repository.SettingsRecord { return a.currentSettings() }},
		Emitters:    []service.Emitter{a.emitTask},
		WorkDir:     filepath.Join(dataDir, "work"),
		Concurrency: 1,
		Settings: func(ctx context.Context) (repository.SettingsRecord, error) {
			return a.currentSettings(), nil
		},
	})
	if interrupted, err := queue.Recover(ctx); err != nil {
		log.Printf("清理中断任务失败: %v", err)
	} else if interrupted > 0 {
		log.Printf("已将 %d 个中断任务标记为失败", interrupted)
	}

	a.mu.Lock()
	a.settings = settings
	a.store = store
	a.media = selectedFFmpeg
	a.engine = engine
	a.models = models
	a.queue = queue
	a.mu.Unlock()

	if existingQueue != nil {
		existingQueue.Close()
	}
}

// asrScriptPath 返回外部 ASR 脚本路径（供用户自行替换），默认由引擎内嵌。
// 保留该函数以便设置页展示脚本位置。
func asrScriptPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "scripts", "transcribe_worker.py")
}

// 说明：真实链路中脚本由 asr 包内嵌并在首次运行时释放到缓存目录，
// 用户可通过设置里的 Python 路径指定解释器。
var _ = asrScriptPath

// mirrorsFor 根据设置生成镜像候选列表。
func mirrorsFor(mirror, endpoint string) []modelmanager.Mirror {
	mirrors := make([]modelmanager.Mirror, 0, 3)
	normalized := strings.ToLower(strings.TrimSpace(mirror))
	if normalized == "custom" && strings.TrimSpace(endpoint) != "" {
		mirrors = append(mirrors, modelmanager.Mirror{ID: "custom", Label: "自定义端点", BaseURL: strings.TrimRight(strings.TrimSpace(endpoint), "/")})
	}
	if normalized == "huggingface" {
		mirrors = append(mirrors,
			modelmanager.Mirror{ID: "huggingface", Label: "Hugging Face", BaseURL: "https://huggingface.co"},
			modelmanager.Mirror{ID: "hf-mirror", Label: "国内镜像", BaseURL: "https://hf-mirror.com"},
		)
		return mirrors
	}
	mirrors = append(mirrors,
		modelmanager.Mirror{ID: "hf-mirror", Label: "国内镜像", BaseURL: "https://hf-mirror.com"},
		modelmanager.Mirror{ID: "huggingface", Label: "Hugging Face", BaseURL: "https://huggingface.co"},
	)
	if strings.TrimSpace(endpoint) != "" && normalized != "custom" {
		mirrors = append(mirrors, modelmanager.Mirror{ID: "custom", Label: "自定义端点", BaseURL: strings.TrimRight(strings.TrimSpace(endpoint), "/")})
	}
	return mirrors
}

// currentSettings 返回当前设置快照。
func (a *App) currentSettings() repository.SettingsRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings
}

// modelInUse 判断模型是否正被任务使用。
func (a *App) modelInUse(modelID string) bool {
	a.mu.RLock()
	store := a.store
	queue := a.queue
	a.mu.RUnlock()
	if store == nil {
		return false
	}
	tasks, err := store.ListTasks(context.Background())
	if err != nil {
		return true
	}
	for _, task := range tasks {
		if task.Model != modelID {
			continue
		}
		switch task.Status {
		case repository.TaskStatusExtracting, repository.TaskStatusTranscribing, repository.TaskStatusTranslating, repository.TaskStatusQueued:
			return true
		}
		if queue != nil && queue.Running(task.ID) {
			return true
		}
	}
	return false
}

// emitTask 把任务状态映射成进度事件推送到前端。
func (a *App) emitTask(ctx context.Context, rec *repository.TaskRecord) {
	if a.ctx == nil || rec == nil {
		return
	}
	runtime.EventsEmit(a.ctx, eventTaskProgress, TaskProgressEvent{
		TaskID:       rec.ID,
		Status:       rec.Status,
		Stage:        rec.Stage,
		Progress:     rec.Progress,
		CurrentStage: rec.CurrentStage,
		Message:      rec.Message,
		Error:        rec.Error,
	})
}

// emitModel 推送模型下载进度。
func (a *App) emitModel(event ModelProgressEventPayload) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, eventModelProgress, event)
}

// storeHandle 返回数据库句柄，未初始化时返回错误。
func (a *App) storeHandle() (*repository.Store, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.store == nil {
		return nil, errors.New("数据库未就绪")
	}
	return a.store, nil
}

// queueHandle 返回任务队列，未初始化时返回错误。
func (a *App) queueHandle() (*service.Queue, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.queue == nil {
		return nil, errors.New("任务调度器未就绪")
	}
	return a.queue, nil
}

// contextOrBackground 返回可用的上下文。
func (a *App) contextOrBackground() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// translationFactory 依据设置构造翻译流水线。
type translationFactory struct {
	settings func() repository.SettingsRecord
}

// New 实现 service.TranslatorFactory。
func (f translationFactory) New(ctx context.Context, job service.TranslationJob) (service.Pipeline, error) {
	cfg := translation.Config{
		Kind:    job.Provider,
		BaseURL: job.Endpoint,
		APIKey:  job.APIKey,
		Model:   job.Model,
	}
	provider, err := translation.New(cfg)
	if err != nil {
		return nil, err
	}
	settings := repository.SettingsRecord{}
	if f.settings != nil {
		settings = f.settings()
	}
	return translation.NewTranslator(provider, translation.Options{
		BatchSize:      20,
		MaxRetries:     3,
		Concurrency:    1,
		Cache:          translation.NewMemoryCache(),
		Glossary:       settings.Glossary,
		SourceLanguage: job.SourceLanguage,
		TargetLanguage: job.TargetLanguage,
		Style:          defaultStyle(settings.TranslationStyle),
		ContentMode:    "subtitle",
	}), nil
}

// ensureExecutable 检查可执行文件是否存在于 PATH 或给定路径。
func ensureExecutable(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	if resolved, err := exec.LookPath(path); err == nil {
		return resolved, true
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, true
	}
	return "", false
}
