//go:build windows

package web

import "os"

// openNoFollow on Windows falls back to a plain read-only open — Windows lacks
// a portable O_NOFOLLOW. The Lstat-then-IsRegular check in
// readCloudflaredCertFile already rejects reparse points / symlinks before we
// reach here.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
