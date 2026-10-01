package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/home"
	"browser-test-agent/internal/osdeps"
	"browser-test-agent/internal/steps"
	"browser-test-agent/pkg/spec"
)

// Doctor is the health report the worker polls every few minutes. A location
// whose report is unhealthy is skipped by the dispatcher rather than blamed
// for failing tests.
type Doctor struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	RunnerVersion string `json:"runner_version"`
	Playwright    string `json:"playwright_version"`
	Home          string `json:"home"`
	// Distro is the VM's os-release ID and version, which decides whether
	// install-deps can install the browsers' OS packages here.
	Distro string `json:"distro,omitempty"`
	// MissingDeps are the OS packages the failed launches below need.
	MissingDeps   []string          `json:"missing_deps,omitempty"`
	DiskFreeBytes uint64            `json:"disk_free_bytes"`
	DriverOK      bool              `json:"driver_ok"`
	Browsers      map[string]string `json:"browsers"` // name → version, or "error: …"
	StepTypes     []string          `json:"step_types"`
	RunFolders    int               `json:"run_folders"`
	Healthy       bool              `json:"healthy"`
	Problems      []string          `json:"problems,omitempty"`
}

// minDiskFree is the least free space at which the location is still
// considered healthy: a run with screenshots is tens of MB, and a full disk
// fails runs in confusing ways.
const minDiskFree = 1 << 30

func cmdDoctor(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	skipLaunch := fs.Bool("no-launch", false, "report versions without launching each browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	d := Doctor{
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		RunnerVersion: Version,
		Playwright:    playwrightVersion(),
		Home:          home.Dir(),
		Distro:        osdeps.Detect().String(),
		Browsers:      map[string]string{},
		StepTypes:     steps.Implemented(),
		Healthy:       true,
	}
	d.Hostname, _ = os.Hostname()

	if err := os.MkdirAll(home.RunsDir(), 0o750); err != nil {
		d.Problems = append(d.Problems, fmt.Sprintf("cannot create %s: %v", home.RunsDir(), err))
	} else if entries, err := os.ReadDir(home.RunsDir()); err == nil {
		d.RunFolders = len(entries)
	}
	if free, err := home.DiskFree(home.Dir()); err == nil {
		d.DiskFreeBytes = free
		if free < minDiskFree {
			d.Problems = append(d.Problems, fmt.Sprintf("only %d MB free under %s", free>>20, home.Dir()))
		}
	}

	if err := home.ExportBrowsersPath(); err != nil {
		d.Problems = append(d.Problems, err.Error())
	}
	pw, err := playwright.Run(&playwright.RunOptions{DriverDirectory: home.DriverDir(), SkipInstallBrowsers: true, Verbose: false})
	if err != nil {
		d.Problems = append(d.Problems, "playwright driver not installed: "+err.Error()+" (run install-browsers)")
		d.Healthy = false
		return printJSON(d)
	}
	defer pw.Stop()
	d.DriverOK = true

	missingLibs := false
	for _, name := range spec.Browsers {
		if *skipLaunch {
			d.Browsers[name] = "not checked"
			continue
		}
		version, err := launchCheck(ctx, pw, name)
		if err != nil {
			// The raw Playwright error stays on the browser entry; Problems
			// gets the line an operator can act on.
			d.Browsers[name] = "error: " + err.Error()
			// Edge is optional: it needs Microsoft Edge installed on the VM.
			// Chromium and Firefox are ours to install, so their absence is a
			// problem.
			if name == spec.BrowserEdge {
				continue
			}
			if soname, ok := osdeps.MissingLibsHint(err.Error()); ok {
				d.Problems = append(d.Problems, name+": cannot start — this VM is missing the browsers' OS libraries"+sonameSuffix(soname))
				missingLibs = true
				continue
			}
			d.Problems = append(d.Problems, name+": "+err.Error())
			continue
		}
		d.Browsers[name] = version
	}
	// One remediation for all of them: both browsers fail for the same reason
	// and the apt command is long enough to print once.
	if missingLibs {
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		missing, _ := osdeps.Probe(pctx, home.DriverDir(), []string{"chromium", "firefox"})
		cancel()
		d.MissingDeps = missing
		d.Problems = append(d.Problems, osdeps.LaunchRemediation(osdeps.Detect(), missing, runnerExe()))
	}

	if len(d.Problems) > 0 {
		d.Healthy = false
	}
	return printJSON(d)
}

// sonameSuffix names the library the loader could not find, when it named one.
func sonameSuffix(soname string) string {
	if soname == "" {
		return ""
	}
	return " (" + soname + ")"
}

// runnerExe is this binary's path on the VM, so the remediation is a command
// the operator can paste rather than one they have to locate first.
func runnerExe() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return filepath.Join(home.Dir(), "browser-test-agent")
}

// launchCheck starts and stops a browser, which is the only proof that the
// build on disk actually runs on this VM (missing shared libraries fail here
// and nowhere earlier).
func launchCheck(ctx context.Context, pw *playwright.Playwright, name string) (string, error) {
	opts := playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(60_000)}
	var bt playwright.BrowserType
	switch name {
	case spec.BrowserChrome:
		bt = pw.Chromium
	case spec.BrowserEdge:
		bt = pw.Chromium
		opts.Channel = playwright.String("msedge")
	case spec.BrowserFirefox:
		bt = pw.Firefox
	default:
		return "", fmt.Errorf("unknown browser %q", name)
	}
	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		b, err := bt.Launch(opts)
		if err != nil {
			done <- res{err: err}
			return
		}
		v := b.Version()
		b.Close()
		done <- res{v: v}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(90 * time.Second):
		return "", errors.New("launch timed out")
	}
}

// cmdInstallBrowsers installs the Playwright driver and browser builds under
// the home directory. With --from, browsers come from a bundle the worker
// uploaded (a tar.gz of a browsers directory) instead of the internet, which
// is how a VM with no outbound access is provisioned.
//
// The driver is a Node.js release plus the playwright-core npm package
// (playwright-go assembles it; the old driver-zip CDN was shut down). It comes
// from nodejs.org and registry.npmjs.org unless NODE_MIRROR /
// PLAYWRIGHT_GO_NPM_REGISTRY point elsewhere, or PLAYWRIGHT_NODEJS_PATH names
// a Node already on the VM.
func cmdInstallBrowsers(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("install-browsers", flag.ContinueOnError)
	from := fs.String("from", "", "tar.gz bundle of pre-downloaded browsers to extract instead of downloading")
	browsersFlag := fs.String("browsers", "chromium,firefox", "comma-separated Playwright browsers to install")
	withDeps := fs.Bool("with-deps", false, "also install the browsers' OS packages (needs root or passwordless sudo)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := home.ExportBrowsersPath(); err != nil {
		return printJSONErr(err)
	}
	if err := os.MkdirAll(home.DriverDir(), 0o755); err != nil {
		return printJSONErr(fmt.Errorf("create %s: %w", home.DriverDir(), err))
	}

	installed := map[string]any{"home": home.Dir(), "driver": home.DriverDir(), "browsers_dir": home.BrowsersDir()}

	if *from != "" {
		n, err := extractTarGz(*from, home.BrowsersDir())
		if err != nil {
			return printJSONErr(fmt.Errorf("extract bundle %s: %w", *from, err))
		}
		installed["bundle_files"] = n
		// The driver itself is small and versioned with this binary; it is
		// still fetched, but browsers are not.
		if err := playwright.Install(&playwright.RunOptions{DriverDirectory: home.DriverDir(), SkipInstallBrowsers: true, Verbose: false, Stdout: os.Stderr, Stderr: os.Stderr}); err != nil {
			return printJSONErr(fmt.Errorf("install driver: %w", err))
		}
		installed["browsers"] = "from bundle"
		return printJSON(installed)
	}

	browsers := splitList(*browsersFlag)
	if err := playwright.Install(&playwright.RunOptions{
		DriverDirectory: home.DriverDir(),
		Browsers:        browsers,
		WithDeps:        *withDeps,
		Verbose:         false,
		Stdout:          os.Stderr,
		Stderr:          os.Stderr,
	}); err != nil {
		return printJSONErr(fmt.Errorf("install: %w", err))
	}
	installed["browsers"] = browsers
	installed["with_deps"] = *withDeps
	_ = ctx
	return printJSON(installed)
}

// extractTarGz unpacks a bundle into dir, refusing entries that would land
// outside it.
func extractTarGz(path, dir string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		target := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") {
			return count, fmt.Errorf("bundle entry %q escapes %s", hdr.Name, dir)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return count, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return count, err
			}
			out.Close()
			count++
		case tar.TypeSymlink:
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return count, err
			}
		}
	}
}

// playwrightVersion is the pinned playwright-go module version, which pins the
// driver and browser builds install-browsers fetches.
func playwrightVersion() string {
	return playwrightModuleVersion
}
