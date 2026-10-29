import json
import sys
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time

KIND, PIN = sys.argv[1:]
assert KIND in ('core', 'client') and len(PIN) == 40 and all(c in '0123456789abcdef' for c in PIN)
repo = Path.cwd()
output = Path(os.environ['RUNNER_TEMP']) / (KIND + '-push-results')
output.mkdir(mode=0o700)
work = Path(tempfile.mkdtemp(prefix=KIND + '-push-', dir=os.environ['RUNNER_TEMP']))
tool = repo / '.cache/golangci-lint'
assert not tool.exists()
env = os.environ.copy()
for name in ('tmp', 'gotmp', 'cache', 'modcache', 'bin', 'lintcache'):
    (work / name).mkdir()
env.update(GOMAXPROCS='2', GOFLAGS='-p=2 -trimpath', CGO_ENABLED='1',
           TMPDIR=str(work / 'tmp'), GOTMPDIR=str(work / 'gotmp'),
           GOCACHE=str(work / 'cache'), GOMODCACHE=str(work / 'modcache'),
           GOBIN=str(work / 'bin'), GOLANGCI_LINT_CACHE=str(work / 'lintcache'))
env['PATH'] = str(work / 'venv/bin') + ':' + str(work / 'bin') + ':' + env['PATH']
active = set()
status = {'kind': KIND, 'head': PIN, 'acceptance': False, 'stages': [], 'cleanup': False}


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def stop(pgid):
    try:
        os.killpg(pgid, signal.SIGTERM)
    except ProcessLookupError:
        return True
    for _ in range(100):
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.05)
    try:
        os.killpg(pgid, signal.SIGKILL)
    except ProcessLookupError:
        return True
    return False


def call(name, argv, timeout):
    started = time.monotonic()
    with (output / (name + '.log')).open('w') as stream:
        process = subprocess.Popen(argv, env=env, stdout=stream,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        active.add(process.pid)
        timed_out = False
        try:
            code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            stop(process.pid)
            code = process.wait(timeout=10)
        finally:
            if stop(process.pid):
                active.discard(process.pid)
    status['stages'].append({'name': name, 'argv': argv, 'exit_code': code, 'timed_out': timed_out,
                            'elapsed_seconds': time.monotonic() - started})
    assert code == 0 and not timed_out, name + ' failed; no retry'


try:
    assert git('rev-parse', 'HEAD') == PIN
    assert not git('status', '--porcelain=v1')
    (output / 'environment.json').write_text(json.dumps({
        'head': PIN, 'tree': git('rev-parse', 'HEAD^{tree}'),
        'GOMAXPROCS': 2, 'GOFLAGS': env['GOFLAGS'], 'CGO_ENABLED': 1,
        'prek': '0.5.7', 'golangci_lint': 'v2.14.0'}, indent=2) + '\n')
    call('venv', ['python3', '-m', 'venv', str(work / 'venv')], 120)
    call('install-prek', [str(work / 'venv/bin/pip'), 'install', 'prek==0.5.7'], 300)
    call('install-linter', ['go', 'install', 'github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0'], 1800)
    call('prepush', ['prek', 'run', '--all-files', '--stage', 'pre-push'], 2400)
except BaseException as error:
    status['error'] = type(error).__name__ + ': ' + str(error)
finally:
    for pgid in list(active):
        if stop(pgid):
            active.discard(pgid)
    status['owned_processes_stopped'] = not active
    for owned in (work, tool):
        if owned.exists():
            try:
                for directory, _, _ in os.walk(owned):
                    os.chmod(directory, 0o700)
                shutil.rmtree(owned)
            except OSError as error:
                status['cleanup_error'] = type(error).__name__ + ': ' + str(error)
    status['cleanup'] = not work.exists() and not tool.exists()
    status['source_frozen_after'] = git('rev-parse', 'HEAD') == PIN and not git('status', '--porcelain=v1')
    (output / 'statuses.json').write_text(json.dumps(status, indent=2) + '\n')
assert 'error' not in status and status['cleanup'] and status['source_frozen_after'] and status['owned_processes_stopped']
