package modelmanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMirror 是一个可编程的假镜像服务器，用于离线模拟 HF 同构端点。
//
// 支持：Range 续传（206）、强制返回 200（忽略 Range）、返回 500、
// 返回 HTML 错误页、返回 404 等场景，并统计请求次数。
type fakeMirror struct {
	*httptest.Server

	mu sync.Mutex
	// files 是路径 -> 内容。
	files map[string][]byte
	// failFirst 表示前 N 次请求直接返回 500。
	failFirst int32
	// alwaysHTML 为 true 时所有请求返回 HTML 错误页且状态 200。
	alwaysHTML bool
	// ignoreRange 为 true 时即使带 Range 也只返回 200 全量内容。
	ignoreRange bool
	// missing 是应返回 404 的路径。
	missing map[string]bool
	// holdOn 非空时，命中该路径的请求会阻塞直到 release 被关闭（用于取消测试）。
	holdOn  string
	release chan struct{}
	// requests 统计每个路径的请求次数。
	requests map[string]int
	// rangeHeaders 记录收到的 Range 头。
	rangeHeaders []string
	// blockChunk，若 > 0，则每次 Write 后 sleep 该时长，模拟慢速下载。
	blockChunk time.Duration
}

func newFakeMirror(t *testing.T) *fakeMirror {
	t.Helper()
	fm := &fakeMirror{
		files:    make(map[string][]byte),
		missing:  make(map[string]bool),
		requests: make(map[string]int),
	}
	fm.Server = httptest.NewServer(http.HandlerFunc(fm.handle))
	t.Cleanup(fm.Close)
	return fm
}

// setFile 写入一个可下载文件。
func (fm *fakeMirror) setFile(path string, content []byte) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.files[path] = content
}

// requestCount 返回某个路径被请求的次数。
func (fm *fakeMirror) requestCount(path string) int {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.requests[path]
}

// totalRequests 返回所有请求总数。
func (fm *fakeMirror) totalRequests() int {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	n := 0
	for _, v := range fm.requests {
		n += v
	}
	return n
}

func (fm *fakeMirror) handle(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
		return
	default:
	}

	fm.mu.Lock()
	fm.requests[r.URL.Path]++
	content, exists := fm.files[r.URL.Path]
	missing := fm.missing[r.URL.Path]
	ignoreRange := fm.ignoreRange
	alwaysHTML := fm.alwaysHTML
	holdOn := fm.holdOn
	release := fm.release
	chunk := fm.blockChunk
	if rng := r.Header.Get("Range"); rng != "" {
		fm.rangeHeaders = append(fm.rangeHeaders, rng)
	}
	fm.mu.Unlock()

	if holdOn != "" && r.URL.Path == holdOn && release != nil {
		// 先写一点数据，确保客户端已经拿到响应头，再阻塞。
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		if len(content) > 0 {
			n := len(content) / 4
			if n < 1 {
				n = 1
			}
			_, _ = w.Write(content[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return
	}

	if alwaysHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>502 Bad Gateway</body></html>"))
		return
	}

	if n := atomic.LoadInt32(&fm.failFirst); n > 0 {
		atomic.AddInt32(&fm.failFirst, -1)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	if missing || !exists {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	total := len(content)
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && !ignoreRange {
		start, ok := parseRangeStart(rangeHeader)
		if ok && start < int64(total) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, total-1, total))
			w.WriteHeader(http.StatusPartialContent)
			body := content[start:]
			if chunk > 0 {
				writeSlowly(w, body, chunk)
			} else {
				_, _ = w.Write(body)
			}
			return
		}
		if ok && start >= int64(total) && total > 0 {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", fmt.Sprint(total))
	w.WriteHeader(http.StatusOK)
	if chunk > 0 {
		writeSlowly(w, content, chunk)
	} else {
		_, _ = w.Write(content)
	}
}

// writeSlowly 分块写入以模拟慢速链路，并支持 ctx 取消。
func writeSlowly(w http.ResponseWriter, body []byte, d time.Duration) {
	flusher, _ := w.(http.Flusher)
	const step = 8 * 1024
	for i := 0; i < len(body); i += step {
		end := i + step
		if end > len(body) {
			end = len(body)
		}
		if _, err := w.Write(body[i:end]); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(d)
	}
}

// parseRangeStart 解析 `bytes=100-` 的起始偏移。
func parseRangeStart(v string) (int64, bool) {
	v = strings.TrimPrefix(v, "bytes=")
	dash := strings.Index(v, "-")
	if dash <= 0 {
		return 0, false
	}
	var n int64
	if _, err := fmt.Sscan(v[:dash], &n); err != nil {
		return 0, false
	}
	return n, true
}

// testFixture 构造一个使用假清单（体积很小）的测试环境。
type testFixture struct {
	root    string
	mirrors []*fakeMirror
	catalog []CatalogEntry
	mgr     *ManagerImpl
}

// testFiles 是小体积测试文件及其内容。
var testFiles = []struct {
	name    string
	content []byte
}{
	{"config.json", bytes.Repeat([]byte("c"), 2048)},
	{"model.bin", bytes.Repeat([]byte("m"), 256*1024)},
	{"tokenizer.json", bytes.Repeat([]byte("t"), 8192)},
	{"vocabulary.txt", bytes.Repeat([]byte("v"), 4096)},
}

// newFixture 创建一个带 n 个假镜像的管理器。
func newFixture(t *testing.T, mirrorCount int) *testFixture {
	t.Helper()
	root := t.TempDir()
	f := &testFixture{root: root}

	files := make([]ManifestFile, 0, len(testFiles))
	for _, tf := range testFiles {
		files = append(files, ManifestFile{Path: tf.name, Size: int64(len(tf.content))})
	}
	f.catalog = []CatalogEntry{{
		ID:          "tiny",
		Name:        "Whisper Tiny",
		Repo:        "Systran/faster-whisper-tiny",
		Description: "测试用极小模型",
		SizeBytes:   int64(len(testFiles[0].content) + len(testFiles[1].content)),
		Files:       files,
		Required:    []string{"config.json", "model.bin"},
	}}

	mirrors := make([]Mirror, 0, mirrorCount)
	for i := 0; i < mirrorCount; i++ {
		fm := newFakeMirror(t)
		f.mirrors = append(f.mirrors, fm)
		for _, tf := range testFiles {
			fm.setFile(fileURLPath("tiny", tf.name), tf.content)
		}
		mirrors = append(mirrors, Mirror{
			ID:      fmt.Sprintf("mirror-%d", i+1),
			Label:   fmt.Sprintf("测试镜像 %d", i+1),
			BaseURL: fm.URL,
		})
	}

	f.mgr = New(Options{
		RootDir:    root,
		Mirrors:    mirrors,
		Catalog:    f.catalog,
		MaxRetries: 2,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	})
	return f
}

// fileURLPath 返回假镜像上的文件路径。
func fileURLPath(id, file string) string {
	return "/Systran/faster-whisper-" + id + "/resolve/main/" + file
}

// installedFiles 返回模型目录下除 .part 外的文件列表。
func installedFiles(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := make(map[string]int64)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取模型目录失败：%v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = info.Size()
	}
	return out
}

// hasPart 判断目录下是否存在 .part 文件。
func hasPart(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			return true
		}
	}
	return false
}

// partFiles 返回目录下所有 .part 文件的路径。
func partFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// —— 1. 正常下载 ——

func TestEnsureDownloadsAllFilesAndMarksInstalled(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	var lastRatio float64
	var lastMsg string
	dir, err := f.mgr.Ensure(ctx, "tiny", func(ratio float64, msg string) {
		lastRatio = ratio
		lastMsg = msg
	})
	if err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	wantDir := filepath.Join(f.root, "tiny")
	if dir != wantDir {
		t.Fatalf("本地目录不一致：got %q want %q", dir, wantDir)
	}
	if lastRatio != 1 {
		t.Fatalf("最终进度应为 1，实际 %v", lastRatio)
	}
	if lastMsg == "" {
		t.Fatalf("进度消息不应为空")
	}

	got := installedFiles(t, dir)
	for _, tf := range testFiles {
		size, ok := got[tf.name]
		if !ok {
			t.Fatalf("缺少文件 %s", tf.name)
		}
		if size != int64(len(tf.content)) {
			t.Fatalf("文件 %s 大小不符：got %d want %d", tf.name, size, len(tf.content))
		}
	}
	if hasPart(t, dir) {
		t.Fatalf("下载完成后不应残留 .part 文件")
	}

	installed, err := f.mgr.Installed(ctx)
	if err != nil {
		t.Fatalf("Installed 失败：%v", err)
	}
	if installed["tiny"] != wantDir {
		t.Fatalf("模型未被标记为已安装：%v", installed)
	}
}

// —— 2. 缓存复用 ——

func TestEnsureReusesInstalledModelWithoutRequests(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	if _, err := f.mgr.Ensure(ctx, "tiny", nil); err != nil {
		t.Fatalf("首次下载失败：%v", err)
	}
	before := f.mirrors[0].totalRequests()

	dir, err := f.mgr.Ensure(ctx, "tiny", nil)
	if err != nil {
		t.Fatalf("二次 Ensure 失败：%v", err)
	}
	if dir != filepath.Join(f.root, "tiny") {
		t.Fatalf("目录不一致：%q", dir)
	}
	if after := f.mirrors[0].totalRequests(); after != before {
		t.Fatalf("缓存复用时不应再发请求，请求数从 %d 变为 %d", before, after)
	}
}

// —— 3. 断点续传（206） ——

func TestEnsureResumesFromPartialPartFile(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	dir := filepath.Join(f.root, "tiny")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 预置一个只下了一半的 model.bin.part。
	modelContent := testFiles[1].content
	half := len(modelContent) / 2
	if err := os.WriteFile(filepath.Join(dir, "model.bin.part"), modelContent[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.mgr.Ensure(ctx, "tiny", nil); err != nil {
		t.Fatalf("续传失败：%v", err)
	}

	fm := f.mirrors[0]
	fm.mu.Lock()
	rangeHeaders := append([]string(nil), fm.rangeHeaders...)
	fm.mu.Unlock()
	wantRange := fmt.Sprintf("bytes=%d-", half)
	found := false
	for _, h := range rangeHeaders {
		if h == wantRange {
			found = true
		}
	}
	if !found {
		t.Fatalf("未发送期望的续传 Range 头 %q，实际收到 %v", wantRange, rangeHeaders)
	}

	final, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil {
		t.Fatalf("读取最终文件失败：%v", err)
	}
	if !bytes.Equal(final, modelContent) {
		t.Fatalf("续传后内容不一致：got %d bytes want %d bytes", len(final), len(modelContent))
	}
	if hasPart(t, dir) {
		t.Fatalf("续传完成后不应残留 .part 文件")
	}
}

// 服务端忽略 Range 返回 200 时，应从头重写而不是拼接出错。
func TestEnsureRestartsWhenServerIgnoresRange(t *testing.T) {
	f := newFixture(t, 1)
	f.mirrors[0].ignoreRange = true
	ctx := context.Background()
	dir := filepath.Join(f.root, "tiny")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelContent := testFiles[1].content
	if err := os.WriteFile(filepath.Join(dir, "model.bin.part"), modelContent[:1024], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.Ensure(ctx, "tiny", nil); err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	final, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(final, modelContent) {
		t.Fatalf("服务端返回 200 时应重头写，got %d bytes want %d bytes", len(final), len(modelContent))
	}
}

// —— 4. 镜像降级 ——

func TestEnsureFallsBackToNextMirrorOnServerError(t *testing.T) {
	f := newFixture(t, 2)
	// 首个镜像对全部请求返回 500。
	atomic.StoreInt32(&f.mirrors[0].failFirst, 1000)
	ctx := context.Background()

	dir, err := f.mgr.Ensure(ctx, "tiny", nil)
	if err != nil {
		t.Fatalf("镜像降级后仍应成功，实际失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "model.bin")); err != nil {
		t.Fatalf("model.bin 未落盘：%v", err)
	}
	if n := f.mirrors[0].totalRequests(); n == 0 {
		t.Fatalf("首个镜像应至少被尝试过")
	}
	if n := f.mirrors[1].totalRequests(); n == 0 {
		t.Fatalf("第二个镜像应被使用")
	}
}

// 全部镜像失败时应返回中文错误，并说明尝试过的镜像。
func TestEnsureAllMirrorsFailReturnsChineseError(t *testing.T) {
	f := newFixture(t, 2)
	for _, fm := range f.mirrors {
		atomic.StoreInt32(&fm.failFirst, 1000)
	}
	_, err := f.mgr.Ensure(context.Background(), "tiny", nil)
	if err == nil {
		t.Fatalf("全部镜像失败时应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "失败") {
		t.Fatalf("错误信息应为中文，实际：%v", msg)
	}
	if !strings.Contains(msg, "测试镜像 1") || !strings.Contains(msg, "测试镜像 2") {
		t.Fatalf("错误信息应说明尝试过的镜像，实际：%v", msg)
	}
}

// 404 应被识别为镜像不可用并立即切换。
func TestEnsureSwitchesOnNotFound(t *testing.T) {
	f := newFixture(t, 2)
	f.mirrors[0].mu.Lock()
	f.mirrors[0].missing[fileURLPath("tiny", "model.bin")] = true
	f.mirrors[0].mu.Unlock()

	dir, err := f.mgr.Ensure(context.Background(), "tiny", nil)
	if err != nil {
		t.Fatalf("切换镜像后应成功：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "model.bin")); err != nil {
		t.Fatalf("model.bin 未落盘：%v", err)
	}
}

// —— 5. 取消与再次续传 ——

func TestEnsureCancelKeepsPartAndResumesLater(t *testing.T) {
	f := newFixture(t, 1)
	fm := f.mirrors[0]
	release := make(chan struct{})
	fm.mu.Lock()
	fm.holdOn = fileURLPath("tiny", "model.bin")
	fm.release = release
	fm.mu.Unlock()

	// 本用例只通过 ManagerImpl.Cancel 取消，不直接使用 ctx 的 cancel 函数。
	ctx := context.Background()
	done := make(chan struct{})
	var ensureErr error
	go func() {
		defer close(done)
		_, ensureErr = f.mgr.Ensure(ctx, "tiny", nil)
	}()

	// 等到 model.bin.part 出现且已有数据。
	// 注意：不能等「任意 .part 有数据」——config.json 等小文件会先下完并被
	// 重命名成正式文件，此时 model.bin 可能还没开始，取消后自然没有 .part，
	// 本用例就会随机失败。
	dir := filepath.Join(f.root, "tiny")
	modelPart := filepath.Join(dir, "model.bin.part")
	waitFor(t, 5*time.Second, func() bool {
		info, err := os.Stat(modelPart)
		return err == nil && info.Size() > 0
	})

	// 通过 Cancel(modelID) 取消，验证 sync.Map 中的取消函数生效。
	if !f.mgr.Cancel("tiny") {
		t.Fatalf("Cancel 应返回 true（模型正在下载）")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("取消后 Ensure 未能及时返回")
	}
	if !errors.Is(ensureErr, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际：%v", ensureErr)
	}
	if !hasPart(t, dir) {
		t.Fatalf("取消后应保留 .part 以便续传")
	}
	// 再次取消应返回 false（已不在下载）。
	if f.mgr.Cancel("tiny") {
		t.Fatalf("下载已结束，Cancel 应返回 false")
	}

	// 恢复镜像，再次 Ensure 应续传成功。
	close(release)
	fm.mu.Lock()
	fm.holdOn = ""
	fm.release = nil
	fm.mu.Unlock()

	if _, err := f.mgr.Ensure(context.Background(), "tiny", nil); err != nil {
		t.Fatalf("取消后续传失败：%v", err)
	}
	final, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(final, testFiles[1].content) {
		t.Fatalf("续传后内容不一致：got %d want %d", len(final), len(testFiles[1].content))
	}
	if hasPart(t, dir) {
		t.Fatalf("续传完成后不应残留 .part")
	}

	// Range 头应被用于续传。
	fm.mu.Lock()
	headers := append([]string(nil), fm.rangeHeaders...)
	fm.mu.Unlock()
	usedRange := false
	for _, h := range headers {
		if strings.HasPrefix(h, "bytes=") && h != "bytes=0-" {
			usedRange = true
		}
	}
	if !usedRange {
		t.Fatalf("续传时应发送非零起始的 Range 头，实际：%v", headers)
	}
}

// ctx 取消（不经 Cancel 方法）同样应保留 .part。
func TestEnsureContextCancelReturnsCanceled(t *testing.T) {
	f := newFixture(t, 1)
	fm := f.mirrors[0]
	release := make(chan struct{})
	fm.mu.Lock()
	fm.holdOn = fileURLPath("tiny", "model.bin")
	fm.release = release
	fm.mu.Unlock()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := f.mgr.Ensure(ctx, "tiny", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际：%v", err)
	}
}

// —— 6. 代理返回 HTML 错误页 ——

func TestEnsureRejectsHTMLErrorPage(t *testing.T) {
	f := newFixture(t, 1)
	f.mirrors[0].alwaysHTML = true
	_, err := f.mgr.Ensure(context.Background(), "tiny", nil)
	if err == nil {
		t.Fatalf("HTML 错误页应被判为失败")
	}
	if !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("错误信息应说明 HTML 错误页，实际：%v", err)
	}
	// 不应把 HTML 当成正式文件落盘。
	dir := filepath.Join(f.root, "tiny")
	if info, statErr := os.Stat(filepath.Join(dir, "model.bin")); statErr == nil {
		t.Fatalf("不应生成 model.bin，实际大小 %d", info.Size())
	}
}

// 大小与清单不符（非 HTML 内容）同样应判失败。
func TestEnsureRejectsSizeMismatch(t *testing.T) {
	f := newFixture(t, 1)
	// 把远端 model.bin 换成明显偏小的内容。
	f.mirrors[0].setFile(fileURLPath("tiny", "model.bin"), bytes.Repeat([]byte("x"), 1024))
	_, err := f.mgr.Ensure(context.Background(), "tiny", nil)
	if err == nil {
		t.Fatalf("大小不符应判失败")
	}
	if !strings.Contains(err.Error(), "大小异常") {
		t.Fatalf("错误信息应说明大小异常，实际：%v", err)
	}
}

// —— 7. Required 不齐不算已安装 ——

func TestInstalledRequiresAllRequiredFiles(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	dir := filepath.Join(f.root, "tiny")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 只有 config.json，缺 model.bin。
	if err := os.WriteFile(filepath.Join(dir, "config.json"), testFiles[0].content, 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := f.mgr.Installed(ctx)
	if err != nil {
		t.Fatalf("Installed 失败：%v", err)
	}
	if _, ok := installed["tiny"]; ok {
		t.Fatalf("Required 不齐时不应算已安装：%v", installed)
	}

	// 补齐 model.bin 后算已安装。
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), testFiles[1].content, 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err = f.mgr.Installed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if installed["tiny"] != dir {
		t.Fatalf("补齐 Required 后应算已安装：%v", installed)
	}
}

// 空文件不能被当作已安装。
func TestInstalledIgnoresEmptyFile(t *testing.T) {
	f := newFixture(t, 1)
	dir := filepath.Join(f.root, "tiny")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), testFiles[1].content, 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := f.mgr.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := installed["tiny"]; ok {
		t.Fatalf("空的 Required 文件不应算已安装")
	}
}

// —— 8. Remove 删除保护 ——

func TestRemoveBlockedWhenInUse(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	if _, err := f.mgr.Ensure(ctx, "tiny", nil); err != nil {
		t.Fatal(err)
	}
	inUse := true
	f.mgr.inUse = func(modelID string) bool {
		return inUse && modelID == "tiny"
	}

	err := f.mgr.Remove(ctx, "tiny")
	if err == nil {
		t.Fatalf("模型被占用时应拒绝删除")
	}
	if err.Error() != "模型正在被任务使用，暂不能删除" {
		t.Fatalf("错误信息不符合约定：%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "tiny", "model.bin")); statErr != nil {
		t.Fatalf("被拒绝删除时文件应保留：%v", statErr)
	}

	// 解除占用后可正常删除。
	inUse = false
	if err := f.mgr.Remove(ctx, "tiny"); err != nil {
		t.Fatalf("解除占用后应能删除：%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "tiny")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("删除后目录应不存在：%v", statErr)
	}
	installed, err := f.mgr.Installed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := installed["tiny"]; ok {
		t.Fatalf("删除后不应再算已安装")
	}
}

// 未下载的模型删除应返回中文提示。
func TestRemoveNotDownloaded(t *testing.T) {
	f := newFixture(t, 1)
	err := f.mgr.Remove(context.Background(), "tiny")
	if err == nil {
		t.Fatalf("未下载时删除应返回错误")
	}
	if !strings.Contains(err.Error(), "尚未下载") {
		t.Fatalf("错误信息应为中文提示，实际：%v", err)
	}
}

// —— 9. 进度节流 ——

func TestProgressCallbackIsThrottled(t *testing.T) {
	f := newFixture(t, 1)
	// 用较大的文件让读循环执行很多次。
	big := bytes.Repeat([]byte("z"), 2*1024*1024)
	f.mirrors[0].setFile(fileURLPath("tiny", "model.bin"), big)
	f.catalog[0].Files[1].Size = int64(len(big))
	f.mgr = New(Options{
		RootDir:    f.root,
		Mirrors:    f.mgr.Mirrors(),
		Catalog:    f.catalog,
		MaxRetries: 2,
	})

	var calls int32
	start := time.Now()
	if _, err := f.mgr.Ensure(context.Background(), "tiny", func(ratio float64, msg string) {
		atomic.AddInt32(&calls, 1)
	}); err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	elapsed := time.Since(start)
	got := int(atomic.LoadInt32(&calls))

	// 2MB / 256KB 缓冲区 ≈ 8 次读循环 + 4 个文件，未节流时回调次数会远多于 10。
	if got > 12 {
		t.Fatalf("进度回调未被有效节流：%d 次（耗时 %v）", got, elapsed)
	}
	if got == 0 {
		t.Fatalf("进度回调一次都没有触发")
	}
}

// 慢速下载时，节流应让回调次数明显少于数据块数。
func TestProgressCallbackThrottledOnSlowStream(t *testing.T) {
	f := newFixture(t, 1)
	big := bytes.Repeat([]byte("q"), 512*1024)
	f.mirrors[0].setFile(fileURLPath("tiny", "model.bin"), big)
	f.mirrors[0].blockChunk = 15 * time.Millisecond // 64 块 → 约 960ms
	f.catalog[0].Files[1].Size = int64(len(big))
	f.mgr = New(Options{
		RootDir:    f.root,
		Mirrors:    f.mgr.Mirrors(),
		Catalog:    f.catalog,
		MaxRetries: 2,
	})

	var calls int32
	if _, err := f.mgr.Ensure(context.Background(), "tiny", func(ratio float64, msg string) {
		atomic.AddInt32(&calls, 1)
	}); err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	got := int(atomic.LoadInt32(&calls))
	if got >= 64 {
		t.Fatalf("慢速下载时回调次数 %d 不应接近数据块数 64", got)
	}
}

// —— 10. 清单与别名 ——

func TestDefaultCatalogContainsCoreModels(t *testing.T) {
	cat := DefaultCatalog()
	want := map[string]bool{
		"tiny": false, "base": false, "small": false, "medium": false, "large-v3": false,
		"cohere-transcribe": false,
	}
	for _, e := range cat {
		if _, ok := want[e.ID]; ok {
			want[e.ID] = true
		}
		switch e.Engine {
		case EngineTransformers:
			// transformers 格式：safetensors 权重，不需要 CTranslate2 的 model.bin。
			hasConfig, hasModel, hasTokenizer := false, false, false
			for _, f := range e.Files {
				switch f.Path {
				case "config.json":
					hasConfig = true
				case "model.safetensors":
					hasModel = true
				case "tokenizer.json", "tokenizer.model":
					hasTokenizer = true
				}
				if f.Size <= 0 {
					t.Fatalf("%s 的文件 %s 缺少大小", e.ID, f.Path)
				}
			}
			if !hasConfig || !hasModel || !hasTokenizer {
				t.Fatalf("%s 文件清单不完整：config=%v model=%v tokenizer=%v",
					e.ID, hasConfig, hasModel, hasTokenizer)
			}
			if e.Repo == "" {
				t.Fatalf("%s 缺少下载仓库", e.ID)
			}
		default:
			// faster-whisper 系列必须来自官方 CTranslate2 转换仓库。
			if e.Repo != "Systran/faster-whisper-"+e.ID {
				t.Fatalf("%s 的仓库名不符合约定：%s", e.ID, e.Repo)
			}
			hasConfig, hasModel, hasTok, hasVocab := false, false, false, false
			for _, f := range e.Files {
				switch f.Path {
				case "config.json":
					hasConfig = true
				case "model.bin":
					hasModel = true
				case "tokenizer.json":
					hasTok = true
				case "vocabulary.txt", "vocabulary.json":
					hasVocab = true
				}
				if f.Size <= 0 {
					t.Fatalf("%s 的文件 %s 缺少大小", e.ID, f.Path)
				}
			}
			if !hasConfig || !hasModel || !hasTok || !hasVocab {
				t.Fatalf("%s 文件清单不完整：config=%v model=%v tokenizer=%v vocab=%v",
					e.ID, hasConfig, hasModel, hasTok, hasVocab)
			}
			required := map[string]bool{}
			for _, r := range e.Required {
				required[r] = true
			}
			if !required["config.json"] || !required["model.bin"] {
				t.Fatalf("%s 的 Required 必须包含 config.json 与 model.bin", e.ID)
			}
		}
		if e.SizeBytes <= 0 {
			t.Fatalf("%s 缺少 SizeBytes", e.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("清单缺少模型 %s", id)
		}
	}
}

// large-v3 使用 vocabulary.json，其余 faster-whisper 模型使用 vocabulary.txt
// —— 与真实仓库一致。transformers 格式的模型没有 CTranslate2 词表文件。
func TestCatalogVocabularyFilesMatchRealRepos(t *testing.T) {
	for _, e := range DefaultCatalog() {
		if e.Engine == EngineTransformers {
			continue
		}
		hasTxt, hasJSON := false, false
		for _, f := range e.Files {
			if f.Path == "vocabulary.txt" {
				hasTxt = true
			}
			if f.Path == "vocabulary.json" {
				hasJSON = true
			}
		}
		if e.ID == "large-v3" {
			if !hasJSON || hasTxt {
				t.Fatalf("large-v3 应使用 vocabulary.json 且不含 vocabulary.txt（实际 txt=%v json=%v）", hasTxt, hasJSON)
			}
			continue
		}
		if !hasTxt || hasJSON {
			t.Fatalf("%s 应使用 vocabulary.txt（实际 txt=%v json=%v）", e.ID, hasTxt, hasJSON)
		}
	}
}

func TestFindCatalogEntryAliases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"small", "small"},
		{"Small", "small"},
		{" small ", "small"},
		{"systran/faster-whisper-small", "small"},
		{"Systran/faster-whisper-small", "small"},
		{"faster-whisper-small", "small"},
		{"systran__faster-whisper-small", "small"},
		{"Systran--faster-whisper-small", "small"},
		{"large-v3", "large-v3"},
		{"faster-whisper-large-v3", "large-v3"},
		{"tiny", "tiny"},
		{"medium", "medium"},
		{"base", "base"},
	}
	for _, c := range cases {
		got, ok := FindCatalogEntry(c.in)
		if !ok {
			t.Fatalf("别名 %q 未解析", c.in)
		}
		if got.ID != c.want {
			t.Fatalf("别名 %q 解析为 %q，期望 %q", c.in, got.ID, c.want)
		}
	}
	if _, ok := FindCatalogEntry("no-such-model"); ok {
		t.Fatalf("未知模型不应解析成功")
	}
	if _, ok := FindCatalogEntry(""); ok {
		t.Fatalf("空 ID 不应解析成功")
	}
}

// Ensure 应支持别名，并使用归一化后的目录名。
func TestEnsureAcceptsAliasAndUsesCanonicalDir(t *testing.T) {
	f := newFixture(t, 1)
	dir, err := f.mgr.Ensure(context.Background(), "Systran/faster-whisper-tiny", nil)
	if err != nil {
		t.Fatalf("别名下载失败：%v", err)
	}
	if dir != filepath.Join(f.root, "tiny") {
		t.Fatalf("应使用归一化目录名，got %q", dir)
	}
	if f.mgr.LocalDir("systran__faster-whisper-tiny") != dir {
		t.Fatalf("LocalDir 别名解析不一致")
	}
}

// —— 11. URL 模板 ——

func TestDownloadURLTemplate(t *testing.T) {
	m := New(Options{RootDir: t.TempDir()})
	entry, ok := findInCatalog(m.Catalog(), "small")
	if !ok {
		t.Fatal("找不到 small")
	}
	mi := Mirror{ID: "x", BaseURL: "https://hf-mirror.com/"}
	got := m.fileURL(mi, entry, ManifestFile{Path: "model.bin"})
	want := "https://hf-mirror.com/Systran/faster-whisper-small/resolve/main/model.bin"
	if got != want {
		t.Fatalf("下载 URL 模板不符：\ngot  %s\nwant %s", got, want)
	}
}

// 默认镜像顺序必须是 hf-mirror 优先，其次是 huggingface 官方。
func TestDefaultMirrorOrder(t *testing.T) {
	m := New(Options{RootDir: t.TempDir()})
	ms := m.Mirrors()
	if len(ms) < 2 {
		t.Fatalf("默认镜像至少两个，实际 %d", len(ms))
	}
	if !strings.Contains(ms[0].BaseURL, "hf-mirror.com") {
		t.Fatalf("首选镜像应为 hf-mirror，实际 %s", ms[0].BaseURL)
	}
	if !strings.Contains(ms[1].BaseURL, "huggingface.co") {
		t.Fatalf("次选镜像应为 huggingface.co，实际 %s", ms[1].BaseURL)
	}
}

// —— 12. 其他工具函数 ——

func TestSizeInTolerance(t *testing.T) {
	cases := []struct {
		actual, expected int64
		want             bool
	}{
		{1000, 1000, true},
		{1005, 1000, true},  // 极小文件有 1024 字节下限
		{2000, 1000, true},  // 差值 1000 <= 1024 下限
		{2100, 1000, false}, // 超出下限
		{1000000, 1000000, true},
		{1005000, 1000000, true},  // 0.5% 内
		{1020000, 1000000, false}, // 2% 超出
		{10, 0, true},             // 未知大小不校验
	}
	for _, c := range cases {
		if got := sizeInTolerance(c.actual, c.expected); got != c.want {
			t.Fatalf("sizeInTolerance(%d, %d) = %v, want %v", c.actual, c.expected, got, c.want)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	if got := parseContentRangeTotal("bytes 0-99/12345"); got != 12345 {
		t.Fatalf("解析总大小失败：%d", got)
	}
	if got := parseContentRangeTotal("bytes 0-99/*"); got != 0 {
		t.Fatalf("未知总大小应返回 0，实际 %d", got)
	}
	if got, ok := parseContentRangeStart("bytes 512-1023/2048"); !ok || got != 512 {
		t.Fatalf("解析起始偏移失败：%d %v", got, ok)
	}
}

func TestProgressThrottle(t *testing.T) {
	now := time.Unix(0, 0)
	th := newProgressThrottle()
	th.now = func() time.Time { return now }

	if !th.allow(0) {
		t.Fatalf("首次回调必须放行")
	}
	// 立即同进度：被节流。
	if th.allow(0.005) {
		t.Fatalf("250ms 内的回调应被节流")
	}
	now = now.Add(300 * time.Millisecond)
	// 时间够但进度变化 <1%：仍被节流。
	if th.allow(0.008) {
		t.Fatalf("进度变化不足 1%% 应被节流")
	}
	now = now.Add(300 * time.Millisecond)
	if !th.allow(0.05) {
		t.Fatalf("时间与进度都满足时应放行")
	}
	// 到达 100% 应立即放行。
	now = now.Add(10 * time.Millisecond)
	if !th.allow(1) {
		t.Fatalf("到达 100%% 应立即放行")
	}
}

// Progress(modelID) 在下载中应能读到非零百分比，结束后归零。
func TestProgressValueDuringDownload(t *testing.T) {
	f := newFixture(t, 1)
	fm := f.mirrors[0]
	release := make(chan struct{})
	fm.mu.Lock()
	fm.holdOn = fileURLPath("tiny", "model.bin")
	fm.release = release
	fm.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.mgr.Ensure(context.Background(), "tiny", nil)
	}()

	waitFor(t, 5*time.Second, func() bool {
		return f.mgr.IsDownloading("tiny") && f.mgr.Progress("tiny") >= 0
	})
	if msg := f.mgr.ProgressMessage("tiny"); msg == "" {
		t.Fatalf("下载中 ProgressMessage 不应为空")
	}
	close(release)
	<-done
	if f.mgr.IsDownloading("tiny") {
		t.Fatalf("下载结束后 IsDownloading 应为 false")
	}
	if got := f.mgr.Progress("tiny"); got != 0 {
		t.Fatalf("下载结束后 Progress 应归零，实际 %d", got)
	}
}

// 未知模型 ID 应返回中文错误。
func TestEnsureUnknownModel(t *testing.T) {
	f := newFixture(t, 1)
	_, err := f.mgr.Ensure(context.Background(), "no-such-model", nil)
	if err == nil {
		t.Fatalf("未知模型应返回错误")
	}
	if !strings.Contains(err.Error(), "未知的模型") {
		t.Fatalf("错误信息应为中文提示，实际：%v", err)
	}
}

// SHA256File 校验。
func TestSHA256File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	// "abc" 的 SHA-256。
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SHA256File(p)
	if err != nil {
		t.Fatalf("SHA256File 失败：%v", err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("sha256 不符：got %s want %s", got, want)
	}
	if _, err := SHA256File(filepath.Join(dir, "missing")); err == nil {
		t.Fatalf("文件不存在应返回错误")
	}
}

// 清单给了 SHA256 时应校验，不匹配则失败。
func TestEnsureVerifiesSHA256WhenProvided(t *testing.T) {
	f := newFixture(t, 1)
	f.catalog[0].Files[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	mgr := New(Options{
		RootDir:    f.root,
		Mirrors:    f.mgr.Mirrors(),
		Catalog:    f.catalog,
		MaxRetries: 1,
	})
	_, err := mgr.Ensure(context.Background(), "tiny", nil)
	if err == nil {
		t.Fatalf("SHA256 不匹配时应失败")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("错误信息应提到 SHA-256，实际：%v", err)
	}
}

// —— 辅助 ——

// waitFor 在超时内轮询条件，失败则终止测试。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%v）", d)
}
