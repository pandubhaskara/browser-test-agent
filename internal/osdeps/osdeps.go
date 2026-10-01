// Package osdeps is the browsers' OS-level dependencies: which distribution
// the VM runs, installing the packages through Playwright's own install-deps,
// and turning a "cannot open shared object file" launch failure into the
// command an operator should run as root.
//
// install-browsers downloads browser builds but cannot install packages, so a
// freshly bootstrapped VM has Chromium on disk and no libatk to run it with.
// That is what this package fixes, and — when it cannot, because the SSH user
// has no sudo or the distribution is not one Playwright knows — what it
// explains.
package osdeps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

// Reasons a Report is not OK. They are the machine-readable half of the
// verdict: the backend logs them, the operator reads Remediation.
const (
	ReasonNotLinux          = "not_linux"
	ReasonUnsupportedDistro = "unsupported_distro"
	ReasonNoRoot            = "no_root"
	ReasonDriverMissing     = "driver_missing"
	ReasonAptLocked         = "apt_locked"
	ReasonInstallFailed     = "install_failed"
	ReasonStillMissing      = "still_missing"
)

// Distro is the subset of /etc/os-release that decides whether Playwright can
// install packages on this VM.
type Distro struct {
	ID        string `json:"id"`         // "ubuntu", "debian", "rocky"
	VersionID string `json:"version_id"` // "24.04"
	IDLike    string `json:"id_like"`    // "debian"
	Pretty    string `json:"pretty,omitempty"`
}

// String is the short form shown on a location: "ubuntu 24.04", or "" when
// os-release told us nothing.
func (d Distro) String() string {
	switch {
	case d.ID == "":
		return d.Pretty
	case d.VersionID == "":
		return d.ID
	default:
		return d.ID + " " + d.VersionID
	}
}

// debianIDs are the distributions Playwright has an apt package list for.
var debianIDs = map[string]bool{
	"debian": true, "ubuntu": true, "linuxmint": true, "pop": true, "raspbian": true,
}

// DebianFamily reports whether `install-deps` can install packages here.
// ID_LIKE is consulted too: a derivative names its base there and uses the
// same package names.
func (d Distro) DebianFamily() bool {
	if debianIDs[d.ID] {
		return true
	}
	for _, like := range strings.Fields(d.IDLike) {
		if debianIDs[like] {
			return true
		}
	}
	return false
}

// osReleasePath is a var so the tests can read a fixture rather than the
// machine they happen to run on.
var osReleasePath = "/etc/os-release"

// Detect reads /etc/os-release. A missing file is not an error: it means the
// same thing as an unknown distribution, which the caller already handles.
func Detect() Distro {
	if runtime.GOOS != "linux" {
		return Distro{}
	}
	f, err := os.Open(osReleasePath)
	if err != nil {
		return Distro{}
	}
	defer f.Close()
	return ParseOSRelease(f)
}

// ParseOSRelease reads the KEY=VALUE lines of an os-release file. Values are
// optionally quoted; anything that is not one of the four keys is ignored.
func ParseOSRelease(r io.Reader) Distro {
	var d Distro
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = unquote(strings.TrimSpace(value))
		switch strings.TrimSpace(key) {
		case "ID":
			d.ID = strings.ToLower(value)
		case "VERSION_ID":
			d.VersionID = value
		case "ID_LIKE":
			d.IDLike = strings.ToLower(value)
		case "PRETTY_NAME":
			d.Pretty = value
		}
	}
	return d
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// Report is install-deps' verdict, printed as the command's only line of
// stdout and parsed by the backend worker. Every field name here is part of
// that contract.
type Report struct {
	OK          bool     `json:"ok"`
	Reason      string   `json:"reason,omitempty"`
	OS          string   `json:"os"`
	Distro      string   `json:"distro,omitempty"`
	Browsers    []string `json:"browsers,omitempty"`
	Installed   bool     `json:"installed"` // install-deps ran and exited 0
	Verified    bool     `json:"verified"`  // a probe afterwards found nothing missing
	Missing     []string `json:"missing,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// safePackage is a Debian package name. The names come from parsing another
// program's stdout and end up in a line an operator pastes into a root shell,
// so anything else is dropped rather than quoted and hoped for.
var safePackage = regexp.MustCompile(`^[a-z0-9][a-z0-9+._:-]*$`)

var missingHeader = regexp.MustCompile(`(?i)missing system dependencies`)

// ParseMissing reads the package list out of `install-deps --dry-run`:
//
//	Missing system dependencies (2):
//	    libatk1.0-0t64
//	    libnss3
//
// "All system dependencies are installed." yields nothing, and so does any
// other output: a package name is a single token, so the first line of prose
// ends the list.
func ParseMissing(out string) []string {
	var pkgs []string
	collecting := false
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if !collecting {
			collecting = missingHeader.MatchString(line)
			continue
		}
		name := strings.TrimSpace(line)
		if name == "" || strings.ContainsAny(name, " \t") || !safePackage.MatchString(name) {
			break
		}
		pkgs = append(pkgs, name)
	}
	return dedupe(pkgs)
}

// AptInstallCommand is the single copy-pasteable line that fixes a VM the
// runner could not fix itself.
func AptInstallCommand(pkgs []string) string {
	safe := dedupe(pkgs)
	if len(safe) == 0 {
		return ""
	}
	return "sudo sh -c 'apt-get update && apt-get install -y --no-install-recommends " + strings.Join(safe, " ") + "'"
}

// dedupe keeps the first occurrence of every package name that is one, in
// order, so the command reads the way the driver listed it.
func dedupe(pkgs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || !safePackage.MatchString(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// missingLibsMarkers are how a launch failure for missing OS libraries reads.
// The first two come from the dynamic loader, the rest from Playwright's own
// host validation.
var missingLibsMarkers = []string{
	"error while loading shared libraries",
	"cannot open shared object file",
	"host system is missing dependencies",
	"missing dependencies to run browsers",
	"install-deps",
}

var sonameRe = regexp.MustCompile(`lib[A-Za-z0-9._+-]*\.so(?:\.[0-9]+)*`)

// MissingLibsHint classifies a browser launch failure as "this VM lacks the
// browsers' OS libraries" and names the first library when the loader did.
// Every other failure returns false and must keep its raw message: a timeout
// or a crashed page is not a packaging problem.
func MissingLibsHint(msg string) (string, bool) {
	low := strings.ToLower(msg)
	for _, m := range missingLibsMarkers {
		if strings.Contains(low, m) {
			return sonameRe.FindString(msg), true
		}
	}
	return "", false
}

// aptLockMarkers are another package manager — unattended-upgrades, usually —
// holding dpkg's lock. That is a retry, not a misconfiguration.
var aptLockMarkers = []string{
	"could not get lock",
	"dpkg frontend lock",
	"is another process using it",
}

// AptLocked reports whether output shows apt losing a race for the dpkg lock.
func AptLocked(out string) bool {
	low := strings.ToLower(out)
	for _, m := range aptLockMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// FallbackLibraries are the library families Chromium and Firefox need, named
// by soname rather than by package. On a distribution Playwright has no
// package list for the package names differ, and a wrong apt line is worse
// than none.
var FallbackLibraries = []string{
	"libatk-1.0", "libatk-bridge-2.0", "libatspi-2.0", "libcups", "libdrm", "libgbm",
	"libnss3", "libnspr4", "libpango-1.0", "libcairo", "libasound2", "libxkbcommon",
	"libxcomposite", "libxdamage", "libxfixes", "libxrandr", "libdbus-glib-1",
	"libxslt", "libgtk-3",
}

// ManualRemediation is what to tell an operator whose distribution Playwright
// cannot install packages for.
func ManualRemediation(d Distro) string {
	name := d.String()
	if name == "" {
		name = "this distribution"
	}
	return "Playwright has no OS package list for " + name +
		": install the Chromium and Firefox runtime libraries from your distribution (" +
		strings.Join(FallbackLibraries, ", ") + "), then check the location again"
}

// LaunchRemediation is the fix for a VM whose browsers will not start: run
// install-deps as root, or the apt line it would have run.
func LaunchRemediation(d Distro, missing []string, runner string) string {
	if !d.DebianFamily() {
		return ManualRemediation(d)
	}
	if runner == "" {
		runner = "browser-test-agent"
	}
	msg := "install the browsers' OS libraries: run `sudo " + runner + " install-deps` on this VM"
	if cmd := AptInstallCommand(missing); cmd != "" {
		msg += ", or run: " + cmd
	}
	return msg
}

// remediationFor is the command to show when the automatic install did not
// happen. With package names it is the exact apt line; without them (the
// probe could not run) the library list is still enough to work from.
func remediationFor(d Distro, missing []string) string {
	if cmd := AptInstallCommand(missing); cmd != "" {
		return cmd
	}
	return ManualRemediation(d)
}

// Install installs the browsers' OS packages with the Playwright driver's own
// install-deps, which on Debian and Ubuntu is `apt-get update` followed by
// `apt-get install`. It never returns an error: the browsers are already on
// disk by the time it runs, so every outcome is a Report the location can
// show rather than a bootstrap to fail.
func Install(ctx context.Context, driverDir string, browsers []string) Report {
	rep := Report{OS: runtime.GOOS, Browsers: browsers}

	// Windows and macOS ship these libraries with the OS.
	if runtime.GOOS != "linux" {
		rep.OK = true
		rep.Reason = ReasonNotLinux
		return rep
	}

	d := Detect()
	rep.Distro = d.String()
	// Playwright only logs "Cannot install dependencies for …" and exits 0 on
	// a distribution it has no list for, so refuse here instead of reporting a
	// success that installed nothing.
	if !d.DebianFamily() {
		rep.Reason = ReasonUnsupportedDistro
		rep.Remediation = ManualRemediation(d)
		return rep
	}

	// Probe before touching the system: `apt-get install -s` needs no network
	// and no root, takes about a second, and re-bootstrapping a healthy VM
	// stops here rather than running apt-get update again.
	missing, probeErr := Probe(ctx, driverDir, browsers)
	if probeErr == nil && len(missing) == 0 {
		rep.OK = true
		rep.Verified = true
		return rep
	}
	rep.Missing = missing
	if probeErr != nil && isDriverMissing(probeErr) {
		rep.Reason = ReasonDriverMissing
		rep.Error = probeErr.Error() + "; run install-browsers first"
		rep.Remediation = ManualRemediation(d)
		return rep
	}

	// Playwright shells out to plain `sudo --`, with no -n: on a TTY-less SSH
	// exec that dies with "no tty present and no askpass program specified",
	// which reads like a bug rather than a missing sudoers rule. Check first
	// and say what is actually wrong. A passing `sudo -n true` means NOPASSWD,
	// so Playwright's own sudo works too.
	if !canInstallPackages(ctx) {
		rep.Reason = ReasonNoRoot
		rep.Remediation = remediationFor(d, missing)
		return rep
	}

	out, runErr := runDriver(ctx, driverDir, append([]string{"install-deps"}, browsers...)...)
	if runErr != nil {
		rep.Reason = ReasonInstallFailed
		if AptLocked(out) {
			rep.Reason = ReasonAptLocked
		}
		if isDriverMissing(runErr) {
			rep.Reason = ReasonDriverMissing
		}
		rep.Error = strings.TrimSpace(runErr.Error() + " " + tail(out, 1000))
		rep.Remediation = remediationFor(d, missing)
		return rep
	}
	rep.Installed = true

	// A zero exit is not proof: re-probe and believe the package manager.
	still, probeErr := Probe(ctx, driverDir, browsers)
	if probeErr == nil && len(still) == 0 {
		rep.OK = true
		rep.Verified = true
		rep.Missing = nil
		return rep
	}
	if len(still) > 0 {
		rep.Reason = ReasonStillMissing
		rep.Missing = still
		rep.Remediation = remediationFor(d, still)
		return rep
	}
	// install-deps succeeded and only the verification probe failed: report
	// the install rather than a problem we cannot name.
	rep.OK = true
	rep.Error = tail(probeErr.Error(), 300)
	return rep
}

// Probe asks the driver which OS packages are missing. It is `apt-get install
// -s` underneath, and exits non-zero when something is missing — a verdict,
// not a failure, so the exit status is ignored once a list has parsed.
func Probe(ctx context.Context, driverDir string, browsers []string) ([]string, error) {
	out, runErr := runDriver(ctx, driverDir, append([]string{"install-deps", "--dry-run"}, browsers...)...)
	if missing := ParseMissing(out); len(missing) > 0 {
		return missing, nil
	}
	if strings.Contains(strings.ToLower(out), "all system dependencies are installed") {
		return nil, nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("probe OS packages: %w: %s", runErr, tail(out, 300))
	}
	// No list and no confirmation: an older CLI, or a platform it said nothing
	// about. Treat it as "do not know" so the caller still tries the install.
	return nil, errors.New("probe OS packages: install-deps --dry-run said nothing about packages")
}

// canInstallPackages reports whether this process can install packages: root,
// or a sudo that needs no password.
func canInstallPackages(ctx context.Context) bool {
	if os.Geteuid() == 0 {
		return true
	}
	return exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil
}

// runDriver runs the Playwright driver CLI and returns its combined output.
// The driver's own chatter is mirrored to stderr: install-deps' stdout is
// reserved for the JSON verdict.
func runDriver(ctx context.Context, driverDir string, args ...string) (string, error) {
	drv, err := playwright.NewDriver(&playwright.RunOptions{
		DriverDirectory:     driverDir,
		SkipInstallBrowsers: true,
		Verbose:             false,
	})
	if err != nil {
		return "", err
	}
	cmd := drv.Command(args...)
	buf := &limitWriter{max: 64 << 10}
	cmd.Stdout = io.MultiWriter(buf, os.Stderr)
	cmd.Stderr = cmd.Stdout
	err = runWithContext(ctx, cmd)
	return buf.String(), err
}

// runWithContext runs cmd, killing it if ctx is done first. The driver's
// exec.Cmd is built without a context, so the deadline is applied here rather
// than by duplicating its path resolution. Killing node does not kill an
// apt-get it started; the deadline is generous enough that this is the
// already-wedged case.
func runWithContext(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

// isDriverMissing reports whether the driver's node or cli.js is not there,
// which means install-browsers has not run on this VM.
func isDriverMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound)
}

// limitWriter keeps the first max bytes written to it. Driver output is
// unbounded in principle and only an excerpt is ever shown.
type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func (w *limitWriter) String() string { return w.buf.String() }

// tail keeps the last n characters, where a failure's reason tends to be.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-n:], "")
}
