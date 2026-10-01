//go:build linux

package home

import "syscall"

func defaultDir() string { return "/opt/browsertest" }

// DiskFree reports free bytes on the filesystem holding path.
func DiskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
