//go:build !windows

package rerankd

import (
	"syscall"
)

// sidecarSysProcAttr puts the sidecar in its own process group so a signal
// aimed at the SAGE node's group doesn't take the sidecar down accidentally,
// and so killSidecar can terminate the whole group deliberately.
func sidecarSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killSidecar terminates the sidecar's process group (falling back to the
// single pid when it leads no group).
func killSidecar(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}
