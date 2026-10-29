#!/usr/bin/env python3
"""Hosted-only paired actual Service.Search diagnostic; never an acceptance gate."""
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time

BASE = "a9ba01d956ca7f2e7e476bbbb10a6dd6ac0cce47"
BASELINE = "5d685ac3d51d19b89f598abd9ef35c7e4847018f"
root, temporary = Path.cwd(), Path(os.environ["RUNNER_TEMP"])
packet_path = temporary / "reuse-focused-overlay.json"
packet = json.loads(packet_path.read_text())
evidence = temporary / "reuse-focused-evidence"
evidence.mkdir()  # Refuse to combine this run with old evidence.
status = {"success": False, "purpose": "paired 10k utility only; no 300ms acceptance", "stages": [], "lanes": []}
managed, backups, active = None, {}, set()
current_lane = None
env = os.environ.copy()
env["GIT_OPTIONAL_LOCKS"] = "0"  # Even status/VCS stamping must not refresh the real index.
deadline, finalizing = time.monotonic() + 45 * 60, False


def save():
    (evidence / "statuses.json").write_text(json.dumps(status, indent=2) + "\n")


def alive(pgid):
    for path in Path("/proc").glob("[0-9]*/stat"):
        try:
            values = path.read_text().rsplit(")", 1)[1].split()
            if int(values[2]) == pgid and values[0] != "Z":
                return True
        except (OSError, ValueError, IndexError):
            pass
    return False


def stop(pgid):
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            os.killpg(pgid, sig)
        except ProcessLookupError:
            return True
        until = time.monotonic() + 5
        while time.monotonic() < until:
            if not alive(pgid):
                return True
            time.sleep(0.05)
    return not alive(pgid)


def call(name, argv, settings=None, timeout=120):
    stage = {"name": name, "argv": argv, "exit_code": None, "timed_out": False}
    status["stages"].append(stage)
    process, start = None, time.monotonic()
    save()
    try:
        with (evidence / (name + ".log")).open("wb") as output:
            remaining = deadline - time.monotonic()
            if not finalizing and remaining <= 0:
                raise TimeoutError("45-minute profile budget exhausted; cleanup reserved")
            process = subprocess.Popen(argv, cwd=root, env=env | (settings or {}), stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            active.add(process.pid)
            try:
                stage["exit_code"] = process.wait(timeout=min(timeout, 60 if finalizing else max(1, remaining)))
            except subprocess.TimeoutExpired:
                stage["timed_out"] = True
                raise
        assert stage["exit_code"] == 0, name + " failed; no retry"
        return (evidence / (name + ".log")).read_bytes()
    except BaseException as error:
        stage["error"] = str(error)
        raise
    finally:
        if process:
            if stop(process.pid):
                active.discard(process.pid)
            stage["exit_code"] = process.wait(timeout=10)
        stage["seconds"] = time.monotonic() - start
        save()


def git(name, *args, settings=None):
    return call(name, ["git", *args], settings).decode().strip()


def index_hash():
    path = Path(git("real-index-path-" + str(len(status["stages"])), "rev-parse", "--git-path", "index"))
    path = path if path.is_absolute() else root / path
    data = path.read_bytes() if path.exists() else b""
    label = "real-index-" + str(len(status["stages"]))
    if path.exists():
        (evidence / (label + ".bin")).write_bytes(data)
    entries = call(label + "-entries", ["git", "ls-files", "--stage", "-z"])
    call(label + "-debug", ["git", "ls-files", "--debug", "-z"])
    status[label] = {"binary_sha256": hashlib.sha256(data).hexdigest() if path.exists() else None,
                     "entries_sha256": hashlib.sha256(entries).hexdigest()}
    save()
    return hashlib.sha256(data).hexdigest() if path.exists() else None


def source_tree(label):
    settings = {"GIT_INDEX_FILE": str(managed / "snapshot.index")}
    git(label + "-read-tree", "read-tree", BASE, settings=settings)
    # Only explicit packet-owned paths may enter this temporary index.
    changed = call(label + "-changed-paths", ["git", "diff", "HEAD", "--name-only", "-z", "--no-ext-diff"])
    added = call(label + "-untracked-paths", ["git", "ls-files", "--others", "--exclude-standard", "-z"])
    observed = {name for name in (changed + added).decode().split("\0") if name}
    assert observed <= backups.keys(), "source changed outside owned packet paths: " + repr(sorted(observed - backups.keys()))
    owned = sorted(name for name, previous in backups.items() if previous is not None or (root / name).exists())
    if owned:
        git(label + "-add", "add", "--", *owned, settings=settings)
    tree = git(label + "-tree", "write-tree", settings=settings)
    head = git(label + "-head", "rev-parse", "HEAD")
    porcelain = git(label + "-status", "status", "--porcelain=v1", "--untracked-files=all")
    status[label] = {"tree": tree, "head": head, "status": porcelain}
    save()
    assert head == BASE, "real HEAD changed"
    return tree


def restore():
    for name, previous in backups.items():
        path = root / name
        if previous is None:
            path.unlink(missing_ok=True)
        else:
            data, mode = previous
            path.write_bytes(data)
            path.chmod(mode)


def interrupt(signum, _frame):
    raise InterruptedError("received signal " + str(signum))


signal.signal(signal.SIGTERM, interrupt)
signal.signal(signal.SIGINT, interrupt)
save()
try:
    assert os.environ.get("GITHUB_ACTIONS") == "true", "hosted workflow only"
    assert packet["base"] == BASE and packet["baseline_commit"] == BASELINE and packet["mode"] == "paired-profile"
    assert git("initial-head", "rev-parse", "HEAD") == BASE
    assert not git("initial-status", "status", "--porcelain=v1", "--untracked-files=all")
    original_index = index_hash()
    status["original_index_sha256"] = original_index
    status["packet_sha256"] = hashlib.sha256(packet_path.read_bytes()).hexdigest()
    managed = Path(tempfile.mkdtemp(prefix="reuse-paired-owned-", dir=temporary))
    for name in ("cache", "modcache", "bin", "tmp", "gotmp"):
        (managed / name).mkdir()
    env.update(GOMAXPROCS="2", GOFLAGS="-p=2 -trimpath -tags=fts5", CGO_ENABLED="1", GOENV="off",
               GOCACHE=str(managed / "cache"), GOMODCACHE=str(managed / "modcache"), GOBIN=str(managed / "bin"),
               TMPDIR=str(managed / "tmp"), GOTMPDIR=str(managed / "gotmp"))
    for name in ("GOGC", "GOMEMLIMIT", "GODEBUG"):
        assert name not in env, "require normal runtime defaults: unexpected " + name
    for lane in ("baseline", "native"):
        files = packet[lane + "_files"]
        assert len(files) == (16 if lane == "baseline" else 28)
        for name, item in files.items():
            path = Path(name)
            assert not path.is_absolute() and ".." not in path.parts
            assert name.startswith(("internal/store/", "internal/processing/", "docs/architecture/", "sqlite/mattn/")) and path.suffix in (".go", ".md", ".c", ".h")
            data = base64.b64decode(item["content"], validate=True)
            assert hashlib.sha256(data).hexdigest() == item["sha256"]
            if name not in backups:
                target = root / path
                assert not target.is_symlink()
                backups[name] = (target.read_bytes(), target.stat().st_mode & 0o777) if target.exists() else None
    (evidence / "source.json").write_text(json.dumps({key: value for key, value in packet.items() if not key.endswith("_files")} | {
        "files": {lane: {name: item["sha256"] for name, item in packet[lane + "_files"].items()} for lane in ("baseline", "native")}}, indent=2) + "\n")
    for name, argv in (("uname", ["uname", "-smr"]), ("cpu", ["lscpu"]), ("go-version", ["go", "version"]), ("go-env", ["go", "env", "GOFLAGS", "CGO_ENABLED", "GOOS", "GOARCH", "GOVERSION"])):
        call(name, argv)
    (evidence / "runtime-environment.json").write_text(json.dumps({key: env.get(key, "unset") for key in ("GOMAXPROCS", "GOFLAGS", "CGO_ENABLED", "GOGC", "GOMEMLIMIT", "GODEBUG")}, indent=2) + "\n")
    for lane in ("baseline", "native"):
        current_lane = lane
        for name, item in packet[lane + "_files"].items():
            target = root / name
            target.write_bytes(base64.b64decode(item["content"], validate=True))
        expected = packet["trees"][lane]
        assert source_tree(lane + "-before") == expected
        scratch, gotmp = managed / (lane + "-synthetic"), managed / (lane + "-gotmp")
        scratch.mkdir(); gotmp.mkdir()
        settings = {"TMPDIR": str(scratch), "GOTMPDIR": str(gotmp), "REUSE_EXPECT_NATIVE": "1" if lane == "native" else "0", "REUSE_DIAGNOSTIC_CPU": str(evidence / (lane + ".cpu.pprof"))}
        log = call(lane, ["go", "test", "-tags", "fts5", "./internal/processing", "-run", "^TestReuseNativeReaderDiagnostic$", "-count=1", "-parallel=1", "-timeout=20m", "-v"], settings, 1500).decode()
        assert "REUSE_NATIVE_COMPLETE samples=5 documents=10000 dimensions=1024 normal_gc=true" in log, "missing completed real diagnostic (skipped test is failure)"
        samples = [json.loads(line.split("REUSE_NATIVE_SAMPLE ", 1)[1]) for line in log.splitlines() if "REUSE_NATIVE_SAMPLE " in line]
        assert len(samples) == 5 and [item["sample"] for item in samples] == list(range(5))
        assert all(item["native_present"] == (lane == "native") and item["warm_extra_loads_decodes"] == 0 for item in samples)
        assert all((0 < item["native_phase_prepares"] <= 42) if lane == "native" else item["native_phase_prepares"] == 0 for item in samples)
        assert (evidence / (lane + ".cpu.pprof")).stat().st_size > 0
        assert source_tree(lane + "-after") == expected
        residue = {"synthetic": len(list(scratch.iterdir())), "go_tmp": len(list(gotmp.iterdir()))}
        assert not any(residue.values()), "test/tool left owned synthetic roots"
        status["lanes"].append({"lane": lane, "samples": samples, "p50_ns": sorted(item["elapsed_ns"] for item in samples)[2], "cleanup_residue": residue})
        restore()
        assert source_tree(lane + "-restored") == git(lane + "-base-tree", "rev-parse", BASE + "^{tree}")
        assert not status[lane + "-restored"]["status"]
        current_lane = None
        save()
except BaseException as error:
    status["error"] = type(error).__name__ + ": " + str(error)
finally:
    finalizing = True
    signal.signal(signal.SIGTERM, signal.SIG_IGN); signal.signal(signal.SIGINT, signal.SIG_IGN)
    errors = []
    for pgid in list(active):
        if stop(pgid): active.discard(pgid)
    status["owned_processes_stopped"] = not active
    if current_lane and managed is not None:
        try:
            source_tree(current_lane + "-failure-source")
        except BaseException as error:
            errors.append("failed-lane source readback: " + str(error))
    try:
        if backups:
            call("final-diff", ["git", "diff", "--binary", "--no-ext-diff"])
            restore()
        status["restored_head"] = git("final-head", "rev-parse", "HEAD")
        status["restored_tree"] = git("final-base-tree", "rev-parse", "HEAD^{tree}")
        if managed is not None:
            assert source_tree("final-restored-source") == status["restored_tree"]
        status["restored_status"] = git("final-status", "status", "--porcelain=v1", "--untracked-files=all")
        status["real_index_unchanged"] = index_hash() == status.get("original_index_sha256")
        assert status["restored_head"] == BASE and not status["restored_status"] and status["real_index_unchanged"]
    except BaseException as error:
        errors.append("restore/readback: " + str(error))
    if managed:
        try:
            for directory, _, _ in os.walk(managed, followlinks=False): os.chmod(directory, 0o700)
            shutil.rmtree(managed)
        except OSError as error:
            errors.append("remove owned work: " + str(error))
    status["cleanup_errors"] = errors
    status["cleanup"] = not errors and (managed is None or not managed.exists()) and status["owned_processes_stopped"]
    status["success"] = "error" not in status and status["cleanup"] and len(status["lanes"]) == 2
    save()
print(json.dumps({"success": status["success"], "error": status.get("error"), "lanes_completed": len(status["lanes"])}))
raise SystemExit(0 if status["success"] else 1)
