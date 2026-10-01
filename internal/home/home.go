// Package home is the runner's install directory on the VM and the layout
// under it: the Playwright driver, the browser builds, and one folder per run.
//
//	<home>/
//	├── driver/          Playwright driver (Node + package), from install-browsers
//	├── browsers/        pinned browser builds (PLAYWRIGHT_BROWSERS_PATH)
//	└── runs/<run_id>/   result.ndjson, step-NNN.png, resources.json, payloads/, uploads/
package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome overrides the platform default install directory.
const EnvHome = "BROWSERTEST_HOME"

// Dir returns the install directory: BROWSERTEST_HOME, else the platform
// default (/opt/browsertest, C:\browsertest).
func Dir() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		return v
	}
	return defaultDir()
}

// DriverDir is where the Playwright driver lives.
func DriverDir() string { return filepath.Join(Dir(), "driver") }

// BrowsersDir is where browser builds live; exported to the driver through
// PLAYWRIGHT_BROWSERS_PATH by ExportBrowsersPath.
func BrowsersDir() string { return filepath.Join(Dir(), "browsers") }

// RunsDir holds one folder per run.
func RunsDir() string { return filepath.Join(Dir(), "runs") }

// RunDir is a run's folder. The ID is confined to a single path segment: it
// comes from the worker, but a runner that trusted "../../" would be one
// misconfiguration away from deleting the wrong folder on purge.
func RunDir(runID string) (string, error) {
	if runID == "" || runID != filepath.Base(runID) || runID == "." || runID == ".." {
		return "", fmt.Errorf("invalid run id %q", runID)
	}
	return filepath.Join(RunsDir(), runID), nil
}

// ExportBrowsersPath points the Playwright driver at BrowsersDir. The driver is
// a child process and reads this from its environment, so it is set once here
// before the driver is started.
func ExportBrowsersPath() error {
	if err := os.MkdirAll(BrowsersDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", BrowsersDir(), err)
	}
	return os.Setenv("PLAYWRIGHT_BROWSERS_PATH", BrowsersDir())
}
