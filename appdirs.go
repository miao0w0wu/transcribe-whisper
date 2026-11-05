package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// dataDirName 是应用在自身所在目录下创建的数据目录名。
//
// 之所以把数据放在应用旁边而不是 %APPDATA%，是为了做成「绿色版」：
// 整个目录拷走就能带走任务库、模型与 FFmpeg，换机器不用重新下载几个 GB。
const dataDirName = "transcribe-whisper-data"

// executableDir 返回当前可执行文件所在目录。
//
// 拿不到时返回空串，调用方据此回退到用户配置目录。
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	// 解析符号链接，避免 wails dev 等场景下拿到临时目录的软链接路径。
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// portableDataDir 返回「应用目录下的数据目录」路径，并确保它可写。
//
// 可写性必须实测：应用可能被装到 Program Files 这类普通用户无写权限的
// 位置。此时不能假装成功，要返回错误让调用方回退并提示用户。
func portableDataDir() (string, error) {
	exeDir := executableDir()
	if exeDir == "" {
		return "", os.ErrNotExist
	}
	dir := filepath.Join(exeDir, dataDirName)
	if err := ensureWritableDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureWritableDir 创建目录并实际写一个探针文件确认可写。
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return err
	}
	_ = os.Remove(probe)
	return nil
}

// fallbackDataDir 返回用户配置目录下的数据目录（便携目录不可写时使用）。
func fallbackDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "transcribe-whisper")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// resolveDataDir 决定数据目录：优先应用所在目录，不可用时回退用户配置目录。
//
// 回退不是静默的：会写日志，界面也能通过 GetEnvironment 看到实际路径，
// 避免用户以为模型下到了 A 处结果在 B 处。
func resolveDataDir() (string, error) {
	if dir, err := portableDataDir(); err == nil {
		return dir, nil
	} else {
		log.Printf("应用所在目录不可写（%v），改为把数据放在用户配置目录", err)
	}
	return fallbackDataDir()
}

// defaultFFmpegDir 返回 FFmpeg 的默认安装目录：数据目录下的 ffmpeg/。
//
// 放在数据目录内而不是 exe 同级，是为了让「绿色版」的所有产物都聚在
// 同一个文件夹里，卸载时删掉即可。
func defaultFFmpegDir(dataDir string) string {
	base := strings.TrimSpace(dataDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, "ffmpeg")
}

// defaultModelDir 返回模型默认目录：数据目录下的 models/。
func defaultModelDir(dataDir string) string {
	base := strings.TrimSpace(dataDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, "models")
}

// normalizeForCompare 把路径归一成可直接比较前缀的形式。
//
// 做三件事：清理分隔符与末尾斜杠（filepath.Clean）、转绝对路径、
// Windows 下统一小写（盘符与路径大小写不敏感）。无法归一（空串、
// 取绝对路径失败）时返回空串，调用方据此判定为「不安全」。
func normalizeForCompare(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	// 解析符号链接：临时目录可能经软链接访问（如 macOS 的 /var），
	// 两边都解析才能正确比较。解析失败就沿用原路径。
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

// shouldRemoveTempDataDir 判断数据目录是否确实位于系统临时目录之下。
//
// 这是 shutdown 删除前的最后一道防线：只有确认目标落在 os.TempDir()
// 之内才允许删除。真实数据目录（应用旁的绿色版目录、用户配置目录）
// 无论内部标记如何，都不满足该条件，因此永远不会被删除。
// 注意「等于临时目录本身」也判为不安全——那会删掉整个系统临时目录。
func shouldRemoveTempDataDir(dir string) bool {
	target := normalizeForCompare(dir)
	if target == "" {
		return false
	}
	tempRoot := normalizeForCompare(os.TempDir())
	if tempRoot == "" {
		return false
	}
	// 加分隔符再比较前缀：既避免 /tmp-evil 被 /tmp 误判为子目录，
	// 也天然排除「目标就是临时目录本身」——那会删掉整个系统临时目录。
	return strings.HasPrefix(target, tempRoot+string(os.PathSeparator))
}
