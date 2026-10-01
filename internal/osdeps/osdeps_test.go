package osdeps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     Distro
		debian   bool
		stringer string
	}{
		{
			name: "ubuntu 24.04",
			in: `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
`,
			want:     Distro{ID: "ubuntu", VersionID: "24.04", IDLike: "debian", Pretty: "Ubuntu 24.04.1 LTS"},
			debian:   true,
			stringer: "ubuntu 24.04",
		},
		{
			name: "debian 12 has no ID_LIKE",
			in: `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
ID=debian
`,
			want:     Distro{ID: "debian", VersionID: "12", Pretty: "Debian GNU/Linux 12 (bookworm)"},
			debian:   true,
			stringer: "debian 12",
		},
		{
			name: "rocky is not the debian family",
			in: `NAME="Rocky Linux"
VERSION_ID="9.4"
ID="rocky"
ID_LIKE="rhel centos fedora"
`,
			want:     Distro{ID: "rocky", VersionID: "9.4", IDLike: "rhel centos fedora"},
			debian:   false,
			stringer: "rocky 9.4",
		},
		{
			name: "mint declares its base in ID_LIKE",
			in: `ID=linuxmint
ID_LIKE="ubuntu debian"
VERSION_ID="22"
`,
			want:     Distro{ID: "linuxmint", VersionID: "22", IDLike: "ubuntu debian"},
			debian:   true,
			stringer: "linuxmint 22",
		},
		{
			name: "a derivative is recognised through ID_LIKE alone",
			in: `ID=zorin
ID_LIKE=ubuntu
`,
			want:     Distro{ID: "zorin", IDLike: "ubuntu"},
			debian:   true,
			stringer: "zorin",
		},
		{
			name:     "comments and junk lines are ignored",
			in:       "# a comment\n\nnot-a-pair\nID=debian\n",
			want:     Distro{ID: "debian"},
			debian:   true,
			stringer: "debian",
		},
		{
			name:     "empty",
			in:       "",
			want:     Distro{},
			debian:   false,
			stringer: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOSRelease(strings.NewReader(tc.in))
			if got != tc.want {
				t.Errorf("ParseOSRelease() = %+v, want %+v", got, tc.want)
			}
			if got.DebianFamily() != tc.debian {
				t.Errorf("DebianFamily() = %v, want %v", got.DebianFamily(), tc.debian)
			}
			if got.String() != tc.stringer {
				t.Errorf("String() = %q, want %q", got.String(), tc.stringer)
			}
		})
	}
}

func TestParseMissing(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "the driver's missing list",
			in: "Missing system dependencies (3):\n" +
				"    libatk1.0-0t64\n" +
				"    libatk-bridge2.0-0t64\n" +
				"    libnss3\n",
			want: []string{"libatk1.0-0t64", "libatk-bridge2.0-0t64", "libnss3"},
		},
		{
			name: "nothing missing",
			in:   "All system dependencies are installed.\n",
			want: nil,
		},
		{
			name: "prose after the list ends it",
			in: "Missing system dependencies (1):\n" +
				"    libnss3\n" +
				"Install them with sudo npx playwright install-deps\n",
			want: []string{"libnss3"},
		},
		{
			name: "a blank line ends the list",
			in:   "Missing system dependencies (1):\n    libnss3\n\n    libgbm1\n",
			want: []string{"libnss3"},
		},
		{
			name: "windows line endings",
			in:   "Missing system dependencies (2):\r\n    libnss3\r\n    libgbm1\r\n",
			want: []string{"libnss3", "libgbm1"},
		},
		{
			name: "no header, no packages",
			in:   "Downloading Chromium 141.0...\nlibnss3\n",
			want: nil,
		},
		{
			name: "duplicates collapse",
			in:   "Missing system dependencies (2):\n    libnss3\n    libnss3\n",
			want: []string{"libnss3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseMissing(tc.in)
			if !equal(got, tc.want) {
				t.Errorf("ParseMissing() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAptInstallCommand(t *testing.T) {
	t.Run("one line for the operator", func(t *testing.T) {
		got := AptInstallCommand([]string{"libatk1.0-0t64", "libnss3"})
		want := "sudo sh -c 'apt-get update && apt-get install -y --no-install-recommends libatk1.0-0t64 libnss3'"
		if got != want {
			t.Errorf("AptInstallCommand() = %q, want %q", got, want)
		}
		if strings.Contains(got, "\n") {
			t.Error("the command must be a single line")
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := AptInstallCommand(nil); got != "" {
			t.Errorf("AptInstallCommand(nil) = %q, want \"\"", got)
		}
		if got := AptInstallCommand([]string{"", "  "}); got != "" {
			t.Errorf("AptInstallCommand(blanks) = %q, want \"\"", got)
		}
	})

	// The names are parsed out of another program's stdout and the result is
	// pasted into a root shell, so anything that is not a package name is
	// dropped rather than quoted and hoped for.
	t.Run("drops anything that is not a package name", func(t *testing.T) {
		got := AptInstallCommand([]string{"libnss3; rm -rf /", "$(id)", "`id`", "libgbm1", "--allow-downgrades", "ok/../bad"})
		if !strings.Contains(got, "libgbm1") {
			t.Errorf("dropped the good name: %q", got)
		}
		for _, bad := range []string{"rm -rf", "$(", "`", "--allow", ".."} {
			if strings.Contains(got, bad) {
				t.Errorf("kept %q in %q", bad, got)
			}
		}
	})
}

func TestMissingLibsHint(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		soname string
		ok     bool
	}{
		{
			name:   "chrome, as the loader reports it",
			in:     "target closed: Target page, context or browser has been closed\n[pid=408059][err] /opt/browsertest/browsers/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell: error while loading shared libraries: libatk-1.0.so.0: cannot open shared object file: No such file or directory",
			soname: "libatk-1.0.so.0",
			ok:     true,
		},
		{
			name:   "firefox, as Playwright reports it",
			in:     "Host system is missing dependencies to run browsers. Please install them with the following command:\n    sudo npx playwright install-deps",
			soname: "",
			ok:     true,
		},
		{
			name:   "a timeout is not a packaging problem",
			in:     "launch timed out",
			soname: "",
			ok:     false,
		},
		{
			name:   "a closed target on its own is not a packaging problem",
			in:     "target closed: Target page, context or browser has been closed",
			soname: "",
			ok:     false,
		},
		{
			name:   "a missing executable is not a packaging problem",
			in:     "Executable doesn't exist at /opt/browsertest/browsers/chromium-1234/chrome-linux/chrome",
			soname: "",
			ok:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			soname, ok := MissingLibsHint(tc.in)
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v", ok, tc.ok)
			}
			if soname != tc.soname {
				t.Errorf("soname = %q, want %q", soname, tc.soname)
			}
		})
	}
}

func TestAptLocked(t *testing.T) {
	locked := "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 912 (unattended-upgr)"
	if !AptLocked(locked) {
		t.Errorf("AptLocked(%q) = false, want true", locked)
	}
	if AptLocked("E: Unable to locate package libnss3") {
		t.Error("a missing package is not a lock")
	}
}

func TestLaunchRemediation(t *testing.T) {
	ubuntu := Distro{ID: "ubuntu", VersionID: "24.04", IDLike: "debian"}
	got := LaunchRemediation(ubuntu, []string{"libnss3"}, "/opt/browsertest/browser-test-agent")
	for _, want := range []string{"sudo /opt/browsertest/browser-test-agent install-deps", "apt-get install -y --no-install-recommends libnss3"} {
		if !strings.Contains(got, want) {
			t.Errorf("LaunchRemediation() = %q, missing %q", got, want)
		}
	}

	// Without a package list there is still a command worth running.
	if got := LaunchRemediation(ubuntu, nil, "/opt/browsertest/browser-test-agent"); strings.Contains(got, "apt-get") {
		t.Errorf("LaunchRemediation() invented an apt line: %q", got)
	} else if !strings.Contains(got, "install-deps") {
		t.Errorf("LaunchRemediation() = %q, want the install-deps command", got)
	}

	// Playwright has no package names for a non-Debian VM, so neither do we.
	rocky := Distro{ID: "rocky", VersionID: "9.4", IDLike: "rhel centos fedora"}
	manual := LaunchRemediation(rocky, []string{"libnss3"}, "/opt/browsertest/browser-test-agent")
	if strings.Contains(manual, "apt-get") {
		t.Errorf("LaunchRemediation() offered apt on %s: %q", rocky, manual)
	}
	for _, want := range []string{"rocky 9.4", "libatk-1.0"} {
		if !strings.Contains(manual, want) {
			t.Errorf("LaunchRemediation() = %q, missing %q", manual, want)
		}
	}
}

func TestManualRemediationWithoutADistro(t *testing.T) {
	got := ManualRemediation(Distro{})
	if !strings.Contains(got, "this distribution") {
		t.Errorf("ManualRemediation(zero) = %q, want a generic subject", got)
	}
}

// The backend worker parses this JSON out of another module, so the field
// names are a contract rather than an implementation detail.
func TestReportJSONContract(t *testing.T) {
	raw, err := json.Marshal(Report{
		Reason:      ReasonNoRoot,
		OS:          "linux",
		Distro:      "ubuntu 24.04",
		Missing:     []string{"libnss3"},
		Remediation: "sudo sh -c 'apt-get update && apt-get install -y --no-install-recommends libnss3'",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"ok", "reason", "os", "distro", "missing", "remediation"} {
		if _, found := fields[key]; !found {
			t.Errorf("missing key %q in %s", key, raw)
		}
	}
	if fields["ok"] != false {
		t.Errorf("ok = %v, want false", fields["ok"])
	}
}

// Install must not touch the system — or need a driver — where the libraries
// ship with the OS.
func TestInstallOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux takes the real path")
	}
	rep := Install(context.Background(), t.TempDir(), []string{"chromium", "firefox"})
	if !rep.OK || rep.Reason != ReasonNotLinux {
		t.Errorf("Install() = %+v, want OK with reason %q", rep, ReasonNotLinux)
	}
	if rep.Remediation != "" {
		t.Errorf("Remediation = %q, want nothing to do", rep.Remediation)
	}
}

// A distribution Playwright has no package list for is refused before anything
// runs: its own install-deps would log a line and exit 0, installing nothing.
func TestInstallUnsupportedDistro(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the distro check only runs on linux")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	if err := os.WriteFile(path, []byte("ID=rocky\nVERSION_ID=\"9.4\"\nID_LIKE=\"rhel centos fedora\"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	restore := osReleasePath
	osReleasePath = path
	defer func() { osReleasePath = restore }()

	rep := Install(context.Background(), dir, []string{"chromium"})
	if rep.OK || rep.Reason != ReasonUnsupportedDistro {
		t.Errorf("Install() = %+v, want a refusal with reason %q", rep, ReasonUnsupportedDistro)
	}
	if rep.Distro != "rocky 9.4" {
		t.Errorf("Distro = %q, want %q", rep.Distro, "rocky 9.4")
	}
	if !strings.Contains(rep.Remediation, "libatk-1.0") {
		t.Errorf("Remediation = %q, want the library list", rep.Remediation)
	}
}

func TestTail(t *testing.T) {
	if got := tail("  abc  ", 10); got != "abc" {
		t.Errorf("tail() = %q, want %q", got, "abc")
	}
	// The reason a command failed is at the end of its output.
	if got := tail("abcdef", 3); got != "…def" {
		t.Errorf("tail() = %q, want %q", got, "…def")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
