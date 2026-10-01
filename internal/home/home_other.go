//go:build !linux && !windows

package home

import (
	"os"
	"path/filepath"
)

// A location only ever runs on Linux or Windows; these keep the package
// building on a developer's machine.
func defaultDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "browsertest")
	}
	return filepath.Join(dir, "browsertest")
}

func DiskFree(string) (uint64, error) { return 0, nil }
