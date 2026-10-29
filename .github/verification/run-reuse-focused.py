import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

root = Path.cwd()
runner = Path(os.environ['RUNNER_TEMP'])
packet = json.loads((runner / 'reuse-focused-overlay.json').read_text())
expected = packet['base']
assert subprocess.check_output(['git', 'rev-parse', 'HEAD']).decode().strip() == expected
assert not subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=all']).strip()
evidence = runner / 'reuse-focused-evidence'
evidence.mkdir()
managed = Path(tempfile.mkdtemp(prefix='docbank-reuse-focused.', dir=runner))
backups = {}
env = os.environ.copy()
for key, name in [('TMPDIR', 'tmp'), ('GOTMPDIR', 'gotmp'), ('GOCACHE', 'cache'), ('GOMODCACHE', 'modcache')]:
    p = managed / name
    p.mkdir()
    env[key] = str(p)
env['GOMAXPROCS'] = '2'
env['GOFLAGS'] = '-p=2 -trimpath'
results = []
try:
    for name, item in packet['files'].items():
        relative = Path(name)
        assert not relative.is_absolute() and '..' not in relative.parts
        assert name.startswith(('internal/store/', 'internal/retrieval/', 'internal/processing/', 'docs/architecture/'))
        assert relative.suffix in ('.go', '.md')
        data = base64.b64decode(item['content'])
        assert hashlib.sha256(data).hexdigest() == item['sha256']
        destination = root / relative
        backups[name] = destination.read_bytes() if destination.exists() else None
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
    tags = packet.get('tags', 'fts5')
    assert tags in ('fts5', 'fts5 sqlite_trace')
    metadata = {'base': expected, 'mode': packet['mode'], 'tags': tags, 'files': {k: v['sha256'] for k, v in packet['files'].items()}}
    (evidence / 'source.json').write_text(json.dumps(metadata, indent=2))
    with (evidence / 'environment.txt').open('w') as f:
        for command in (['uname', '-smr'], ['lscpu'], ['go', 'version']):
            subprocess.run(command, env=env, stdout=f, check=True)
        f.write('GOMAXPROCS=2 GOFLAGS=-p=2 -trimpath\n')
    if packet['mode'] == 'red':
        runs = [('red', '1', ['./internal/store'], '^TestSemanticSearchSourceSingletonQueryGrowth$', tags, False)]
    else:
        assert packet['mode'] == 'green'
        expression = packet['run_regex']
        assert len(expression) < 2048
        packages = packet.get('packages', ['./internal/store'])
        assert packages and all(p in ('./internal/store', './internal/retrieval', './internal/processing') for p in packages)
        runs = [('default', '1', packages, expression, 'fts5', False), ('purego', '0', packages, expression, 'fts5', False)]
        if packet.get('trace'):
            runs.append(('execution-trace', '1', ['./internal/store'], '^TestSemanticSearchSourceSingletonQueryGrowth$', 'fts5 sqlite_trace', False))
        if packet.get('fuzz'):
            runs.append(('fuzz', '1', ['./internal/store'], '^$', 'fts5', True))
    fuzz_dir = root / 'internal/store/testdata/fuzz/FuzzSemanticSearchSourceSingletonParity'
    assert not fuzz_dir.exists()
    for name, cgo, packages, expression, run_tags, fuzz in runs:
        command = ['go', 'test', '-tags', run_tags, *packages, '-run', expression, '-count=1', '-parallel=' + ('1' if fuzz else '2'), '-timeout=20m', '-v']
        if fuzz:
            command += ['-fuzz', '^FuzzSemanticSearchSourceSingletonParity$', '-fuzztime=5s']
        run_env = env | {'CGO_ENABLED': cgo}
        log = evidence / (name + '.log')
        with log.open('w') as output:
            output.write('COMMAND=' + json.dumps(command) + '\nCGO_ENABLED=' + cgo + '\n')
            output.flush()
            process = subprocess.run(command, env=run_env, stdout=output, stderr=subprocess.STDOUT, timeout=1500)
        content = log.read_text()
        print(content, flush=True)
        result = {'name': name, 'go_exit': process.returncode}
        if packet['mode'] == 'red':
            match = re.search(r'executed SELECTs: small=(\d+) multi=(\d+)', content)
            assert process.returncode != 0 and match and 'source SQL executions must stay bounded as members grow' in content
            small, multi = map(int, match.groups())
            assert multi > small + 4
            result |= {'expected_red': True, 'small': small, 'multi': multi}
        else:
            assert process.returncode == 0
        results.append(result)
        (evidence / 'results.json').write_text(json.dumps(results, indent=2))
    (evidence / 'results.json').write_text(json.dumps(results, indent=2))
finally:
    fuzz_dir = root / 'internal/store/testdata/fuzz/FuzzSemanticSearchSourceSingletonParity'
    if fuzz_dir.exists():
        shutil.copytree(fuzz_dir, evidence / 'failing-fuzz-corpus')
        shutil.rmtree(fuzz_dir)
    for name, previous in backups.items():
        destination = root / name
        if previous is None:
            destination.unlink(missing_ok=True)
        else:
            destination.write_bytes(previous)
    subprocess.run(['chmod', '-R', 'u+w', str(managed)], check=True)
    shutil.rmtree(managed)
    assert not managed.exists()
    assert subprocess.check_output(['git', 'rev-parse', 'HEAD']).decode().strip() == expected
    assert not subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=all']).strip()
    (evidence / 'cleanup.txt').write_text('CLEANUP_VERIFIED_BASE_RESTORED\n')
    print('CLEANUP_VERIFIED_BASE_RESTORED', flush=True)
