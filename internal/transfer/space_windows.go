package transfer

import "golang.org/x/sys/windows"

func freeBytes(path string) (uint64, bool) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return 0, false
	}
	var available, total, free uint64
	if windows.GetDiskFreeSpaceEx(p, &available, &total, &free) != nil {
		return 0, false
	}
	return available, true
}
