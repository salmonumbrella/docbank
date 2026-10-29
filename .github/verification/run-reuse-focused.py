#!/usr/bin/env python3
"""Hosted-only paired actual Service.Search diagnostic; never an acceptance gate."""
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import struct
import subprocess
import tempfile
import time

BASE = "a9ba01d956ca7f2e7e476bbbb10a6dd6ac0cce47"
BASELINE = "5d685ac3d51d19b89f598abd9ef35c7e4847018f"
root, temporary = Path.cwd().resolve(), Path(os.environ["RUNNER_TEMP"]).resolve()
packet_path = temporary / "reuse-focused-overlay.json"
packet = json.loads(packet_path.read_text())
evidence = temporary / "reuse-focused-evidence"
evidence.mkdir()  # Refuse to combine this run with old evidence.
status = {"success": False, "purpose": "paired 10k utility only; no 300ms acceptance", "stages": [], "lanes": []}
managed, backups, active = None, {}, set()
current_lane = None
initial_index, index_snapshots = None, []
env = os.environ.copy()
env["GIT_OPTIONAL_LOCKS"] = "0"  # Reduce optional refreshes; exact snapshots classify any actual byte change.
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
    assert path == root / ".git" / "index", "index outside owned ordinary checkout"
    metadata = path.lstat()
    assert stat.S_ISREG(metadata.st_mode) and metadata.st_uid == os.getuid() and metadata.st_nlink == 1
    data = path.read_bytes()
    label = "real-index-" + str(len(status["stages"]))
    (evidence / (label + ".bin")).write_bytes(data)
    entries = call(label + "-entries", ["git", "ls-files", "--stage", "-z"])
    index_snapshots.append({"path": path, "raw": data, "entries": entries,
                            "uid": metadata.st_uid, "gid": metadata.st_gid, "links": metadata.st_nlink,
                            "mode": metadata.st_mode & 0o777})
    call(label + "-debug", ["git", "ls-files", "--debug", "-z"])
    status[label] = {"binary_sha256": hashlib.sha256(data).hexdigest(),
                     "entries_sha256": hashlib.sha256(entries).hexdigest()}
    save()
    return hashlib.sha256(data).hexdigest()


def decode_index(data):
    """Fail closed on formats/flags not present in the reviewed hosted index."""
    assert 32 <= len(data) <= 32 << 20, "unsupported index size"
    signature, version, count = struct.unpack_from(">4sII", data)
    assert signature == b"DIRC" and version == 2 and count <= 131072, "unsupported index format"
    assert hashlib.sha1(data[:-20]).digest() == data[-20:], "invalid index checksum"
    position, entries = 12, []
    for _ in range(count):
        start = position
        assert start + 62 <= len(data) - 20, "truncated index entry"
        values = struct.unpack_from(">10I", data, start)
        flags = struct.unpack_from(">H", data, start + 60)[0]
        assert flags & 0xF000 == 0, "nonordinary stage/extended/assume-valid flags"
        end = data.index(b"\0", start + 62, len(data) - 20)
        name = data[start + 62:end].decode("utf-8")
        assert flags & 0xFFF == end - start - 62, "unsupported long index path"
        path = Path(name)
        assert name and not path.is_absolute() and ".." not in path.parts
        entries.append({"path": name, "offset": start, "stat": values,
                        "mode": format(values[6], "o"), "oid": data[start + 40:start + 60].hex(), "flags": flags})
        position = start + ((end + 1 - start + 7) // 8) * 8
        assert position <= len(data) - 20, "invalid entry padding"
    assert len({entry["path"] for entry in entries}) == count, "duplicate index path"
    extensions = []
    while position < len(data) - 20:
        assert position + 8 <= len(data) - 20, "truncated index extension"
        size = struct.unpack_from(">I", data, position + 4)[0]
        end = position + 8 + size
        assert end <= len(data) - 20, "invalid index extension size"
        extensions.append({"signature": data[position:position + 4].hex(), "bytes": size,
                           "sha256": hashlib.sha256(data[position + 8:end]).hexdigest()})
        position = end
    assert position == len(data) - 20
    directory = {}
    for entry in entries:
        target, components = directory, entry["path"].split("/")
        for component in components[:-1]:
            target = target.setdefault(component, {})
        target[components[-1]] = (entry["mode"], entry["oid"])
    def tree(node):
        children = {name: ("40000", tree(value)) if isinstance(value, dict) else value for name, value in node.items()}
        body = b"".join(mode.encode() + b" " + name.encode() + b"\0" + bytes.fromhex(oid)
                        for name, (mode, oid) in sorted(children.items(), key=lambda item: (item[0] + ("/" if item[1][0] == "40000" else "")).encode()))
        return hashlib.sha1(b"tree " + str(len(body)).encode() + b"\0" + body).hexdigest()
    return {"entries": entries, "extensions": extensions, "tree": tree(directory)}


def classify_index(before, after, owned):
    initial, observed = decode_index(before), decode_index(after)
    assert len(before) == len(after), "index byte length changed"
    assert len(initial["entries"]) == len(observed["entries"]), "index entry count changed"
    assert initial["tree"] == observed["tree"] == status["initial_tree"], "logical index tree changed"
    patched, changes = bytearray(before), []
    for old, new in zip(initial["entries"], observed["entries"]):
        assert old["path"] == new["path"] and old["offset"] == new["offset"], "index order/path changed"
        if old["stat"][:4] != new["stat"][:4]:
            assert old["path"] in owned, "timestamp change on an unowned path"
            position = old["offset"]
            patched[position:position + 16] = after[position:position + 16]
            changes.append({"path": old["path"], "before": old["stat"][:4], "observed": new["stat"][:4]})
    patched[-20:] = hashlib.sha1(patched[:-20]).digest()
    assert bytes(patched) == after, "index changed beyond four owned stat timestamps"
    return {"classification": "unchanged" if before == after else "owned-stat-timestamps-only",
            "logical_tree": observed["tree"], "entry_count": len(observed["entries"]),
            "extensions": observed["extensions"], "timestamp_changes": changes,
            "entries_flags_extensions_nonstat_equal": True}


def finish_index():
    """Restore only this job's index after exact source/semantic classification."""
    observed_hash = index_hash()  # Saves actual observed-final raw/entries/debug.
    observed = index_snapshots[-1]
    status["real_index_unchanged_observed"] = observed_hash == status["original_index_sha256"]
    status["index_restoration"] = {"needed": not status["real_index_unchanged_observed"], "performed": False,
                                   "observed_sha256": observed_hash, "initial_sha256": status["original_index_sha256"]}
    try:
        classification = classify_index(initial_index["raw"], observed["raw"], backups.keys())
        assert initial_index["entries"] == observed["entries"], "staged entry listing changed"
    except BaseException as error:
        status["index_classification"] = {"classification": "refused", "reason": str(error)}
        save()
        raise
    status["index_classification"] = classification
    status["index_semantics_unchanged"] = True
    save()  # Preserve refusal/classification evidence before any restoration.
    assert not active and status["owned_processes_stopped"], "owned processes still active"
    assert status["restored_head"] == BASE and status["restored_tree"] == status["initial_tree"] and not status["restored_status"]
    assert packet["owned_ephemeral_checkout"] is True
    assert env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted"
    assert root == Path(env["GITHUB_WORKSPACE"]).resolve() and (root / ".git").is_dir() and not (root / ".git").is_symlink()
    path = initial_index["path"]
    assert path == observed["path"] == root / ".git" / "index", "index outside owned ordinary checkout"
    metadata = path.lstat()
    assert stat.S_ISREG(metadata.st_mode) and metadata.st_uid == os.getuid() == initial_index["uid"]
    assert metadata.st_gid == initial_index["gid"] and metadata.st_nlink == initial_index["links"] == 1
    assert metadata.st_mode & 0o777 == initial_index["mode"], "index file permissions changed"
    if status["real_index_unchanged_observed"]:
        assert path.read_bytes() == initial_index["raw"], "index changed during readback"
    else:
        lock = path.with_name("index.lock")
        descriptor = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        lock_owned = True
        try:
            with os.fdopen(descriptor, "wb") as output:
                assert path.read_bytes() == observed["raw"], "index changed before atomic restoration"
                os.fchmod(output.fileno(), initial_index["mode"])
                output.write(initial_index["raw"])
                output.flush()
                os.fsync(output.fileno())
            os.replace(lock, path)
            lock_owned = False
            status["index_restoration"]["performed"] = True
        finally:
            if lock_owned:
                lock.unlink()
    final_data = path.read_bytes()  # No Git refresh follows atomic restoration.
    (evidence / "real-index-restored.bin").write_bytes(final_data)
    status["real_index_final_sha256"] = hashlib.sha256(final_data).hexdigest()
    status["real_index_unchanged"] = final_data == initial_index["raw"]
    assert status["real_index_unchanged"], "index restoration was not byte exact"
    status["index_restoration_verified"] = True
    save()


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
    assert env.get("RUNNER_ENVIRONMENT") == "github-hosted", "fresh hosted runner only"
    assert packet["owned_ephemeral_checkout"] is True and packet["experiment"] == "complete-cover"
    assert root == Path(env["GITHUB_WORKSPACE"]).resolve()
    assert (root / ".git").is_dir() and not (root / ".git").is_symlink()
    assert "GIT_INDEX_FILE" not in os.environ, "inherited external index forbidden"
    assert packet["base"] == BASE and packet["baseline_commit"] == BASELINE and packet["mode"] == "paired-profile"
    assert git("initial-head", "rev-parse", "HEAD") == BASE
    assert not git("initial-status", "status", "--porcelain=v1", "--untracked-files=all")
    original_index = index_hash()
    status["original_index_sha256"] = original_index
    initial_index = index_snapshots[-1]
    assert initial_index["path"] == root / ".git" / "index" and initial_index["uid"] == os.getuid() and initial_index["links"] == 1
    status["initial_tree"] = git("initial-tree", "rev-parse", "HEAD^{tree}")
    initial_shape = decode_index(initial_index["raw"])
    assert initial_shape["tree"] == status["initial_tree"], "initial logical index differs from base"
    status["initial_index_tree"] = initial_shape["tree"]
    status["counter_contract"] = {"capture_delta_C": 3, "capture_delta_Go": 3, "fallback_delta": 0,
                                  "resource_endpoints": 0, "warm_extra_loads_decodes": 0,
                                  "indexed_batch_prepares_delta": 12, "cover_phases_delta": 15,
                                  "cover_path": packet["cover_counter_contract"]}
    status["cover_path_counter_status"] = "required-frozen-api"
    assert packet["cover_counter_contract"] == {"api": "NativeSourceResearchCoverCount", "sample_field": "native_cover_phases", "native_delta": 15, "baseline_delta": 0, "indexed_batch_native_delta": 12}
    assert packet["file_counts"] == {"baseline": 16, "native": 29}
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
        assert len(files) == packet["file_counts"][lane]
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
        for item in samples:
            before, after = item["native_before"], item["native_after"]
            assert len(before) == len(after) == 7 and before[:3] == after[:3] == [0, 0, 0]
            expected_captures = 3 if lane == "native" else 0
            assert after[3] - before[3] == after[5] - before[5] == expected_captures
            assert after[6] - before[6] == 0
        assert all(item["native_phase_prepares"] == (12 if lane == "native" else 0) and item["native_cover_phases"] == (15 if lane == "native" else 0) for item in samples)
        status["cover_path_counter_verified_" + lane] = True
        status["counter_contract_verified_" + lane] = True
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
        assert initial_index is not None, "initial owned index snapshot unavailable"
        assert status["restored_head"] == BASE and status["restored_tree"] == status["initial_tree"] and not status["restored_status"]
        finish_index()
    except BaseException as error:
        errors.append("restore/readback: " + str(error))
    if managed:
        try:
            for directory, _, _ in os.walk(managed, followlinks=False): os.chmod(directory, 0o700)
            shutil.rmtree(managed)
        except OSError as error:
            errors.append("remove owned work: " + str(error))
    status["cleanup_errors"] = errors
    status["owned_work_removed"] = managed is None or not managed.exists()
    status["cleanup"] = not errors and status["owned_work_removed"] and status["owned_processes_stopped"] and status.get("real_index_unchanged", False)
    status["success"] = "error" not in status and status["cleanup"] and len(status["lanes"]) == 2
    save()
print(json.dumps({"success": status["success"], "error": status.get("error"), "lanes_completed": len(status["lanes"])}))
raise SystemExit(0 if status["success"] else 1)
