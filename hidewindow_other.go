//go:build !windows

package main

import "os/exec"

// hideWindow 在非 Windows 平台是空操作：桌面环境下子进程不会
// 额外弹出终端窗口。
func hideWindow(cmd *exec.Cmd) {}
