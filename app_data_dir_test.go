package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件锁定「配置目录被误删」这个 bug 的三层防御：
//  1. 构造分离：生产构造 newApp 不建临时目录、不标记 tempDataDir；
//  2. startup 拿到真实数据目录后显式复位 tempDataDir；
//  3. shutdown 删除前校验路径确实在系统临时目录之下。
//
// 回归保护：测试用的 NewApp 仍必须在 shutdown 时清掉自己的临时目录。

// makeNonTempDir 构造一个不在系统临时目录下的可写目录。
//
// 测试要验证「非临时目录不删除」，而 t.TempDir() 恰好位于系统临时目录内，
// 会被判定为可清理，所以改在用户缓存/配置目录下建一个临时子目录，
// 用完由 Cleanup 删掉。两者都不可写时跳过该用例。
func makeNonTempDir(t *testing.T) string {
	t.Helper()
	bases := make([]string, 0, 2)
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		bases = append(bases, dir)
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		bases = append(bases, dir)
	}
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "tw-real-data-*")
		if err != nil {
			continue
		}
		if shouldRemoveTempDataDir(dir) {
			// 该基准目录本身就在系统临时目录里，换下一个候选。
			_ = os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	t.Skip("当前环境无法创建系统临时目录之外的目录，跳过该用例")
	return ""
}

// TestNewAppIsProductionConstructor 验证生产构造不会把自己标记成临时目录。
func TestNewAppIsProductionConstructor(t *testing.T) {
	app := newApp()
	if app == nil {
		t.Fatal("newApp 不应返回 nil")
	}
	if app.tempDataDir {
		t.Fatal("生产构造 newApp 不应设置 tempDataDir")
	}
	if app.dataDir != "" {
		t.Fatalf("生产构造 newApp 不应预设数据目录，实际 %q", app.dataDir)
	}
	if app.store != nil || app.queue != nil {
		t.Fatal("生产构造 newApp 不应打开数据库或任务队列")
	}
}

// TestNewAppTestConstructorKeepsTempSemantics 验证测试构造的语义没被改动。
func TestNewAppTestConstructorKeepsTempSemantics(t *testing.T) {
	app := NewApp()
	dir := app.dataDir
	if !app.tempDataDir {
		t.Fatal("测试构造 NewApp 应把 tempDataDir 置为 true")
	}
	if dir == "" {
		t.Fatal("测试构造 NewApp 应创建临时数据目录")
	}
	if !shouldRemoveTempDataDir(dir) {
		t.Fatalf("测试构造的数据目录应位于系统临时目录下，实际 %q", dir)
	}
	t.Cleanup(func() { app.shutdown(context.Background()) })
}

// TestShouldRemoveTempDataDir 直接单测删除前的路径判定。
func TestShouldRemoveTempDataDir(t *testing.T) {
	tempRoot := os.TempDir()
	inside, err := os.MkdirTemp(tempRoot, "tw-temp-inside-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(inside) })

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"系统临时目录下的子目录", inside, true},
		{"末尾带分隔符的临时目录", inside + string(os.PathSeparator), true},
		{"系统临时目录本身", tempRoot, false},
		{"空路径", "", false},
		{"仅空白字符", "   ", false},
		{"前缀相似的兄弟目录", filepath.Join(filepath.Dir(tempRoot), "tw-not-temp-"+filepath.Base(inside)), false},
	}
	for _, tc := range cases {
		if got := shouldRemoveTempDataDir(tc.dir); got != tc.want {
			t.Errorf("%s: shouldRemoveTempDataDir(%q) = %v，期望 %v", tc.name, tc.dir, got, tc.want)
		}
	}

	// Windows 下路径大小写不敏感，大写形式也必须判为可清理。
	if runtime.GOOS == "windows" {
		if got := shouldRemoveTempDataDir(strings.ToUpper(inside)); !got {
			t.Errorf("大写路径 %q 应判为可清理，实际 %v", strings.ToUpper(inside), got)
		}
	}

	// 非系统临时目录一律不删。
	realDir := makeNonTempDir(t)
	if shouldRemoveTempDataDir(realDir) {
		t.Errorf("非临时目录 %q 不应判为可清理", realDir)
	}
}

// TestShutdownKeepsRealDataDir 复现原始 bug 的场景：
// startup 之后 tempDataDir 已复位为 false，shutdown 必须保留真实数据目录。
func TestShutdownKeepsRealDataDir(t *testing.T) {
	realDir := makeNonTempDir(t)
	dbPath := filepath.Join(realDir, "transcribe-whisper.db")
	if err := os.WriteFile(dbPath, []byte("task data"), 0o644); err != nil {
		t.Fatalf("写入数据库占位文件失败: %v", err)
	}

	app := newApp()
	app.dataDir = realDir
	app.tempDataDir = false
	app.shutdown(context.Background())

	if _, err := os.Stat(realDir); err != nil {
		t.Fatalf("关闭软件后真实数据目录不应被删除: %v", err)
	}
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("关闭软件后数据库文件不应被删除: %v", err)
	}
	if string(data) != "task data" {
		t.Fatalf("数据库文件内容被破坏: %q", string(data))
	}
}

// TestShutdownKeepsRealDataDirWhenFlagStuck 验证第三层防御：
// 即使临时标记因故没被复位，只要路径不在系统临时目录下就不删。
func TestShutdownKeepsRealDataDirWhenFlagStuck(t *testing.T) {
	realDir := makeNonTempDir(t)

	app := newApp()
	app.dataDir = realDir
	app.tempDataDir = true // 故意留下脏标记，模拟复位失效
	app.shutdown(context.Background())

	if _, err := os.Stat(realDir); err != nil {
		t.Fatalf("脏标记下也不应删除非临时目录: %v", err)
	}
}

// TestShutdownRemovesTestTempDataDir 回归保护：
// 真正的测试临时目录仍然要被清理，否则每个测试都会留下垃圾。
func TestShutdownRemovesTestTempDataDir(t *testing.T) {
	app := NewApp()
	dir := app.dataDir
	if dir == "" {
		t.Fatal("测试构造应创建临时数据目录")
	}
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写入标记文件失败: %v", err)
	}

	app.shutdown(context.Background())

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("测试临时目录 %q 应被清理，实际 err = %v", dir, err)
	}
}

// TestStartupResetsTempDataDirFlag 验证第二层防御：
// 即使误用了测试构造 NewApp，startup 拿到真实数据目录后也必须复位标记。
func TestStartupResetsTempDataDirFlag(t *testing.T) {
	// startup 会真的创建并写入数据目录，所以只在它落在系统临时目录里
	// （go test 的二进制通常就在那里）时才跑，避免污染用户的真实配置。
	resolved, err := resolveDataDir()
	if err != nil {
		t.Skipf("当前环境无法解析数据目录，跳过该用例: %v", err)
	}
	if !shouldRemoveTempDataDir(resolved) {
		t.Skipf("startup 会写入系统临时目录之外的真实数据目录 %q，跳过以避免污染", resolved)
	}

	app := NewApp()
	// 记下测试构造的临时目录：startup 之后它不再被引用，自己清理。
	oldTemp := app.dataDir
	t.Cleanup(func() {
		app.shutdown(context.Background())
		_ = os.RemoveAll(oldTemp)
		// startup 得到的数据目录此时 tempDataDir 已复位，shutdown 不会清理，
		// 由测试兜底删除，避免在系统临时目录里留垃圾。
		_ = os.RemoveAll(resolved)
	})

	app.startup(context.Background())
	if app.tempDataDir {
		t.Fatal("startup 之后 tempDataDir 必须复位为 false")
	}
	if app.dataDir == "" {
		t.Fatal("startup 之后应有真实数据目录")
	}
	if app.dataDir == oldTemp {
		t.Fatalf("startup 应把数据目录改写为真实目录，实际仍是临时目录 %q", oldTemp)
	}
}
