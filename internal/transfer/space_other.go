//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !windows

package transfer

func freeBytes(string) (uint64, bool) { return 0, false }
