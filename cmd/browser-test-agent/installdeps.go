package main

import (
	"context"
	"flag"
	"strings"

	"browser-test-agent/internal/home"
	"browser-test-agent/internal/osdeps"
)

// cmdInstallDeps installs the browsers' OS-level shared libraries — the step
// install-browsers deliberately leaves out, because it needs root. The worker
// runs it right after install-browsers when it bootstraps a location.
//
// It exits 0 whenever it printed a verdict, a refusal included: by the time it
// runs the browsers are already on disk, so a VM whose packages could not be
// installed is a location to report, not a bootstrap to fail. The verdict's
// `remediation` is the command an operator should run as root.
func cmdInstallDeps(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("install-deps", flag.ContinueOnError)
	browsersFlag := fs.String("browsers", "chromium,firefox", "comma-separated Playwright browsers whose OS packages to install")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Not needed to install packages, but it keeps every command's view of the
	// install directory identical, and the driver resolves the same browsers.
	if err := home.ExportBrowsersPath(); err != nil {
		return printJSONErr(err)
	}
	return printJSON(osdeps.Install(ctx, home.DriverDir(), splitList(*browsersFlag)))
}

// splitList reads a comma-separated flag value.
func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
