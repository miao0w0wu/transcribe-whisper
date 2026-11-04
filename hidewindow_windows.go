//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow 让子进程在无控制台窗口的情况下运行。
//
// 桌面应用本身没有控制台，若子进程不设置 CREATE_NO_WINDOW，
// Windows 会为它临时创建一个命令行窗口并一闪而过。所有从界面
// 路径上同步触发的子进程（nvidia-smi、explorer、ffmpeg 探测等）
// 都必须经过这里。
func hideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
