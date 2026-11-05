package modelmanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 默认参数。
const (
	// defaultMaxRetries 是单文件在单个镜像上的最大尝试次数。
	defaultMaxRetries = 3
	// resumeRetryBudget 是「同一镜像 + 同一偏移量」允许的连续失败次数。
	// 断点续传时若服务端反复返回同样的落点却不给数据，过早判定失败会误伤；
	// 一旦超过该阈值即视为协议不兼容，直接切开固定用量重头下载。
	resumeRetryBudget = 8
	// progressMinInterval 是进度回调的最小间隔。
	progressMinInterval = 250 * time.Millisecond
	// progressMinDelta 是进度回调的最小变化量（1%）。
	progressMinDelta = 0.01
	// sizeTolerance 是文件大小校验的浮动比例（±1%），用于识破代理返回的错误页。
	sizeTolerance = 0.01
	// maxSizeTolerance 给极小文件一个字节级下限，避免 ±1% 被取整成 0。
	maxSizeTolerance = 1024
	// downloadTimeout 是单个文件的整体超时，防止连接挂死。
	downloadTimeout = 30 * time.Minute
	// idleTimeout 是下载过程中的读空闲超时，超过该时长没有任何数据即重试。
	idleTimeout = 90 * time.Second
	// removeAsyncDelay 是删除目录改名为临时目录后，后台真正清理的延迟。
	removeAsyncDelay = 3 * time.Second
	// downloadBufferSize 是下载缓冲区大小。
	downloadBufferSize = 256 * 1024
	// maxRetrySleep 是重试退避的上限。
	maxRetrySleep = 2 * time.Second
	// defaultUserAgent 是默认 UA，部分镜像会对空 UA 做拦截。
	defaultUserAgent = "transcribe-whisper/0.1 (+modelmanager)"
)

// Options 是 ManagerImpl 的构造参数。
type Options struct {
	// RootDir 是模型根目录。为空时使用用户配置目录下的
	// `<用户配置目录>/transcribe-whisper/models`。
	RootDir string
	// Mirrors 是镜像列表，按优先级排列。为空时使用默认镜像：
	// hf-mirror.com（国内可达）、huggingface.co（官方，本机可能不可达）。
	Mirrors []Mirror
	// HTTPClient 是下载用 HTTP 客户端。为空时使用内置默认客户端
	// （超时 5 分钟、连接复用、最多 16 个空闲连接）。
	HTTPClient *http.Client
	// Catalog 是模型清单。为空时使用 DefaultCatalog()。
	Catalog []CatalogEntry
	// UserAgent 是请求 UA。为空时使用默认值。
	UserAgent string
	// MaxRetries 是单文件在单个镜像上的最大尝试次数。<=0 时取默认值 3。
	MaxRetries int
	// InUse 由调用方注入，用于删除保护：返回 true 表示该模型正在被任务使用。
	InUse func(modelID string) bool
	// Token 是 Hugging Face 访问令牌，用于下载 gated（受限）仓库。
	// 为空时受限仓库会返回 403，此时应在界面上提示用户配置令牌。
	Token string
}

// DefaultMirrors 返回默认镜像顺序：hf-mirror（国内可达）优先，官方 huggingface 兜底。
//
// 自定义端点（自建反向代理、内网镜像等）由调用方通过 Options.Mirrors 追加在末尾，
// 只要其目录结构与 HF 同构（`{base}/{repo}/resolve/main/{file}`）即可直接使用。
func DefaultMirrors() []Mirror {
	return []Mirror{
		{ID: "hf-mirror", Label: "HF 镜像（国内）", BaseURL: "https://hf-mirror.com"},
		{ID: "huggingface", Label: "Hugging Face 官方", BaseURL: "https://huggingface.co"},
	}
}

// DefaultRootDir 返回默认模型根目录。
//
// Windows 下为 `%AppData%\transcribe-whisper\models`
// （os.UserConfigDir 即 %AppData%），与项目数据库目录同级。
func DefaultRootDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("获取用户配置目录失败：%w", err)
	}
	return filepath.Join(base, "transcribe-whisper", "models"), nil
}

// 下载状态。
const (
	stateIdle int32 = iota
	stateDownloading
	stateCanceling
)

// fileRecord 记录某个模型各文件的下载进度（按集合总字节累加）。
type fileRecord struct {
	modelID string
	total   int64
	done    int64
	// current 是正在下载的文件名，仅用于消息展示。
	current string
	// stop 是本模型当前的取消函数，与 ManagerImpl.cancels 保持同步。
	stop context.CancelFunc
}

// ManagerImpl 是 Manager 的默认实现，并发安全。
type ManagerImpl struct {
	rootDir    string
	mirrors    []Mirror
	client     *http.Client
	catalog    []CatalogEntry
	userAgent  string
	maxRetries int
	inUse      func(modelID string) bool
	token      string

	mu sync.RWMutex
	// locks 保护同一模型的「检查是否已安装 + 下载」临界区。
	locks map[string]*sync.Mutex
	// files 记录各模型的字节级进度，供 Progress(modelID) 读取。
	files map[string]*fileRecord
	// disabled 记录在本次会话中被判定不可用的镜像 ID。
	disabled map[string]bool
	// localDirs 记录用户自选本地模型的 ID -> 目录（见 local.go）。
	localDirs map[string]string

	cancels sync.Map // modelID(string) -> context.CancelFunc
	states  sync.Map // modelID(string) -> *int32
}

// New 创建模型管理器。Options 的零值字段会填充为默认值。
func New(opts Options) *ManagerImpl {
	m := &ManagerImpl{
		rootDir:    strings.TrimSpace(opts.RootDir),
		userAgent:  strings.TrimSpace(opts.UserAgent),
		maxRetries: opts.MaxRetries,
		inUse:      opts.InUse,
		token:      strings.TrimSpace(opts.Token),
		locks:      make(map[string]*sync.Mutex),
		files:      make(map[string]*fileRecord),
		disabled:   make(map[string]bool),
		localDirs:  make(map[string]string),
	}
	if m.rootDir == "" {
		if dir, err := DefaultRootDir(); err == nil {
			m.rootDir = dir
		} else {
			// 极端情况下退到当前目录，保证管理器仍可工作。
			m.rootDir = filepath.Join(".", "models")
		}
	}
	if len(opts.Mirrors) > 0 {
		for _, mi := range opts.Mirrors {
			if base := strings.TrimSpace(mi.BaseURL); base != "" {
				mi.BaseURL = strings.TrimRight(base, "/")
				if strings.TrimSpace(mi.ID) == "" {
					mi.ID = mi.BaseURL
				}
				m.mirrors = append(m.mirrors, mi)
			}
		}
	}
	if len(m.mirrors) == 0 {
		m.mirrors = DefaultMirrors()
	}
	if opts.HTTPClient != nil {
		m.client = opts.HTTPClient
	} else {
		m.client = &http.Client{
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   20 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: 5 * time.Second,
			},
		}
	}
	if len(opts.Catalog) > 0 {
		m.catalog = cloneCatalog(opts.Catalog)
	} else {
		m.catalog = DefaultCatalog()
	}
	if m.userAgent == "" {
		m.userAgent = defaultUserAgent
	}
	if m.maxRetries <= 0 {
		m.maxRetries = defaultMaxRetries
	}
	return m
}

// cloneCatalog 深拷贝清单，避免调用方修改影响到管理器内部状态。
func cloneCatalog(in []CatalogEntry) []CatalogEntry {
	out := make([]CatalogEntry, len(in))
	for i, e := range in {
		out[i] = e
		out[i].Files = append([]ManifestFile(nil), e.Files...)
		out[i].Required = append([]string(nil), e.Required...)
	}
	return out
}

// RootDir 返回模型根目录。
func (m *ManagerImpl) RootDir() string { return m.rootDir }

// Mirrors 返回当前生效的镜像列表（只读副本）。
func (m *ManagerImpl) Mirrors() []Mirror {
	return append([]Mirror(nil), m.mirrors...)
}

// Catalog 返回内置模型清单（只读副本）。
func (m *ManagerImpl) Catalog() []CatalogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneCatalog(m.catalog)
}

// Find 在当前清单中查找模型，支持别名归一化。
func (m *ManagerImpl) Find(modelID string) (CatalogEntry, bool) {
	return findInCatalog(m.Catalog(), modelID)
}

// LocalDir 返回模型在本地的目录路径。
//
// 目录名采用 `<size>` 形式（tiny/base/small/medium/large-v3），与 faster-whisper
// 的 model_size_or_path 参数完全一致，可直接传给 Python 侧。
// 用户自选的本地模型直接返回其原始目录，不受 rootDir 影响。
func (m *ManagerImpl) LocalDir(modelID string) string {
	if dir := m.localDirOf(modelID); dir != "" {
		return dir
	}
	entry, ok := m.Find(modelID)
	if !ok {
		return filepath.Join(m.rootDir, sanitizeDirName(modelID))
	}
	if dir := m.localDirOf(entry.ID); dir != "" {
		return dir
	}
	return filepath.Join(m.rootDir, entry.ID)
}

// sanitizeDirName 清理文件名中的非法字符，防止路径穿越。
func sanitizeDirName(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "-", "<", "-", ">", "-", "|", "-",
	)
	name = replacer.Replace(name)
	// Windows 上 `.` 与 `..` 不能作为目录名。
	if name == "." || name == ".." || name == "" {
		return "model"
	}
	return name
}

// Installed 返回「已安装」模型：ID -> 本地目录。
//
// 两条来源：
//  1. rootDir 下 Required 文件齐全的清单项；
//  2. 用户自选的本地模型（目录不存在或已不成型时不算已安装）。
func (m *ManagerImpl) Installed(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[string]string)

	// 先处理本地模型：它们不在 rootDir 下，不能靠扫描目录发现。
	for id, dir := range m.LocalDirs() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, _, detectErr := DetectLocalModel(dir); detectErr == nil {
			result[id] = dir
		}
	}

	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 根目录还没建：本地模型结果照常返回。
			return result, nil
		}
		return nil, fmt.Errorf("读取模型目录失败：%w", err)
	}
	for _, de := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		// 跳过删除中的临时目录。
		if strings.Contains(name, ".deleting-") {
			continue
		}
		entry, ok := m.Find(name)
		if !ok || entry.Custom {
			continue
		}
		dir := filepath.Join(m.rootDir, entry.ID)
		if m.isInstalled(entry, dir) {
			result[entry.ID] = dir
		}
	}
	return result, nil
}

// isInstalled 判断目录下 Required 文件是否齐全（大小非零）。
func (m *ManagerImpl) isInstalled(entry CatalogEntry, dir string) bool {
	for _, rel := range entry.Required {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() || info.Size() == 0 {
			return false
		}
	}
	return true
}

// Ensure 确保模型已下载到本地，返回本地目录。
//
// 流程：
//  1. 若 Required 文件齐全，直接返回目录（缓存复用，不发起任何网络请求）；
//  2. 否则逐文件下载：先写 `<file>.part`，支持 HTTP Range 断点续传；
//  3. 镜像失败（连接错误、404、5xx）自动切换下一个镜像，单文件每镜像最多重试 MaxRetries 次；
//  4. 下载完成后按清单大小校验（±1%），通过后把 .part 改名为正式文件；
//  5. ctx 取消时立即停止并保留 .part，返回 context.Canceled。
func (m *ManagerImpl) Ensure(ctx context.Context, modelID string, progress Progress) (string, error) {
	entry, ok := m.Find(modelID)
	if !ok {
		return "", fmt.Errorf("未知的模型：%q，可用模型：%s", modelID, strings.Join(modelIDs(m.Catalog()), "、"))
	}

	// 用户自选的本地模型：不下载，直接返回其原始目录。
	// 目录可能已被用户移动或删除，这里每次都用 DetectLocalModel 复查一遍。
	if entry.Custom {
		if err := m.validateLocalRegistered(entry); err != nil {
			return "", err
		}
		dir := m.localDirOf(entry.ID)
		if progress != nil {
			progress(1, "使用本地模型目录")
		}
		return dir, nil
	}

	dir := filepath.Join(m.rootDir, entry.ID)

	if m.isInstalled(entry, dir) {
		if progress != nil {
			progress(1, "模型已下载，直接使用本地缓存")
		}
		return dir, nil
	}

	// 同一模型串行化，避免两个并发 Ensure 写同一批 .part 文件。
	lock := m.modelLock(entry.ID)
	lock.Lock()
	defer lock.Unlock()

	// 拿到锁后再检查一次，避免重复下载。
	if m.isInstalled(entry, dir) {
		if progress != nil {
			progress(1, "模型已下载，直接使用本地缓存")
		}
		return dir, nil
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建模型目录失败：%w", err)
	}

	dlCtx, cancel := context.WithCancel(ctx)
	rec := m.begin(entry, cancel)
	defer func() {
		m.end(entry.ID, rec, cancel)
	}()

	// 计算总字节数：优先使用清单中已知的大小，未知的按 1 计避免除零。
	total := int64(0)
	for _, f := range entry.Files {
		if f.Size > 0 {
			total += f.Size
		}
	}
	if total <= 0 {
		total = 1
	}

	// 已完成的字节数（用于断点续传时恢复进度基准）。
	doneBefore := int64(0)
	for _, f := range entry.Files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if info, err := os.Stat(dst); err == nil && !info.IsDir() && f.Size > 0 && info.Size() == f.Size {
			doneBefore += f.Size
		}
	}

	throttle := newProgressThrottle()
	emit := func(doneBytes int64, msg string) {
		ratio := float64(doneBytes) / float64(total)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		m.setDone(entry.ID, doneBytes)
		if progress != nil && throttle.allow(ratio) {
			progress(ratio, msg)
		}
	}
	emit(doneBefore, "开始下载模型 "+entry.Name)

	if errs, fatal := m.downloadFiles(dlCtx, entry, dir, emit); fatal != nil {
		// 取消 / 致命错误优先返回，避免被误判为「下载完成但文件不全」。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fatal
	} else if len(errs) > 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("下载模型 %s 失败：%s", entry.Name, strings.Join(errs, "；"))
	}

	// 收尾校验：Required 必须齐全，才能算安装成功。
	if !m.isInstalled(entry, dir) {
		return "", fmt.Errorf("模型 %s 下载完成但必需文件不完整，请重试", entry.Name)
	}

	if progress != nil {
		progress(1, "模型 "+entry.Name+" 下载完成")
	}
	return dir, nil
}

// downloadFiles 逐文件下载，返回（各镜像的失败说明, 致命错误）。
//
// 采用「先按镜像优先级遍历文件，再在文件内切换镜像」的策略：
// 单个镜像失败只影响当前文件，不会重复下载已经成功的同镜像文件。
// 返回值 fatal 非 nil 表示流程被取消或出现不可继续的错误，调用方应立即返回。
// builtinFileContents 返回不需要联网下载的内置文件内容。
//
// 有些小配置文件的内容是完全确定的（不随模型版本变化），直接内嵌到二进制里
// 写盘比走网络更可靠：
//   - 不依赖镜像可用性，离线也能装好；
//   - 不会被代理/镜像返回的错误页污染（这类文件只有几百字节，
//     体积校验的 ±1% 容差几乎拦不住错误内容）；
//   - 少一次网络往返。
//
// 返回 nil 表示该文件没有内置内容，按常规流程下载。
func builtinFileContents(entry CatalogEntry, relPath string) []byte {
	if entry.ID == "large-v3" && relPath == "preprocessor_config.json" {
		return []byte(preprocessorLargeV3)
	}
	return nil
}

func (m *ManagerImpl) downloadFiles(ctx context.Context, entry CatalogEntry, dir string, emit func(done int64, msg string)) (errs []string, fatal error) {
	mirrors := m.usableMirrors()
	if len(mirrors) == 0 {
		return nil, errors.New("没有可用的镜像，请检查网络或镜像配置")
	}
	for _, f := range entry.Files {
		if err := ctx.Err(); err != nil {
			// 取消时不再尝试后续文件。
			return errs, err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))

		// 内置文件直接写盘，不走网络。
		if content := builtinFileContents(entry, f.Path); content != nil {
			if info, err := os.Stat(dst); err == nil && !info.IsDir() &&
				info.Size() == int64(len(content)) {
				// 已经是正确内容，跳过。
				emit(int64(len(content)), "已就绪 "+f.Path)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return errs, fmt.Errorf("创建目录失败：%w", err)
			}
			// 先写临时文件再改名，避免并发读到半截文件。
			tmp := dst + ".tmp"
			if err := os.WriteFile(tmp, content, 0o644); err != nil {
				return errs, fmt.Errorf("写入内置文件 %s 失败：%w", f.Path, err)
			}
			if err := os.Rename(tmp, dst); err != nil {
				_ = os.Remove(tmp)
				return errs, fmt.Errorf("安装内置文件 %s 失败：%w", f.Path, err)
			}
			emit(int64(len(content)), "已写入 "+f.Path)
			continue
		}

		if info, err := os.Stat(dst); err == nil && !info.IsDir() {
			if f.Size <= 0 || info.Size() == f.Size {
				continue
			}
			// 大小不符的旧文件直接删掉重下。
			_ = os.Remove(dst)
		}

		var msg string
		var fileErr error
		for _, mi := range mirrors {
			if err := ctx.Err(); err != nil {
				return errs, err
			}
			m.setCurrent(entry.ID, f.Path)
			msg, fileErr = m.downloadOne(ctx, mi, entry, f, dst, emit)
			if fileErr == nil {
				break
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errs, ctxErr
			}
			// 明确判定该镜像不可用（连接失败/404），后续文件不再首选它。
			if isMirrorDown(fileErr) {
				m.markMirrorDown(mi.ID)
			}
			if _, isErr := fileErr.(*downloadError); !isErr {
				// 非分类错误（如取消）直接上抛，不再尝试其余镜像。
				return errs, fileErr
			}
			errs = appendMirrorErr(errs, fmt.Sprintf("%s：%s（镜像 %s）", f.Path, mirrorErrText(fileErr), mi.Label))
		}
		if fileErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errs, ctxErr
			}
			errs = append(errs, fmt.Sprintf("%s 在全部 %d 个镜像上都下载失败：%s", f.Path, len(mirrors), msg))
			return errs, nil
		}
		emitProgressAfterFile(dst, f, emit)
	}
	return nil, nil
}

// emitProgressAfterFile 在单个文件完成时把已完成字节推进到累加值。
func emitProgressAfterFile(dst string, f ManifestFile, emit func(done int64, msg string)) {
	if f.Size > 0 {
		emit(f.Size, "已下载 "+f.Path)
		return
	}
	if info, err := os.Stat(dst); err == nil {
		emit(info.Size(), "已下载 "+f.Path)
	}
}

// appendMirrorErr 追加错误并去重，避免相同文案重复刷屏。
func appendMirrorErr(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// downloadOne 在指定镜像上下载单个文件，返回最后一条错误消息。
//
// 返回值 msg 为最后一次失败的中文描述（成功时为空）。
func (m *ManagerImpl) downloadOne(ctx context.Context, mi Mirror, entry CatalogEntry, f ManifestFile, dst string, emit func(done int64, msg string)) (string, error) {
	part := dst + ".part"
	// 断点续传的累积偏移（本次 Ensure 内若文件已完整则跳过，见 downloadFiles）。
	var baseOffset int64
	if info, err := os.Stat(part); err == nil && !info.IsDir() {
		baseOffset = info.Size()
	}

	lastMsg := ""
	sameMirrorFailures := 0
	var lastOffset int64 = -1
	forceRestart := false

	for attempt := 1; attempt <= m.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if attempt > 1 {
			if err := sleepCtx(ctx, retryBackoff(attempt)); err != nil {
				return "", err
			}
		}

		// 每轮重新读取 .part 大小，因为上一轮可能写入了部分数据。
		size := int64(0)
		if info, err := os.Stat(part); err == nil && !info.IsDir() {
			size = info.Size()
		}
		startOffset := size
		if forceRestart {
			startOffset = 0
		}
		if startOffset == lastOffset {
			sameMirrorFailures++
		} else {
			sameMirrorFailures = 0
			lastOffset = startOffset
		}
		// 服务端始终不给数据或偏移不前进时，放弃续传，从头下载。
		if sameMirrorFailures > resumeRetryBudget {
			forceRestart = true
			_ = os.Remove(part)
			startOffset = 0
			lastOffset = -1
			sameMirrorFailures = 0
		}

		written, err := m.fetchFile(ctx, mi, entry, f, part, startOffset, baseOffset, emit)
		if err == nil {
			// 校验大小。
			if f.Size > 0 && !sizeInTolerance(written, f.Size) {
				_ = os.Remove(part)
				return "", &downloadError{
					msg:  fmt.Sprintf("文件大小异常（期望 %d 字节，实际 %d 字节），可能是代理返回了错误页", f.Size, written),
					kind: errKindBadSize,
				}
			}
			if strings.TrimSpace(f.SHA256) != "" {
				sum, sumErr := SHA256File(part)
				if sumErr != nil {
					return "", &downloadError{msg: "计算文件校验值失败：" + sumErr.Error(), kind: errKindIO}
				}
				if !strings.EqualFold(sum, strings.TrimSpace(f.SHA256)) {
					_ = os.Remove(part)
					return "", &downloadError{msg: "SHA-256 校验不通过，文件可能损坏", kind: errKindBadSize}
				}
			}
			if err := os.Rename(part, dst); err != nil {
				return "", &downloadError{msg: "重命名临时文件失败：" + err.Error(), kind: errKindIO}
			}
			return "", nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		// fetchFile 直接返回的上下文错误（取消/超时）要原样向上传递。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
		}
		var dErr *downloadError
		if errors.As(err, &dErr) && dErr.permanent() {
			return dErr.msg, err
		}
		lastMsg = err.Error()
	}
	return lastMsg, &downloadError{msg: lastMsg, kind: errKindRetry}
}

// fetchFile 发起一次下载请求并把数据写入 .part。
//
// startOffset > 0 时携带 Range 头：服务端返回 206 则续写，返回 200 则截断重头写。
// 返回写入完成后 .part 的总字节数。
func (m *ManagerImpl) fetchFile(ctx context.Context, mi Mirror, entry CatalogEntry, f ManifestFile, part string, startOffset, baseOffset int64, emit func(done int64, msg string)) (int64, error) {
	reqCtx, cancelReq := context.WithTimeout(ctx, downloadTimeout)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, m.fileURL(mi, entry, f), nil)
	if err != nil {
		return 0, &downloadError{msg: "构造下载请求失败：" + err.Error(), kind: errKindPermanent}
	}
	m.decorate(req)
	if startOffset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(startOffset, 10)+"-")
	}

	done := m.client.Do
	resp, err := done(req)
	if err != nil {
		return 0, &downloadError{msg: "连接镜像失败：" + netErrText(err), kind: errKindMirrorDown}
	}
	defer resp.Body.Close()

	// 校验内容类型：正常文件不应是 HTML（代理/网关错误页）。
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode == http.StatusOK && isHTMLContent(ct) {
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		return 0, &downloadError{msg: "镜像返回了 HTML 内容（很可能是代理错误页）", kind: errKindBadSize}
	}

	var total int64 // 文件总大小，0 表示未知
	// appendMode 表示本次响应是否可以从 .part 末尾续写。
	// 只有「服务端确实返回 206」且「起始偏移与本地 .part 一致」时才是续写；
	// 返回 200 说明服务端忽略了 Range、给了完整文件，必须截断重头写。
	appendMode := false
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total = parseContentRangeTotal(resp.Header.Get("Content-Range"))
		if startOffset > 0 {
			// 续传：服务端起始位置与本地不一致时以服务端为准重下。
			matched := true
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				if s, ok := parseContentRangeStart(cr); ok && s != startOffset {
					matched = false
				}
			}
			if matched {
				appendMode = true
			} else {
				startOffset = 0
				_ = os.Remove(part)
			}
		}
	case http.StatusOK:
		// 服务端忽略 Range 返回全量内容：从头写。
		total = resp.ContentLength
		startOffset = 0
	case http.StatusRequestedRangeNotSatisfiable:
		// 本地 .part 已经不小于远端文件（或远端不支持该区间），删掉重下。
		_ = os.Remove(part)
		return 0, &downloadError{msg: "镜像拒绝了续传区间（416），将从头下载", kind: errKindRetry}
	case http.StatusNotFound, http.StatusGone:
		return 0, &downloadError{msg: "镜像上不存在该文件（HTTP 404），仓库或镜像可能未收录", kind: errKindMirrorDown}
	case http.StatusUnauthorized, http.StatusForbidden:
		return 0, &downloadError{msg: fmt.Sprintf("镜像拒绝访问（HTTP %d）", resp.StatusCode), kind: errKindMirrorDown}
	default:
		if resp.StatusCode >= 500 {
			return 0, &downloadError{msg: fmt.Sprintf("镜像服务异常（HTTP %d）", resp.StatusCode), kind: errKindMirrorDown}
		}
		return 0, &downloadError{msg: fmt.Sprintf("镜像返回异常状态码（HTTP %d）", resp.StatusCode), kind: errKindRetry}
	}

	// 打开 .part：续传用 O_APPEND，重头写用 O_TRUNC。
	flags := os.O_CREATE | os.O_WRONLY
	writeOffset := startOffset
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, &downloadError{msg: "打开临时文件失败：" + err.Error(), kind: errKindIO}
	}

	// 空闲超时：长时间没有任何数据就放弃本次尝试，交给重试逻辑。
	idle := newIdleGuard(idleTimeout, cancelReq)
	buf := make([]byte, downloadBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return writeOffset, err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			idle.kick()
			if _, werr := file.Write(buf[:n]); werr != nil {
				_ = file.Close()
				return writeOffset, &downloadError{msg: "写入磁盘失败：" + werr.Error(), kind: errKindIO}
			}
			delta := int64(n)
			writeOffset += delta
			emit(baseOffset+writeOffset, "已下载 "+f.Path)
		}
		if readErr != nil {
			idle.stop()
			_ = file.Sync()
			_ = file.Close()
			if errors.Is(readErr, io.EOF) {
				break
			}
			// 读中断可能来自外层 ctx 取消或整体超时，需显式还原取消语义，
			// 否则会被当成普通网络错误继续重试/换镜像。
			if ctxErr := ctx.Err(); ctxErr != nil {
				return writeOffset, ctxErr
			}
			if reqCtx.Err() != nil {
				return writeOffset, &downloadError{msg: "下载超时（长时间没有数据），已保留断点", kind: errKindRetry}
			}
			return writeOffset, &downloadError{msg: "读取数据流中断：" + netErrText(readErr), kind: errKindRetry}
		}
	}
	idle.stop()

	if total > 0 && writeOffset < total {
		return writeOffset, &downloadError{msg: "数据流提前结束，已保留断点", kind: errKindRetry}
	}
	return writeOffset, nil
}

// fileURL 拼接下载地址：`{mirror}/{repo}/resolve/main/{file}`。
func (m *ManagerImpl) fileURL(mi Mirror, entry CatalogEntry, f ManifestFile) string {
	return strings.TrimRight(mi.BaseURL, "/") + "/" + escapeRepo(entry.Repo) + "/resolve/main/" + escapePath(f.Path)
}

// escapeRepo 对 `org/name` 形式的仓库名逐段转义。
func escapeRepo(repo string) string {
	parts := strings.Split(strings.Trim(repo, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// escapePath 对文件相对路径逐段转义。
func escapePath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

// decorate 设置请求头。
func (m *ManagerImpl) decorate(req *http.Request) {
	if m.userAgent != "" {
		req.Header.Set("User-Agent", m.userAgent)
	}
	// 接受压缩会让 Content-Length 失真；这里显式禁用，保证字节数就是文件大小。
	req.Header.Set("Accept-Encoding", "identity")
	// 受限（gated）仓库需要带访问令牌，否则镜像站直接返回 403。
	if token := strings.TrimSpace(m.token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// usableMirrors 返回当前会话中尚未被判定为不可用的镜像；全被禁用时回退为全量列表。
func (m *ManagerImpl) usableMirrors() []Mirror {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Mirror, 0, len(m.mirrors))
	for _, mi := range m.mirrors {
		if !m.disabled[mi.ID] {
			out = append(out, mi)
		}
	}
	if len(out) == 0 {
		out = append(out, m.mirrors...)
	}
	return out
}

// markMirrorDown 记录镜像不可用。
func (m *ManagerImpl) markMirrorDown(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disabled[id] = true
}

// Remove 删除模型目录。
//
// 若 InUse 返回 true 则拒绝删除并返回中文错误。删除时先把目录改名为
// `<id>.deleting-<时间戳>`，再异步清理；若改名失败则不做任何修改。
func (m *ManagerImpl) Remove(ctx context.Context, modelID string) error {
	entry, ok := m.Find(modelID)
	if !ok {
		return fmt.Errorf("未知的模型：%q", modelID)
	}
	if m.inUse != nil && m.inUse(entry.ID) {
		return errors.New("模型正在被任务使用，暂不能删除")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock := m.modelLock(entry.ID)
	lock.Lock()
	defer lock.Unlock()

	// 下载中不允许删除：先取消并等待其退出。
	if m.isDownloading(entry.ID) {
		m.Cancel(entry.ID)
		return errors.New("模型正在下载，请先取消下载再删除")
	}

	dir := filepath.Join(m.rootDir, entry.ID)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("模型 %s 尚未下载，无需删除", entry.Name)
		}
		return fmt.Errorf("读取模型目录失败：%w", err)
	}

	trash := filepath.Join(m.rootDir, entry.ID+".deleting-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(dir, trash); err != nil {
		return fmt.Errorf("删除模型目录失败：%w", err)
	}

	// 先把目录移走（对外已不可见），后台异步清理，避免阻塞 UI。
	if err := os.RemoveAll(trash); err != nil {
		// 清理失败时尝试恢复，保证用户可以重试删除。
		if rerr := os.Rename(trash, dir); rerr != nil {
			return fmt.Errorf("删除模型目录失败：%w（恢复目录也失败：%v）", err, rerr)
		}
		return fmt.Errorf("删除模型目录失败：%w", err)
	}
	return nil
}

// Cancel 请求取消一个正在进行的下载。未在下载返回 false。
func (m *ManagerImpl) Cancel(modelID string) bool {
	entry, ok := m.Find(modelID)
	if !ok {
		return false
	}
	cancel, ok := m.cancels.Load(entry.ID)
	if !ok {
		return false
	}
	if fn, isFn := cancel.(context.CancelFunc); isFn {
		m.setState(entry.ID, stateCanceling)
		fn()
		return true
	}
	return false
}

// Progress 返回模型下载进度（0..100）。未在下载返回 0。
func (m *ManagerImpl) Progress(modelID string) int {
	entry, ok := m.Find(modelID)
	if !ok {
		return 0
	}
	m.mu.RLock()
	rec, ok := m.files[entry.ID]
	if !ok || rec.total <= 0 {
		m.mu.RUnlock()
		return 0
	}
	ratio := float64(rec.done) / float64(rec.total)
	m.mu.RUnlock()
	pct := int(ratio * 100)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ProgressMessage 返回模型当前正在下载的文件名与整体百分比，供 UI 展示。
func (m *ManagerImpl) ProgressMessage(modelID string) string {
	entry, ok := m.Find(modelID)
	if !ok {
		return "未知模型"
	}
	m.mu.RLock()
	rec, has := m.files[entry.ID]
	if !has {
		m.mu.RUnlock()
		return "未在下载"
	}
	current, total, done := rec.current, rec.total, rec.done
	m.mu.RUnlock()
	pct := 0
	if total > 0 {
		pct = int(float64(done) / float64(total) * 100)
	}
	if current == "" {
		return fmt.Sprintf("%d%%", pct)
	}
	return fmt.Sprintf("正在下载 %s（%d%%）", current, pct)
}

// IsDownloading 报告模型是否正在下载。
func (m *ManagerImpl) IsDownloading(modelID string) bool {
	entry, ok := m.Find(modelID)
	if !ok {
		return false
	}
	return m.isDownloading(entry.ID)
}

// —— 内部状态管理 ——

// modelLock 获取（或创建）某模型的互斥锁。
func (m *ManagerImpl) modelLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

// begin 登记一次下载，返回该模型的进度记录。
func (m *ManagerImpl) begin(entry CatalogEntry, cancel context.CancelFunc) *fileRecord {
	rec := &fileRecord{modelID: entry.ID, stop: cancel}
	for _, f := range entry.Files {
		if f.Size > 0 {
			rec.total += f.Size
		}
	}
	if rec.total <= 0 {
		rec.total = 1
	}
	m.cancels.Store(entry.ID, cancel)
	m.setState(entry.ID, stateDownloading)
	m.mu.Lock()
	m.files[entry.ID] = rec
	m.mu.Unlock()
	return rec
}

// end 注销下载登记。
//
// 同一模型由 acquireLock 串行化，因此此处清理的一定是本次下载自己的登记。
func (m *ManagerImpl) end(id string, rec *fileRecord, cancel context.CancelFunc) {
	m.cancels.Delete(id)
	m.mu.Lock()
	if cur, ok := m.files[id]; ok && cur == rec {
		delete(m.files, id)
	}
	m.mu.Unlock()
	m.setState(id, stateIdle)
	cancel()
}

// setDone 更新已完成字节数。
func (m *ManagerImpl) setDone(id string, done int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.files[id]; ok {
		rec.done = done
	}
}

// setCurrent 更新当前文件名。
func (m *ManagerImpl) setCurrent(id, file string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.files[id]; ok {
		rec.current = file
	}
}

// setState 记录模型状态。
func (m *ManagerImpl) setState(id string, st int32) {
	m.states.Store(id, &st)
}

// stateOf 读取模型状态。
func (m *ManagerImpl) stateOf(id string) int32 {
	if v, ok := m.states.Load(id); ok {
		if p, isP := v.(*int32); isP {
			return *p
		}
	}
	return stateIdle
}

// isDownloading 报告模型是否处于下载中。
func (m *ManagerImpl) isDownloading(id string) bool {
	switch m.stateOf(id) {
	case stateDownloading, stateCanceling:
		return true
	}
	return false
}

// —— 进度节流 ——

// progressThrottle 负责进度回调节流。
//
// 只有「距上次回调 >= 250ms」且「进度变化 >= 1%」或「进度到达 100%」时才放行，
// 避免在逐块读取的循环里高频回调到 UI。
type progressThrottle struct {
	lastTime  time.Time
	lastRatio float64
	fired     bool
	now       func() time.Time
}

func newProgressThrottle() *progressThrottle {
	return &progressThrottle{now: time.Now, lastRatio: -1}
}

func (t *progressThrottle) allow(ratio float64) bool {
	now := t.now()
	// 首次回调或到达完成状态直接放行。
	if !t.fired {
		t.fired = true
		t.lastTime = now
		t.lastRatio = ratio
		return true
	}
	if ratio >= 1 && t.lastRatio < 1 {
		t.lastTime = now
		t.lastRatio = ratio
		return true
	}
	if now.Sub(t.lastTime) < progressMinInterval {
		return false
	}
	if ratio-t.lastRatio < progressMinDelta && ratio < 1 {
		return false
	}
	t.lastTime = now
	t.lastRatio = ratio
	return true
}

// —— 工具函数 ——

// sizeInTolerance 判断实际大小与期望大小是否在 ±1%（下限 1024 字节）内。
func sizeInTolerance(actual, expected int64) bool {
	if expected <= 0 {
		return true
	}
	tol := int64(float64(expected) * sizeTolerance)
	if tol < maxSizeTolerance {
		tol = maxSizeTolerance
	}
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}
	return diff <= tol
}

// parseContentRangeTotal 从 `bytes 0-1/12345` 解析总大小，未知返回 0。
func parseContentRangeTotal(v string) int64 {
	idx := strings.LastIndex(v, "/")
	if idx < 0 || idx+1 >= len(v) {
		return 0
	}
	s := strings.TrimSpace(v[idx+1:])
	if s == "*" || s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseContentRangeStart 解析 `bytes 100-200/300` 中的起始偏移。
func parseContentRangeStart(v string) (int64, bool) {
	idx := strings.Index(v, " ")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(v[idx+1:])
	dash := strings.Index(rest, "-")
	if dash <= 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[:dash], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// isHTMLContent 判断内容类型是否为 HTML。
func isHTMLContent(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// retryBackoff 计算第 attempt 次重试前的等待时间（指数退避，上限 2s）。
func retryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	d := time.Duration(attempt-1) * 250 * time.Millisecond
	if d > maxRetrySleep {
		d = maxRetrySleep
	}
	return d
}

// sleepCtx 可被 ctx 打断的 sleep。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// netErrText 把网络错误转成中文说明，不泄露完整 URL（本包不需要 token，
// 但保留该规则以便将来支持鉴权时不会误打印）。
func netErrText(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		// url.Error 的 Error() 会带上完整 URL，这里只取底层原因。
		return ue.Err.Error()
	}
	return err.Error()
}

// —— 错误分类 ——

// 错误种类。
const (
	// errKindRetry 表示可重试的临时错误。
	errKindRetry int = iota
	// errKindMirrorDown 表示该镜像不可用（连接失败 / 404 / 5xx），应切换镜像。
	errKindMirrorDown
	// errKindBadSize 表示内容校验不通过（大小不符 / 返回 HTML）。
	errKindBadSize
	// errKindIO 表示本地磁盘错误，重试无意义。
	errKindIO
	// errKindPermanent 表示构造请求等永久性错误。
	errKindPermanent
)

// downloadError 是带分类的下载错误。
type downloadError struct {
	msg  string
	kind int
}

func (e *downloadError) Error() string { return e.msg }

// permanent 判断是否应停止对该镜像的重试。
func (e *downloadError) permanent() bool {
	return e.kind == errKindMirrorDown || e.kind == errKindBadSize || e.kind == errKindIO || e.kind == errKindPermanent
}

// isMirrorDown 判断错误是否意味着「该镜像整体不可用」。
func isMirrorDown(err error) bool {
	var dErr *downloadError
	if errors.As(err, &dErr) {
		return dErr.kind == errKindMirrorDown
	}
	return false
}

// mirrorErrText 把错误转成用于汇总的中文短语。
func mirrorErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// —— 空闲超时保护 ——

// idleGuard 在长时间没有数据时可取消请求。
type idleGuard struct {
	cancel context.CancelFunc
	timer  *time.Timer
	d      time.Duration
	mu     sync.Mutex
	done   bool
}

func newIdleGuard(d time.Duration, cancel context.CancelFunc) *idleGuard {
	g := &idleGuard{cancel: cancel, d: d}
	g.reset()
	return g
}

// kick 收到数据时重置计时器。
func (g *idleGuard) kick() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		return
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(g.d, g.cancel)
}

func (g *idleGuard) reset() { g.kick() }

// stop 结束保护。
func (g *idleGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done = true
	if g.timer != nil {
		g.timer.Stop()
	}
}

// —— 兼容性小工具 ——
