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
    metadata = {'base': expected, 'mode': packet['mode'], 'files': {k: v['sha256'] for k, v in packet['files'].items()}}
    (evidence / 'source.json').write_text(json.dumps(metadata, indent=2))
    with (evidence / 'environment.txt').open('w') as f:
        for command in (['uname', '-smr'], ['lscpu'], ['go', 'version']):
            subprocess.run(command, env=env, stdout=f, check=True)
        f.write('GOMAXPROCS=2 GOFLAGS=-p=2 -trimpath\n')
    if packet['mode'] == 'red':
        runs = [('red', '1', './internal/store', '^TestSemanticSearchSourceSingletonQueryGrowth$')]
    else:
        assert packet['mode'] == 'green'
        expression = packet['run_regex']
        assert len(expression) < 2048
        runs = [('default', '1', './internal/store', expression), ('purego', '0', './internal/store', expression)]
    for name, cgo, package, expression in runs:
        command = ['go', 'test', '-tags', 'fts5', package, '-run', expression, '-count=1', '-parallel=2', '-timeout=20m', '-v']
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
            match = re.search(r'prepared SELECTs: small=(\d+) multi=(\d+)', content)
            assert process.returncode != 0 and match and 'source SQL preparation must stay bounded as members grow' in content
            small, multi = map(int, match.groups())
            assert multi > small + 4
            result |= {'expected_red': True, 'small': small, 'multi': multi}
        else:
            assert process.returncode == 0
        results.append(result)
    (evidence / 'results.json').write_text(json.dumps(results, indent=2))
finally:
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
