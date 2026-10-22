//go:build darwin || linux || freebsd || openbsd || netbsd

package transfer

import "golang.org/x/sys/unix"

func freeBytes(path string) (uint64, bool) {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil {
		return 0, false
	}
	return uint64(s.Bavail) * uint64(s.Bsize), true
}
