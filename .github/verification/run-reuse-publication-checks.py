#!/usr/bin/env python3
"""Run publication checks on a clean, exact commit imported from an owned bundle.

Usage: python3 runner.py --pin FULL_SHA --bundle /absolute/input.bundle
Run only on an ephemeral hosted runner after checking out BASE with full history.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time


BASE = "76f5e703055ecd89e7aba297b9179260aac5e98f"
GENERATED = (
    ".cache/golangci-lint", ".cache/docs-assets", "site", ".site.previous",
    ".site.next.*", "docs/.venv", "docs/site", "docs/zensical-site.*",
    "docs/zensical-public-docs.*", "docs/.zensical-build.*", "docs/zensical-subpath-*",
)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pin", required=True)
    parser.add_argument("--bundle", required=True, type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.pin):
        parser.error("--pin must be one full lowercase commit SHA")
    repo = Path.cwd().resolve()
    runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve()
    bundle = args.bundle.resolve()
    if not bundle.is_relative_to(runner_temp) or bundle.is_relative_to(repo):
        parser.error("bundle must be preserved under RUNNER_TEMP, outside the checkout")
    output = runner_temp / "reuse-exact-benchmark"
    output.mkdir(mode=0o700)  # Refuse to reuse evidence from a prior invocation.
    status = {
        "purpose": "exact unchanged tracked full-Service.Search benchmark; report actual acceptance outcome",
        "base": BASE, "pin": args.pin, "stages": [], "success": False,
        "cleanup": False, "owned_processes_stopped": False,
    }
    active: set[int] = set()
    work: Path | None = None
    before: set[Path] = set()
    tracked: set[str] = set()
    cleanup_errors: list[str] = []
    env = os.environ.copy()
    check_deadline = time.monotonic() + 80 * 60
    finalizing = False  # Reserve ten job minutes for cleanup and artifact upload.

    def save_status() -> None:
        temporary = output / "statuses.json.next"
        temporary.write_text(json.dumps(status, indent=2) + "\n", encoding="utf-8")
        temporary.replace(output / "statuses.json")

    def group_alive(pgid: int) -> bool:
        # A zombie cannot execute or hold source files; its parent must reap it.
        for process in Path("/proc").glob("[0-9]*/stat"):
            try:
                fields = process.read_text().rsplit(")", 1)[1].split()
                if int(fields[2]) == pgid and fields[0] != "Z":
                    return True
            except (OSError, ValueError, IndexError):
                continue
        return False

    def stop_group(pgid: int) -> bool:
        for sig in (signal.SIGTERM, signal.SIGKILL):
            try:
                os.killpg(pgid, sig)
            except ProcessLookupError:
                return True
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                if not group_alive(pgid):
                    return True
                time.sleep(0.05)
        return not group_alive(pgid)

    def run(name: str, argv: list[str], timeout: int = 120) -> bytes:
        stage = {"name": name, "argv": argv, "exit_code": None, "timed_out": False}
        status["stages"].append(stage)
        save_status()
        started = time.monotonic()
        process = None
        log = output / f"{name}.log"
        try:
            with log.open("wb") as stream:
                remaining = check_deadline - time.monotonic()
                if not finalizing and remaining <= 0:
                    raise TimeoutError("80-minute checks budget exhausted; cleanup reserved")
                effective_timeout = min(timeout, 60 if finalizing else max(1, remaining))
                process = subprocess.Popen(
                    argv, cwd=repo, env=env, stdout=stream,
                    stderr=subprocess.STDOUT, start_new_session=True,
                )
                active.add(process.pid)
                try:
                    stage["exit_code"] = process.wait(timeout=effective_timeout)
                except subprocess.TimeoutExpired:
                    stage["timed_out"] = True
                    stop_group(process.pid)
                    stage["exit_code"] = process.wait(timeout=10)
            if stage["exit_code"] != 0 or stage["timed_out"]:
                raise RuntimeError(f"{name} failed; see {log.name}; no retry")
            return log.read_bytes()
        except BaseException as error:
            stage["error"] = f"{type(error).__name__}: {error}"
            raise
        finally:
            if process is not None:
                if stop_group(process.pid):
                    active.discard(process.pid)
                try:
                    stage["exit_code"] = process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    stage["reap_error"] = "process did not exit after group termination"
            stage["elapsed_seconds"] = round(time.monotonic() - started, 3)
            save_status()

    def git(name: str, *argv: str) -> str:
        return run(name, ["git", *argv]).decode("utf-8").strip()

    def generated_paths() -> set[Path]:
        return {path for pattern in GENERATED for path in repo.glob(pattern)}

    def remove_owned(path: Path) -> None:
        if path.is_symlink() or path.is_file():
            path.unlink()
        elif path.exists():
            # Go module-cache directories are deliberately read-only.
            for directory, _, _ in os.walk(path, followlinks=False):
                os.chmod(directory, 0o700)
            shutil.rmtree(path)

    def freeze(label: str) -> bool:
        head = git(f"{label}-head", "rev-parse", "HEAD")
        tree = git(f"{label}-tree", "rev-parse", "HEAD^{tree}")
        porcelain = git(f"{label}-status", "status", "--porcelain=v1", "--untracked-files=all")
        staged = run(f"{label}-staged", ["git", "diff", "--cached", "--binary", "--no-ext-diff"])
        unstaged = run(f"{label}-unstaged", ["git", "diff", "--binary", "--no-ext-diff"])
        frozen = head == args.pin and tree == status.get("source_tree") and not staged and not unstaged
        status[label] = {"head": head, "tree": tree, "status": porcelain,
                         "tracked_source_frozen": frozen, "clean_status": not porcelain}
        if not frozen:
            (output / f"{label}-staged.patch").write_bytes(staged)
            (output / f"{label}-unstaged.patch").write_bytes(unstaged)
            patch = run(f"{label}-against-pin", ["git", "diff", "--binary", "--no-ext-diff", args.pin, "--"])
            (output / f"{label}-against-pin.patch").write_bytes(patch)
            names = run(f"{label}-changed-paths", ["git", "diff", args.pin, "--name-only", "-z"])
            with tarfile.open(output / f"{label}-changed-source.tar.gz", "w:gz", dereference=False) as archive:
                for name in names.decode("utf-8").split("\0"):
                    path = repo / name
                    if name and name in tracked and (path.is_file() or path.is_symlink()):
                        archive.add(path, arcname=name, recursive=False)
        save_status()
        return frozen

    def interrupted(signum: int, _frame: object) -> None:
        raise InterruptedError(f"received signal {signum}")

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    # Snapshot before any command: even failed preflight must preserve inputs.
    before = generated_paths()
    save_status()
    try:
        work = Path(tempfile.mkdtemp(prefix="reuse-publication-owned-", dir=runner_temp))
        for name in ("tmp", "gotmp", "cache", "modcache", "bin", "lintcache", "uvcache", "prek", "pipcache", "xdg", "pycache"):
            (work / name).mkdir()
        env.update(
            GOMAXPROCS="2", GOFLAGS="-p=2 -trimpath -tags=fts5", CGO_ENABLED="1", GOENV="off",
            TMPDIR=str(work / "tmp"), GOTMPDIR=str(work / "gotmp"),
            GOCACHE=str(work / "cache"), GOMODCACHE=str(work / "modcache"),
            GOBIN=str(work / "bin"), GOLANGCI_LINT_CACHE=str(work / "lintcache"),
            PREK_HOME=str(work / "prek"), PIP_CACHE_DIR=str(work / "pipcache"),
            UV_CACHE_DIR=str(work / "uvcache"), UV_PROJECT_ENVIRONMENT=str(work / "docsvenv"),
            UV_PYTHON_DOWNLOADS="never", XDG_CACHE_HOME=str(work / "xdg"),
            PYTHONPYCACHEPREFIX=str(work / "pycache"),
        )
        env["PATH"] = os.pathsep.join((str(work / "venv/bin"), str(work / "bin"), env["PATH"]))
        if git("base-head", "rev-parse", "HEAD") != BASE:
            raise RuntimeError("checkout must start at the exact base commit")
        if git("base-status", "status", "--porcelain=v1", "--untracked-files=all"):
            raise RuntimeError("base checkout is dirty; refusing overlays or stashing")
        git("base-object", "cat-file", "-t", BASE)
        if not bundle.is_file():
            raise RuntimeError("owned source bundle is missing")
        with bundle.open("rb") as stream:
            header = []
            for _ in range(1024):
                line = stream.readline(16384)
                if line == b"\n":
                    break
                if not line or len(line) >= 16384:
                    raise RuntimeError("invalid bundle header")
                header.append(line.decode("ascii").rstrip("\n"))
            else:
                raise RuntimeError("oversized bundle header")
        prerequisites = [line[1:41] for line in header if line.startswith("-")]
        if prerequisites != [BASE]:
            raise RuntimeError("bundle must have the exact base as its sole prerequisite")
        git("bundle-verify", "bundle", "verify", str(bundle))
        heads = git("bundle-heads", "bundle", "list-heads", str(bundle)).splitlines()
        if not heads or any(line.split()[0] != args.pin for line in heads):
            raise RuntimeError("bundle advertised heads must all resolve to the exact PIN")
        git("bundle-fetch", "fetch", "--no-tags", str(bundle), args.pin)
        git("pin-ancestry", "merge-base", "--is-ancestor", BASE, args.pin)
        git("pin-checkout", "-c", "core.hooksPath=/dev/null", "checkout", "--detach", args.pin)
        status["source_tree"] = git("source-tree", "rev-parse", "HEAD^{tree}")
        tracked = set(run("source-tracked-paths", ["git", "ls-files", "-z"]).decode("utf-8").split("\0"))
        before = generated_paths()
        if before:
            raise RuntimeError("fresh checkout contains generated outputs; refusing to reuse or remove them")
        if not freeze("source_before") or not status["source_before"]["clean_status"]:
            raise RuntimeError("exact source checkout is not clean")
        with bundle.open("rb") as bundle_stream:
            bundle_sha256 = hashlib.file_digest(bundle_stream, "sha256").hexdigest()
        (output / "environment.json").write_text(json.dumps({
            "pin": args.pin, "tree": status["source_tree"], "helper_sha": env.get("GITHUB_SHA"),
            "bundle_sha256": bundle_sha256,
            "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "GOMAXPROCS": env["GOMAXPROCS"], "GOFLAGS": env["GOFLAGS"], "CGO_ENABLED": env["CGO_ENABLED"],
            "benchmark": "tracked50k-original-file-x1024-3x",
        }, indent=2) + "\n", encoding="utf-8")
        run("go-version", ["go", "version"])
        run("benchmark", ["go", "test", "-tags", "fts5", "./internal/processing", "-run", "^$", "-bench", "^BenchmarkAllLiveSemanticSearch$/^50000x1x1024$", "-benchtime=3x", "-benchmem", "-count=1", "-parallel=1", "-timeout=45m", "-v"], 3000)
        if not freeze("source_after_prepush"):
            raise RuntimeError("benchmark changed tracked source")
    except BaseException as error:
        status["error"] = f"{type(error).__name__}: {error}"
    finally:
        finalizing = True
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        for pgid in list(active):
            if stop_group(pgid):
                active.discard(pgid)
        status["owned_processes_stopped"] = not active
        status["remaining_process_groups"] = sorted(active)
        try:
            freeze("source_before_cleanup")
        except BaseException as error:
            cleanup_errors.append(f"source readback before cleanup: {error}")
        removed = []
        for path in sorted(generated_paths() - before):
            relative = path.relative_to(repo).as_posix()
            if any(name == relative or name.startswith(relative + "/") for name in tracked):
                cleanup_errors.append(f"refused to remove tracked generated path: {relative}")
                continue
            try:
                remove_owned(path)
                removed.append(relative)
            except OSError as error:
                cleanup_errors.append(f"remove {relative}: {error}")
        if work is not None:
            try:
                remove_owned(work)
            except OSError as error:
                cleanup_errors.append(f"remove owned work: {error}")
        status["removed_generated_paths"] = removed
        status["cleanup_errors"] = cleanup_errors
        status["cleanup"] = not cleanup_errors and (work is None or not work.exists()) and not (generated_paths() - before)
        try:
            freeze("source_after_cleanup")
        except BaseException as error:
            status["final_readback_error"] = f"{type(error).__name__}: {error}"
        required = ("source_before", "source_after_prepush", "source_before_cleanup", "source_after_cleanup")
        status["success"] = (
            "error" not in status and "final_readback_error" not in status
            and status["cleanup"] and status["owned_processes_stopped"]
            and all(status.get(key, {}).get("tracked_source_frozen", False) for key in required)
            and status.get("source_after_cleanup", {}).get("clean_status", False)
        )
        save_status()
    print(json.dumps({"success": status["success"], "results": str(output), "error": status.get("error")}))
    return 0 if status["success"] else 1


if __name__ == "__main__":
    sys.exit(main())
