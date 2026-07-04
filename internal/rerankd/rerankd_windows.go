//go:build windows

package rerankd

import (
	"os"
	"syscall"
)

func sidecarSysProcAttr() *syscall.SysProcAttr {
	// CREATE_NEW_PROCESS_GROUP so Ctrl+C aimed at the node console doesn't
	// propagate to the sidecar.
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killSidecar(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
