//go:build windows

package home

import "golang.org/x/sys/windows"

func defaultDir() string { return `C:\browsertest` }

// DiskFree reports free bytes on the volume holding path.
func DiskFree(path string) (uint64, error) {
	var free, total, avail uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err
	}
	return free, nil
}
