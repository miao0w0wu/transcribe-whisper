package main

import (
	"log"
	"os"
	"path/filepath"
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
