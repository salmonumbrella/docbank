#!/usr/bin/env python3
"""Run one synthetic warm-search C-symbol profile on a GitHub Linux runner."""

from __future__ import annotations

import argparse
import collections
import json
import os
import pathlib
import platform
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time


TEST_NAME = "TestNativeWarmSearchProfile"
MAX_TEST_SECONDS = 20 * 60
PERF_SAMPLE_FREQUENCY = 997
MAX_REPORTED_SYMBOLS = 20
PERF_IP_RE = re.compile(r"^(?:0x)?[0-9a-fA-F]+$")
PROCESS_SETTLE_SECONDS = 0.5
STOP_TIMEOUT_SECONDS = 10
PERF_DRAIN_SECONDS = 30


class ProfileError(RuntimeError):
    pass


def run_checked(
    command: list[str], *, cwd: pathlib.Path, env: dict[str, str], timeout: int = 120
) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        command,
        cwd=cwd,
        env=env,
        check=False,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        raise ProfileError(f"command failed ({result.returncode}): {command[0]} {detail}")
    return result


def cpu_quota() -> float | None:
    quota_path = pathlib.Path("/sys/fs/cgroup/cpu.max")
    if not quota_path.is_file():
        return None
    fields = quota_path.read_text(encoding="ascii").split()
    if len(fields) != 2 or fields[0] == "max":
        return None
    quota, period = int(fields[0]), int(fields[1])
    if quota <= 0 or period <= 0:
        raise ProfileError("invalid Linux cgroup CPU quota")
    return quota / period


def verify_host() -> tuple[float, int, str, float | None]:
    if os.environ.get("GITHUB_ACTIONS", "").lower() != "true":
        raise ProfileError("refusing to run outside GitHub Actions")
    if platform.system() != "Linux" or os.environ.get("RUNNER_OS") != "Linux":
        raise ProfileError("this native perf profile requires a Linux runner")
    if os.geteuid() == 0:
        raise ProfileError("the test target must run as the unprivileged runner user")
    affinity = os.sched_getaffinity(0)
    quota = cpu_quota()
    effective = min(float(len(affinity)), quota) if quota is not None else float(len(affinity))
    if not 1.8 <= effective <= 2.2:
        raise ProfileError(f"expected an effective 2-CPU runner, found {effective:.2f}")
    return effective, len(affinity), platform.machine(), quota


def expand_overlay(
    template_path: pathlib.Path,
    overlay_path: pathlib.Path,
    repo_root: pathlib.Path,
    harness_path: pathlib.Path,
) -> None:
    template = json.loads(template_path.read_text(encoding="utf-8"))
    replacements = template.get("Replace")
    if not isinstance(replacements, dict) or len(replacements) != 1:
        raise ProfileError("overlay.json must contain exactly one replacement")
    target_template, source_template = next(iter(replacements.items()))
    if target_template != "__DOCBANK_ROOT__/internal/processing/native_warm_search_profile_test.go":
        raise ProfileError("overlay target template changed unexpectedly")
    if source_template != "__HARNESS_DIR__/diagnostic_test.go":
        raise ProfileError("overlay source template changed unexpectedly")
    target = repo_root / "internal/processing/native_warm_search_profile_test.go"
    if target.exists():
        raise ProfileError("refusing to overlay an existing repository file")
    expanded = {str(target): str(harness_path.resolve())}
    overlay_path.write_text(
        json.dumps({"Replace": expanded}, indent=2) + "\n", encoding="utf-8"
    )


def wait_until_stopped(pid: int, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    status_path = pathlib.Path(f"/proc/{pid}/status")
    while time.monotonic() < deadline:
        if not status_path.exists():
            raise ProfileError("test launcher exited before entering the stopped state")
        for line in status_path.read_text(encoding="ascii").splitlines():
            if line.startswith("State:") and line.split()[1] in {"T", "t"}:
                return
        time.sleep(0.01)
    raise ProfileError("test launcher did not stop before the profile was attached")


def terminate_group(process: subprocess.Popen[str] | None) -> None:
    if process is None or process.poll() is not None:
        return

    def send_group_signal(signum: signal.Signals) -> None:
        try:
            os.killpg(process.pid, signum)
            return
        except ProcessLookupError:
            return
        except PermissionError:
            # perf runs as root under sudo. If the runner cannot signal its
            # process group directly, use sudo for this exact group ID.
            result = subprocess.run(
                [
                    "sudo",
                    "-n",
                    "kill",
                    f"-{signum.name.removeprefix('SIG')}",
                    "--",
                    f"-{process.pid}",
                ],
                check=False,
                capture_output=True,
                text=True,
                timeout=5,
            )
            if result.returncode != 0 and process.poll() is None:
                detail = (result.stderr or result.stdout).strip()
                raise ProfileError(f"could not signal process group {process.pid}: {detail}")

    send_group_signal(signal.SIGTERM)
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        send_group_signal(signal.SIGKILL)
        process.wait(timeout=5)


def read_text(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace") if path.exists() else ""


def parse_profile_symbols(
    script: str,
) -> tuple[int, int, int, collections.Counter[str], collections.Counter[str], int]:
    perf_samples = 0
    resolved_samples = 0
    unresolved_samples = 0
    native_symbols: collections.Counter[str] = collections.Counter()
    observed_symbols: collections.Counter[str] = collections.Counter()
    go_symbol_samples = 0
    for line in script.splitlines():
        fields = line.split()
        if not fields or PERF_IP_RE.fullmatch(fields[0]) is None:
            continue
        perf_samples += 1
        # The script command requests only ip,sym in that order. Keep IPs
        # private; use them only to retain samples whose symbol is unresolved.
        symbol = " ".join(fields[1:])
        if not symbol or symbol.lower() in {"?", "unknown", "[unknown]"}:
            unresolved_samples += 1
            continue
        resolved_samples += 1
        native = re.match(r"^(sqlite3[A-Za-z0-9_]*)\b", symbol)
        if native:
            native_symbols[native.group(1)] += 1
        if re.search(
            r"\b(runtime\.|go\.kenn\.io/docbank/|github\.com/mattn/go-sqlite3\.)",
            symbol,
        ):
            go_symbol_samples += 1
        observed_symbols[symbol] += 1
    return (
        perf_samples,
        resolved_samples,
        unresolved_samples,
        native_symbols,
        observed_symbols,
        go_symbol_samples,
    )


def require_profile_guards(test_output: str) -> tuple[str, int, int, str]:
    if f"--- PASS: {TEST_NAME}" not in test_output:
        raise ProfileError("the exact native-profile test did not pass")
    warmup = re.search(r"profile_guard warmup_[^\n]+", test_output)
    measured = re.search(r"profile_guard measured_[^\n]+", test_output)
    duration = re.search(r"profile_guard one_search_wall_time=([^;\n]+)", test_output)
    if warmup is None or measured is None or duration is None:
        raise ProfileError("one or more profile guard log lines are missing")
    expected_warmup = (
        "warmup_fallbacks=0",
        "warmup_generation_loads=1",
        "media_resources=0",
        "coverage=50000/50000",
    )
    expected_measured = (
        "measured_select_prepare_callbacks=0",
        "measured_fallbacks=0",
        "measured_generation_load_delta=0",
        "same_generation=true",
        "fd_delta=0",
        "media_resources=0",
        "coverage=50000/50000",
        "go_cpu_profile=not_collected",
    )
    if any(item not in warmup.group(0) for item in expected_warmup):
        raise ProfileError(f"warmup counters did not match: {warmup.group(0)}")
    if any(item not in measured.group(0) for item in expected_measured):
        raise ProfileError(f"measured counters did not match: {measured.group(0)}")
    warmup_selects = re.search(r"\bwarmup_selects=([0-9]+)\b", warmup.group(0))
    warmup_prepares = re.search(
        r"\bwarmup_select_prepare_callbacks=([0-9]+)\b", warmup.group(0)
    )
    measured_selects = re.search(r"\bmeasured_selects=([0-9]+)\b", measured.group(0))
    if warmup_selects is None or warmup_prepares is None or measured_selects is None:
        raise ProfileError("warmup or measured SQL counters are missing")
    warmup_select_count = int(warmup_selects.group(1))
    if warmup_select_count == 0:
        raise ProfileError("warmup search executed no SELECT statements")
    warmup_prepare_count = int(warmup_prepares.group(1))
    if warmup_prepare_count == 0:
        raise ProfileError("warmup search prepared no SELECT statements")
    measured_select_count = int(measured_selects.group(1))
    if measured_select_count == 0:
        raise ProfileError("measured search executed no SELECT statements")
    if warmup_select_count != measured_select_count + 1:
        raise ProfileError(
            "warmup must execute one generation-load SELECT avoided by the cached search"
        )
    cgo_delta = re.search(r"cgo_call_delta=([0-9]+)", measured.group(0))
    if cgo_delta is None or int(cgo_delta.group(1)) < 1:
        raise ProfileError("measured search did not record a positive cgo call delta")
    return duration.group(1), warmup_select_count, warmup_prepare_count, measured.group(0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=pathlib.Path, default=os.environ.get("GITHUB_WORKSPACE"))
    parser.add_argument("--expected-sha", default=os.environ.get("DOCBANK_PROFILE_EXPECTED_SHA"))
    args = parser.parse_args()
    if args.repo is None:
        raise ProfileError("pass --repo or set GITHUB_WORKSPACE")
    if not args.expected_sha or not re.fullmatch(r"[0-9a-fA-F]{40}", args.expected_sha):
        raise ProfileError("pass the full expected candidate SHA")

    repo_root = args.repo.resolve()
    if not (repo_root / "go.mod").is_file() or not (repo_root / "internal/processing").is_dir():
        raise ProfileError("--repo must point to the Docbank module root")
    artifact_dir = pathlib.Path(__file__).resolve().parent
    harness_path = artifact_dir / "diagnostic_test.go"
    template_path = artifact_dir / "overlay.json"
    if not harness_path.is_file() or not template_path.is_file():
        raise ProfileError("diagnostic_test.go and overlay.json must accompany runner.py")

    effective_cpu_count, affinity_count, architecture, cpu_quota_count = verify_host()
    env = os.environ.copy()
    env.update(
        {
            "CGO_ENABLED": "1",
            "GOMAXPROCS": "2",
            "GOFLAGS": "-p=2",
            "GOTOOLCHAIN": "local",
            "TZ": "UTC",
        }
    )
    head = run_checked(["git", "rev-parse", "HEAD"], cwd=repo_root, env=env).stdout.strip()
    if head.lower() != args.expected_sha.lower():
        raise ProfileError(f"checkout SHA {head} does not match the requested candidate")
    dirty = run_checked(["git", "status", "--porcelain"], cwd=repo_root, env=env).stdout.strip()
    if dirty:
        raise ProfileError("repository checkout must be clean before profile preparation")

    perf_version = run_checked(["sudo", "-n", "perf", "--version"], cwd=repo_root, env=env).stdout.strip()
    run_checked(
        ["sudo", "-n", "perf", "stat", "-e", "task-clock:u", "--", "true"],
        cwd=repo_root,
        env=env,
        timeout=30,
    )
    go_version = run_checked(["go", "version"], cwd=repo_root, env=env).stdout.strip()

    runner_temp = pathlib.Path(os.environ.get("RUNNER_TEMP", tempfile.gettempdir())).resolve()
    runner_temp.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="docbank-native-profile-", dir=runner_temp) as temp_name:
        work_dir = pathlib.Path(temp_name)
        test_temp = work_dir / "synthetic"
        test_temp.mkdir(mode=0o700)
        test_binary = work_dir / "processing.test"
        overlay_path = work_dir / "overlay.json"
        expand_overlay(template_path, overlay_path, repo_root, harness_path)

        build = run_checked(
            [
                "go",
                "test",
                "-c",
                "-tags=fts5,sqlite_trace",
                f"-overlay={overlay_path}",
                f"-o={test_binary}",
                "./internal/processing",
            ],
            cwd=repo_root,
            env=env,
            timeout=15 * 60,
        )
        del build

        control_fifo = work_dir / "perf-control.fifo"
        ack_fifo = work_dir / "perf-ack.fifo"
        os.mkfifo(control_fifo, 0o600)
        os.mkfifo(ack_fifo, 0o600)
        if "," in str(control_fifo) or "," in str(ack_fifo):
            raise ProfileError("temporary FIFO path cannot contain a comma")

        target_env = env.copy()
        target_env.update(
            {
                "TMPDIR": str(test_temp),
                "DOCBANK_PERF_CONTROL_FIFO": str(control_fifo),
                "DOCBANK_PERF_ACK_FIFO": str(ack_fifo),
            }
        )
        target_log = work_dir / "test.log"
        perf_log = work_dir / "perf.log"
        perf_data = work_dir / "perf.data"
        test_process: subprocess.Popen[str] | None = None
        perf_process: subprocess.Popen[str] | None = None
        try:
            with target_log.open("w", encoding="utf-8") as target_output:
                test_process = subprocess.Popen(
                    [
                        "bash",
                        "-c",
                        'kill -STOP $$; exec "$@"',
                        "docbank-profile-target",
                        str(test_binary),
                        "-test.run=^" + TEST_NAME + "$",
                        "-test.v",
                    ],
                    cwd=repo_root,
                    env=target_env,
                    stdout=target_output,
                    stderr=subprocess.STDOUT,
                    start_new_session=True,
                    text=True,
                )
            wait_until_stopped(test_process.pid, STOP_TIMEOUT_SECONDS)

            with perf_log.open("w", encoding="utf-8") as perf_output:
                perf_process = subprocess.Popen(
                    [
                        "sudo",
                        "-n",
                        "perf",
                        "record",
                        f"--control=fifo:{control_fifo},{ack_fifo}",
                        "--delay=-1",
                        "-e",
                        "cpu-clock:u",
                        "-F",
                        str(PERF_SAMPLE_FREQUENCY),
                        "-p",
                        str(test_process.pid),
                        "-o",
                        str(perf_data),
                    ],
                    cwd=repo_root,
                    env=env,
                    stdout=perf_output,
                    stderr=subprocess.STDOUT,
                    start_new_session=True,
                    text=True,
                )
            time.sleep(PROCESS_SETTLE_SECONDS)
            if perf_process.poll() is not None:
                raise ProfileError("perf recorder exited before the test target resumed")
            os.kill(test_process.pid, signal.SIGCONT)

            deadline = time.monotonic() + MAX_TEST_SECONDS
            while test_process.poll() is None:
                perf_status = perf_process.poll()
                if perf_status is not None and perf_status != 0:
                    raise ProfileError(
                        f"perf recorder exited with status {perf_status} while the test target was running"
                    )
                if time.monotonic() >= deadline:
                    raise ProfileError("synthetic profile test exceeded its time limit")
                time.sleep(0.1)
            test_status = test_process.returncode
            try:
                perf_status = perf_process.wait(timeout=PERF_DRAIN_SECONDS)
            except subprocess.TimeoutExpired as exc:
                raise ProfileError("perf recorder did not drain after the test exited") from exc

            output = read_text(target_log)
            profile_log = read_text(perf_log)
            if test_status != 0:
                raise ProfileError(f"profile test exited {test_status}:\n{output}\n{profile_log}")
            if perf_status != 0:
                raise ProfileError(f"perf recorder exited {perf_status}:\n{profile_log}")
            search_wall_time, sql_selects_per_search, warmup_prepares, measured_guard = (
                require_profile_guards(output)
            )
            if not perf_data.is_file() or perf_data.stat().st_size == 0:
                raise ProfileError("perf produced no profile data")

            script = run_checked(
                [
                    "sudo",
                    "-n",
                    "perf",
                    "script",
                    "-i",
                    str(perf_data),
                    "-F",
                    "ip,sym",
                    "--show-lost-events",
                ],
                cwd=repo_root,
                env=env,
                timeout=90,
            ).stdout
            lost_events = [line for line in script.splitlines() if "PERF_RECORD_LOST" in line]
            if lost_events:
                raise ProfileError("perf recorded lost events: " + " | ".join(lost_events[:5]))
            (
                perf_sample_count,
                resolved_symbol_sample_count,
                unresolved_symbol_sample_count,
                native_symbols,
                observed_symbols,
                go_symbol_samples,
            ) = parse_profile_symbols(script)
            native_sample_count = sum(native_symbols.values())

            summary = {
                "candidate_sha": head,
                "runner_os": "Linux",
                "architecture": architecture,
                "effective_cpu_count": effective_cpu_count,
                "cpu_affinity_count": affinity_count,
                "cpu_quota_count": cpu_quota_count,
                "go_version": go_version,
                "perf_version": perf_version,
                "profile_event": f"cpu-clock:u at {PERF_SAMPLE_FREQUENCY} Hz",
                "perf_script_sample_count": perf_sample_count,
                "resolved_perf_symbol_sample_count": resolved_symbol_sample_count,
                "unresolved_perf_symbol_sample_count": unresolved_symbol_sample_count,
                "observed_perf_symbols": observed_symbols.most_common(MAX_REPORTED_SYMBOLS),
                "native_sqlite_sample_count": native_sample_count,
                "native_sqlite_symbols": native_symbols.most_common(MAX_REPORTED_SYMBOLS),
                "perf_go_symbol_samples": go_symbol_samples,
                "perf_lost_events": 0,
                "go_cpu_profile": "not collected",
                "go_pprof_samples": 0,
                "warm_search_wall_time": search_wall_time,
                "sqlite_select_executions_per_search": sql_selects_per_search,
                "sqlite_select_prepare_callbacks_during_warmup": warmup_prepares,
                "warmup_and_measured_guards": [
                    line.strip()
                    for line in output.splitlines()
                    if "profile_guard warmup_" in line or "profile_guard measured_" in line
                ],
                "source_checkout_clean_before_and_after": True,
                "latency_p50_or_acceptance_claim": False,
            }
            if measured_guard not in "\n".join(summary["warmup_and_measured_guards"]):
                raise ProfileError("summary lost the measured search guard line")
            after_dirty = run_checked(
                ["git", "status", "--porcelain"], cwd=repo_root, env=env
            ).stdout.strip()
            if after_dirty:
                raise ProfileError("profile test changed files in the repository checkout")

            summary_text = json.dumps(summary, indent=2)
            print(summary_text)
            step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
            if step_summary:
                with pathlib.Path(step_summary).open("a", encoding="utf-8") as output_file:
                    output_file.write("## Native warm-search profile\n\n")
                    output_file.write("```json\n" + summary_text + "\n```\n")
            if native_sample_count == 0:
                raise ProfileError("perf recorded no samples attributed to SQLite C symbols")
        finally:
            terminate_group(test_process)
            terminate_group(perf_process)
            if test_process is not None:
                try:
                    test_process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
            if perf_process is not None:
                try:
                    perf_process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.SubprocessError, ProfileError, ValueError) as error:
        print(f"::error::{error}", file=sys.stderr)
        sys.exit(1)
