// Command browser-test-agent is the VM runner: the "location" half of the
// browser testing platform. The backend worker reaches it over SSH, one exec
// per test run, sends a RunSpec on stdin and reads NDJSON events on stdout.
// No daemon, no inbound ports on the VM.
//
//	browser-test-agent run --run-id <id>        RunSpec JSON on stdin; events on stdout
//	browser-test-agent preview                  RunSpec JSON on stdin; rendered page for the editor on stdout
//	browser-test-agent session                  SessionStart on stdin, then input events; streams a live browser
//	browser-test-agent doctor                   JSON: OS, disk, browsers, launch check
//	browser-test-agent install-browsers [--from <bundle.tar.gz>] [--browsers chromium,firefox]
//	browser-test-agent install-deps [--browsers chromium,firefox]
//	browser-test-agent purge --run-id <id> | --older-than 24h
//	browser-test-agent version
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"browser-test-agent/internal/home"
	"browser-test-agent/internal/preview"
	"browser-test-agent/internal/runner"
	"browser-test-agent/internal/session"
	"browser-test-agent/pkg/spec"
)

// Version is stamped at build time (see the Makefile).
var Version = "dev"

// maxRunSpecBytes bounds stdin: a RunSpec is kilobytes; anything larger is a
// worker bug, and reading it unbounded is how a runner runs the VM out of
// memory.
const maxRunSpecBytes = 8 << 20

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:])
	case "preview":
		return cmdPreview(ctx, args[1:])
	case "session":
		return cmdSession(ctx, args[1:])
	case "doctor":
		return cmdDoctor(ctx, args[1:])
	case "install-browsers":
		return cmdInstallBrowsers(ctx, args[1:])
	case "install-deps":
		return cmdInstallDeps(ctx, args[1:])
	case "purge":
		return cmdPurge(args[1:])
	case "version":
		return printJSON(map[string]string{"version": Version, "playwright": playwrightVersion()})
	case "-h", "--help", "help":
		usage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
	usage()
	return 2
}

func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run identifier; must match run_id in the RunSpec")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxRunSpecBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read RunSpec from stdin: %v\n", err)
		return runner.ExitInfra
	}
	var s spec.RunSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintf(os.Stderr, "decode RunSpec: %v\n", err)
		return runner.ExitInfra
	}
	if *runID != "" && *runID != s.RunID {
		fmt.Fprintf(os.Stderr, "--run-id %q does not match run_id %q in the RunSpec\n", *runID, s.RunID)
		return runner.ExitInfra
	}

	if err := home.ExportBrowsersPath(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runner.ExitInfra
	}
	return runner.Run(ctx, runner.Config{
		Stdout:        os.Stdout,
		RunnerVersion: Version,
		DriverDir:     home.DriverDir(),
	}, &s)
}

// cmdPreview renders a RunSpec's start URL for the editor: same input as
// run, no steps executed, the page's DOM on stdout (see spec.EncodePreview).
func cmdPreview(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxRunSpecBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read RunSpec from stdin: %v\n", err)
		return runner.ExitInfra
	}
	var s spec.RunSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintf(os.Stderr, "decode RunSpec: %v\n", err)
		return runner.ExitInfra
	}
	if err := home.ExportBrowsersPath(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runner.ExitInfra
	}
	return preview.Run(ctx, preview.Config{Stdout: os.Stdout, DriverDir: home.DriverDir()}, &s)
}

// cmdSession holds a browser open and streams it to the editor: a SessionStart
// on the first line of stdin, input events on the lines after it, and NDJSON
// events on stdout until the session ends (see pkg/spec/session.go).
func cmdSession(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Only the first line is the start payload; the rest of stdin is the input
	// stream, so this must not drain it.
	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		fmt.Fprintf(os.Stderr, "read session start from stdin: %v\n", err)
		return runner.ExitInfra
	}
	var start spec.SessionStart
	if err := json.Unmarshal(bytes.TrimSpace(line), &start); err != nil {
		fmt.Fprintf(os.Stderr, "decode session start: %v\n", err)
		return runner.ExitInfra
	}
	if err := home.ExportBrowsersPath(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runner.ExitInfra
	}
	return session.Run(ctx, session.Config{
		Stdin:     reader,
		Stdout:    os.Stdout,
		DriverDir: home.DriverDir(),
	}, &start)
}

func cmdPurge(args []string) int {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	runID := fs.String("run-id", "", "delete this run's folder")
	olderThan := fs.Duration("older-than", 0, "delete every run folder not modified within this duration, e.g. 24h")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	switch {
	case *runID != "":
		dir, err := home.RunDir(*runID)
		if err != nil {
			return printJSONErr(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			return printJSONErr(fmt.Errorf("remove %s: %w", dir, err))
		}
		return printJSON(map[string]any{"purged": []string{*runID}})

	case *olderThan > 0:
		entries, err := os.ReadDir(home.RunsDir())
		if err != nil {
			if os.IsNotExist(err) {
				return printJSON(map[string]any{"purged": []string{}})
			}
			return printJSONErr(err)
		}
		cutoff := time.Now().Add(-*olderThan)
		purged := []string{}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			dir, err := home.RunDir(e.Name())
			if err != nil {
				continue
			}
			if err := os.RemoveAll(dir); err == nil {
				purged = append(purged, e.Name())
			}
		}
		return printJSON(map[string]any{"purged": purged})
	}
	fmt.Fprintln(os.Stderr, "purge needs --run-id or --older-than")
	return 2
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func printJSONErr(err error) int {
	printJSON(map[string]string{"error": err.Error()})
	return 1
}

func usage() {
	fmt.Fprint(os.Stderr, `browser-test-agent — VM runner for browser tests (a "location")

  run --run-id <id>                   read a RunSpec (JSON) on stdin, stream NDJSON events on stdout,
                                      write screenshots/resources under <home>/runs/<id>/
  preview                             read a RunSpec on stdin, open its start URL with the test's options,
                                      write the rendered page as NDJSON on stdout (no steps run)
  session                             hold a browser open and stream it: a SessionStart on stdin's first line,
                                      input events after it, JPEG frames and events as NDJSON on stdout
  doctor                              JSON report: OS, arch, disk, runner + browser versions, launch check
  install-browsers [--from <bundle>]  install the Playwright driver and pinned browsers
                   [--browsers chromium,firefox]
  install-deps [--browsers chromium,firefox]
                                      install those browsers' OS packages (needs root or passwordless
                                      sudo; Debian/Ubuntu only). Always prints a JSON verdict and exits 0:
                                      when it cannot install them, "remediation" is the command to run as root
  purge --run-id <id>                 delete one run folder (after the worker collected it)
  purge --older-than 24h              delete stale run folders
  version                             runner and Playwright versions

Install directory: /opt/browsertest (Linux), C:\browsertest (Windows), or $BROWSERTEST_HOME.
Exit codes for run: 0 whether the test passed or failed (the verdict is in the events);
non-zero only when the runner itself could not run.
`)
}
