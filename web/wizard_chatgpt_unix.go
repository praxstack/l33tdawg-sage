//go:build !windows

package web

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only with O_NOFOLLOW so a symlink swapped in
// between Lstat and Open returns ELOOP instead of redirecting the read.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
