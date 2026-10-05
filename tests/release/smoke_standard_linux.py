#!/usr/bin/env python3
"""Run shipped Linux binaries on native amd64 in scratch, with no shared libs."""
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
import uuid


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=60).stdout.strip()


package = pathlib.Path(sys.argv[1]).resolve()
architecture = run('docker', 'info', '--format', '{{.Architecture}}')
if architecture not in ('x86_64', 'amd64'):
    raise SystemExit('Native amd64 Docker is required; emulation is not acceptance evidence')
name = 'coral-standard-smoke-' + uuid.uuid4().hex[:10]
commands = ('coral', 'launch-coral', 'coral-board', 'coral-agent',
            'coral-hook-agentic-state', 'coral-hook-message-check',
            'coral-hook-session-start', 'coral-hook-task-sync')
with tempfile.TemporaryDirectory(prefix='coral-standard-smoke-') as directory:
    context = pathlib.Path(directory)
    (context / 'bin').mkdir()
    for command in commands:
        shutil.copy2(package / command, context / 'bin' / command)
    (context / 'home' / '.coral').mkdir(parents=True)
    # Isolated first-run fixture; no user's settings or credentials are used.
    (context / 'home' / '.coral' / '.eula-accepted').write_text('accepted\n')
    (context / 'Dockerfile').write_text(
        'FROM scratch\nCOPY bin/ /usr/local/bin/\nCOPY home/ /data/\n'
        'ENV HOME=/data CORAL_DATA_DIR=/data CORAL_DIR=/data CORAL_SESSION_NAME=standard-smoke\n'
        'ENV CORAL_TELEMETRY_DISABLED=1\n'
        'ENTRYPOINT ["/usr/local/bin/coral"]\n')
    try:
        run('docker', 'build', '--platform', 'linux/amd64', '-t', name, directory)
        run('docker', 'run', '-d', '--network', 'none', '--platform', 'linux/amd64',
            '--name', name, '--memory', '512m', '--cpus', '2', name,
            '--home', '/data', '--host', '127.0.0.1', '--port', '8420',
            '--backend', 'pty', '--no-browser')

        def board(*args):
            return run('docker', 'exec', name, '/usr/local/bin/coral-board', *args)

        for attempt in range(100):
            try:
                board('join', 'standard-smoke', '--as', 'Tester')
                break
            except subprocess.CalledProcessError:
                state = json.loads(run('docker', 'inspect', name))[0]['State']
                if not state['Running']:
                    raise RuntimeError('Server exited: ' + json.dumps(state))
                time.sleep(.2)
        else:
            raise RuntimeError('Server board API did not become ready')
        print('PASS: native Linux server starts without any shared libraries', flush=True)
        board('post', 'standard no-library smoke message')
        assert 'standard no-library smoke message' in board('read', '--id', '1')
        print('PASS: board join, post and explicit message read', flush=True)
        for _ in range(3):
            board('read')
        print('PASS: repeated coral-board read exits normally', flush=True)
    except Exception:
        # Do not print generated API keys from the temporary startup log.
        logs = subprocess.run(['docker', 'logs', name], capture_output=True, text=True)
        for line in (logs.stdout + logs.stderr).splitlines():
            if not any(word in line.lower() for word in ('key', 'token', 'secret')):
                print(line, file=sys.stderr)
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['docker', 'image', 'rm', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
