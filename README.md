# browser-test-agent

The VM runner for Radar's browser testing platform — the "location" half of
[`docs/BROWSER_TEST_PLAN.md`](../docs/BROWSER_TEST_PLAN.md) (the plan calls it
`vm-runner`; this folder sits alongside `backend/`, `frontend/` and `traceroute/`
as its own Go module).

A **location** is a VM with this binary and Playwright browsers installed. The
backend worker (`backend/services/radar/browsertest`) reaches it over SSH, one exec
per test run: a `RunSpec` (JSON) arrives on stdin, NDJSON events stream back on
stdout, and screenshots and network logs land in a run folder the worker collects
over SFTP. No daemon, no inbound ports, no cloud credentials on the VM.

## Layout

```
browser-test-agent/
├── cmd/browser-test-agent/     run · session · preview · doctor · install-browsers · purge · version
├── pkg/                        imported by the backend too — one definition of the contract
│   ├── spec/                   RunSpec, Step params, Options, Element, Event types, validation
│   └── vars/                   {{ NAME | filter }} resolution, builtins, secure-value masking
└── internal/
    ├── browser/                Playwright launch per browser/device/options; network hooks
    ├── steps/                  type → executor: navigation, interaction, assertions, extract, JS, API
    ├── locator/                data-testid → CSS → XPath → text, reporting which matched
    ├── runner/                 attempts, retries with a fresh context, events, artifacts
    ├── session/                live browser held open and streamed to the editor (CDP screencast)
    ├── output/                 NDJSON writer that masks every secret on the way out
    └── home/                   /opt/browsertest (C:\browsertest): driver, browsers, runs/<id>/
```

## Commands

| Command | Purpose |
|---|---|
| `run --run-id <id>` | Read a RunSpec on stdin; stream events on stdout; write `runs/<id>/` (`result.ndjson`, `step-NNN.png`, `resources.json`, `payloads/`). Exit 0 whether the test passed or failed — the verdict is in the events; non-zero only when the runner itself could not run (which the worker files as `error`, never counted toward alerts). |
| `doctor` | JSON: OS, arch, disk, the VM's `distro`, runner + Playwright versions, a launch check per browser, implemented step types, `healthy`. A launch that fails for missing OS libraries is reported as that, with the library's name, `missing_deps`, and the command to install them — not as the raw loader error. The worker polls it every 5 minutes and skips unhealthy locations. |
| `session` | Hold a browser open and stream it. The first line of stdin is a `SessionStart` (a RunSpec with no steps, the picker script, and the stream's width and quality); every line after it is an input event — navigate, back, reload, mouse, key, text, pick. stdout carries `ready`, base64 JPEG `frame`s from CDP screencast, `nav`, `picked`, `error`, `closed` (`pkg/spec/session.go`). Backs the editor's live page preview. Chromium and Edge only: screencast has no Firefox equivalent. Closes itself when idle or over age, and keeps nothing on disk. |
| `preview` | Read a RunSpec on stdin (steps ignored); open its start URL in the named browser with the test's options; write the rendered DOM on stdout as NDJSON (`meta`, base64 `html` chunks, `done`, or one `error` line — see `pkg/spec/preview.go`). The static fallback for the editor's preview where `session` is unavailable; keeps nothing on disk. |
| `install-browsers [--from bundle.tar.gz] [--browsers chromium,firefox] [--with-deps]` | Install the Playwright driver (a Node.js release + the `playwright-core` npm package, fetched from nodejs.org and registry.npmjs.org, or from `NODE_MIRROR` / `PLAYWRIGHT_GO_NPM_REGISTRY`; `PLAYWRIGHT_NODEJS_PATH` reuses a Node already on the VM) and the pinned browsers. `--from` extracts a pre-downloaded browser bundle. `--with-deps` installs the browsers' OS packages in the same step, which needs root or passwordless sudo; the worker does not use it, because a VM without sudo would then get no browsers either — it runs `install-deps` separately instead. |
| `install-deps [--browsers chromium,firefox]` | Install those browsers' OS packages, through Playwright's own `install-deps`: `apt-get update && apt-get install` as root, or with passwordless sudo. Debian and Ubuntu only — Playwright has no package list for anything else. No-op on Windows, whose libraries ship with the OS. Always prints a JSON verdict and **exits 0**, a refusal included: by the time it runs the browsers are already installed, so the worker turns a refusal into a problem line on the location rather than a failed bootstrap. `remediation` in that verdict is the command to run as root. Checks with `apt-get install -s` first, so a VM that already has the packages costs about a second. |
| `purge --run-id <id>` / `purge --older-than 24h` | Delete run folders after collection; the safety net runs on every health tick. |
| `version` | Runner and Playwright versions. |

## Event protocol

One JSON object per line; unknown events are ignored by the worker.

```json
{"ev":"run_started","run_id":"r1","attempt":1,"browser":"chrome","browser_version":"131.0","device":"laptop_large"}
{"ev":"step_finished","step_id":"s3","status":"passed","duration_ms":412,"screenshot":"step-003.png","locator_used":"css"}
{"ev":"step_finished","step_id":"s4","status":"failed","error":"Checkout button not found (tried css=#checkout, text=\"Checkout\")","screenshot":"step-004.png"}
{"ev":"attempt_finished","attempt":1,"status":"failed"}
{"ev":"run_finished","status":"failed","failed_step_id":"s4","duration_ms":8123,"resources":"resources.json"}
```

## Steps

All Phase 1–3 step types from the plan are implemented: go to URL, refresh, wait,
scroll, click, hover, type text, press key, select option, assert current URL /
page contains / page lacks / element present / element content / element
attribute / checkbox state, extract variable, assert from JS, extract from JS,
assert file download, upload files, assert requests, run API test.

Every string parameter accepts `{{ VARIABLE }}` and `{{ VARIABLE | filter }}`.
Builtins: `numeric(n)`, `alphabetic(n)`, `alphanumeric(n)`, `date(offset, format)`,
`timestamp(n, unit)`, `uuid`, `public-id`, `result-id`. Filters: `urlEncode`,
`urlDecode`, `base64Encode`, `base64Decode`. Resolution order: global (resolved by
the backend) → local (at run start) → extracted (during the run).

## Secrets

Secure values — secure globals and locals, extracted secure variables, the HTTP
auth password, the client-certificate key — are registered with a masker before
the first event is written. Every event line, every error message, `resources.json`
and every captured payload pass through it; a step that types a secure value takes
no screenshot. The RunSpec itself is never written to disk on the VM.

## Not yet verified on real VMs

The plan's Phase 0 spike checks still stand: headless Chromium/Firefox through a
non-interactive SSH session on both OSes, NDJSON arriving line by line without
buffering, SFTP collection time for a 30-step run, and `install-browsers --from`
on a VM without internet. `doctor` is built to answer the first of these quickly.

`install-deps` covers the OS packages those headless browsers need, so a VM no
longer has to be prepared by hand first; what is unverified there is the offline
case, which it does not address at all — it needs apt and a mirror.

## Build

```bash
make dist      # linux/amd64, linux/arm64, windows/amd64 + SHA256SUMS into dist/
make check     # vet for every target platform, then the unit tests
```

```powershell
.\dist.ps1    # the same dist/ on a Windows machine without make
```

Static binaries (`CGO_ENABLED=0`); Playwright's Node driver is installed on the VM
by `install-browsers`, so no system Node is needed. The Go binding is
`github.com/mxschmitt/playwright-go` — the module path playwright-go declares
from v0.6100 on, even though the repository lives under playwright-community;
requiring it under the community path fails with a module-path mismatch, and
the older community-path releases can no longer install a driver at all. `dist/` is git-ignored: it is
built, not committed.

The backend finds the binaries through `RADAR_BROWSER_TEST_RUNNER_DIR`, which may
name either this folder or `dist/`. The path has to be readable by the backend
*process*: a backend under WSL or Docker cannot open `D:\…`, it needs
`/mnt/d/…` or a mounted volume. The Locations UI's **Bootstrap** action then
uploads the location's binary and runs `install-browsers` on the VM; the error
it reports names every path it tried when nothing is built yet.
