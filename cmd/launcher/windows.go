//go:build windows
// +build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow sets up the process to run without a visible console window
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
