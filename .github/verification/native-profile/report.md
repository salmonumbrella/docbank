# Task 23 native C profile — preparation

Status: **prepared and manually reviewed; profiling run pending**.

Target source is clean candidate `da40a73d906e02c957a71de2b196384036e3c08d`.
The overlay test and controller stay outside the candidate checkout's source
tree. Manual Go and controller review found one attribution flaw and fixed it:
`perf script` now requests only the `sym` field, so a DSO name such as
`libsqlite3.so` cannot make an unresolved sample count as a SQLite function.
No further static findings remain.

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

The Go guard requires a positive warmup SQL execution count, then verifies the
measured search executes the same number of SQL statements. It also checks
authorizer prepare callbacks (12 during warmup and 0 during measurement),
generic fallback count (0), media selections (0), coverage (50,000/50,000),
zero measured generation-load delta, the same decoded-generation pointer, zero
descriptor delta, and a positive cgo-call delta. It disables perf if the test
exits through a failure after the enable request. The controller requires
actual SQLite C symbols in the perf sample stream, fails if none are attributed,
and rejects any `PERF_RECORD_LOST` event.

No Go CPU profiler is started, so the Go pprof sample count is zero by design.
The Linux perf stream samples the whole target process and can also contain Go
symbols; the runner reports that separate count and does not describe it as
zero. The native-only symbol count is filtered from the same stream. This is a
diagnostic profile, not a Go-versus-C time split.

The single search duration, if captured, is one diagnostic observation. It is
not a p50, a speedup claim, or evidence that the 300 ms warm full-search target
passes. The candidate must remain draft while that acceptance target is unmet.

The main-pinned PR CI dispatcher has no input for this overlay, and manual CI
dispatch runs the regular suite. This verification branch adds a dedicated
push-triggered workflow on `verification/native-profile-587276d`. It stages the
four reviewed diagnostic inputs outside the candidate checkout, checks out
candidate `da40a73` separately, and runs only the guarded profile. It does not
change the PR branch. The first hosted attempt stopped before building because
the public `ubuntu-24.04` runner exposed four CPUs; the workflow now constrains
the target process to two available CPUs with `taskset` and reports process
affinity and cgroup quota separately. The next attempt reached the exact 50k
synthetic fixture but the fixed SQL-count guard expected 15 executions and
observed 124, so it stopped before perf measurement. The guard now checks parity
between the warmup and measured searches instead of assuming a fixed query
count. The generated overlay and all synthetic databases, blobs, test
binaries, FIFO endpoints, perf data, and logs are temporary and removed when
the hosted runner exits.

## Files

- `diagnostic_test.go`: isolated test overlay; build constraints require
  Linux, cgo, and the `sqlite_trace` build tag.
- `overlay.json`: portable Go overlay template. `runner.py` expands its two
  path markers into a real overlay in a private runner temporary directory.
- `runner.py`: hosted-runner controller with CPU, identity, exact-SHA, clean
  checkout, privilege, timeout, counter, attribution, and cleanup checks.
