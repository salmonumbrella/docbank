# Task 23 native C profile — preparation

Status: **prepared and manually reviewed; not built or run**.

Target source is clean candidate `587276d12536ac1581d7410da608781e312dcebb`.
The overlay test and controller live outside the repository. Manual Go and
controller review found one attribution flaw and fixed it: `perf script` now
requests only the `sym` field, so a DSO name such as `libsqlite3.so` cannot
make an unresolved sample count as a SQLite function. No further static
findings remain.

Static checks passed: `gofmt`, Python source compilation in memory, and JSON
parsing. No Go compile, test, profile, or repository source change was run.

This packet profiles one warm `Service.Search` call against a synthetic
50,000-document original-file scope. It overlays one Linux/cgo-only test onto
the exact clean candidate checkout, builds the test binary once with `fts5`
and `sqlite_trace`, and then runs that binary as the unprivileged GitHub runner
user while `sudo -n perf` records the target PID. The perf event starts disabled
and the test enables and disables it around the single search through bounded
FIFO acknowledgments.

The fixture uses a valid synthetic 2×2 PNG, a temporary SQLite catalog, local
synthetic blobs, a fake embedding runtime, and the existing production
`Service.Search` path. The original fixture file is trashed before the 50,000
live synthetic versions are searched. No developer vault, provider account,
external embedding service, daemon, or production data is read.

The Go guard checks the warmup and measured SQL execution counts (15 and 15),
authorizer prepare callbacks (12 and 0), generic fallback count (0), media
selections (0), coverage (50,000/50,000), zero measured generation-load delta,
same decoded-generation pointer, zero descriptor delta, and a positive cgo-call
delta. It also disables perf if the test exits through a failure after the
enable request. The controller requires actual SQLite C symbols in the perf
sample stream, fails if none are attributed, and rejects any `PERF_RECORD_LOST`
event.

No Go CPU profiler is started, so the Go pprof sample count is zero by design.
The Linux perf stream samples the whole target process and can also contain Go
symbols; the runner reports that separate count and does not describe it as
zero. The native-only symbol count is filtered from the same stream. This is a
diagnostic profile, not a Go-versus-C time split.

The single search duration, if captured, is one diagnostic observation. It is
not a p50, a speedup claim, or evidence that the 300 ms warm full-search target
passes. The candidate must remain draft while that acceptance target is unmet.

The current main checkout has no workflow input for this overlay, and its CI
manual dispatch runs the regular suite. The fork already has a push-triggered
focused-verification workflow on
`verification/vault-wide-reuse-acceptance-1136`; that route can host the profile
after it is updated to preserve these three harness files and check out this
candidate. The generated overlay and all synthetic databases, blobs, test
binaries, FIFO endpoints, perf data, and logs are temporary and removed when
the hosted runner exits.

## Files

- `diagnostic_test.go`: isolated test overlay; build constraints require
  Linux, cgo, and the `sqlite_trace` build tag.
- `overlay.json`: portable Go overlay template. `runner.py` expands its two
  path markers into a real overlay in a private runner temporary directory.
- `runner.py`: hosted-runner controller with CPU, identity, exact-SHA, clean
  checkout, privilege, timeout, counter, attribution, and cleanup checks.
