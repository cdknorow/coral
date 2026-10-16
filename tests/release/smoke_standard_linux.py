#!/usr/bin/env python3
"""Run shipped Linux binaries on their native architecture in scratch, with no shared libs.

usage: smoke_standard_linux.py PACKAGE_DIR [amd64|arm64]   (default amd64)
"""
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
arch = sys.argv[2] if len(sys.argv) > 2 else 'amd64'
native = {'amd64': ('x86_64', 'amd64'), 'arm64': ('aarch64', 'arm64')}
if arch not in native:
    raise SystemExit('architecture must be amd64 or arm64')
architecture = run('docker', 'info', '--format', '{{.Architecture}}')
if architecture not in native[arch]:
    raise SystemExit(f'Native {arch} Docker is required; emulation is not acceptance evidence')
platform = 'linux/' + arch
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
        # No agent-session variable: coral-board runs as a person in a terminal, so under strict
        # local auth (#3936) it signs as the Operator with the command-line token the server writes
        # in /data. An agent-marked caller with no session token is refused, by design.
        'ENV HOME=/data CORAL_DATA_DIR=/data CORAL_DIR=/data\n'
        'ENV CORAL_TELEMETRY_DISABLED=1\n'
        'ENTRYPOINT ["/usr/local/bin/coral"]\n')
    try:
        run('docker', 'build', '--platform', platform, '-t', name, directory)
        run('docker', 'run', '-d', '--network', 'none', '--platform', platform,
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
