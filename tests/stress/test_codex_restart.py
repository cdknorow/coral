#!/usr/bin/env python3
"""Exercise actual chat HTTP + bind CLI across harness-owned server restarts.

Uses synthetic native rollouts only: no provider processes or live transcripts.
The shell harness restarts the server between rotate and verify invocations.
"""
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid

MODE, BASE, ROOT, CLI = sys.argv[1:]
ROOT = Path(ROOT)
STATE = ROOT / 'codex-restart.json'
CODEX = Path(os.environ['CODEX_HOME'])


def rollout(thread, text, coral=None):
    path = CODEX / 'sessions/2026/10/02' / ('rollout-stress-' + thread + '.jsonl')
    path.parent.mkdir(parents=True, exist_ok=True)
    rows = [{'type': 'session_meta', 'payload': {'id': thread}}]
    if coral:
        rows.append({'type': 'response_item', 'payload': {'type': 'message', 'role': 'developer',
                     'content': [{'type': 'input_text', 'text': 'CORAL_SESSION_ID: ' + coral}]}})
    rows.append({'type': 'event_msg', 'payload': {'type': 'user_message', 'message': text}})
    path.write_text(''.join(json.dumps(row) + '\n' for row in rows))
    return path


def bind(sid, thread, success=True):
    result = subprocess.run([CLI, 'bind-codex', sid, thread], capture_output=True, text=True, timeout=10)
    assert (result.returncode == 0) == success, result.stdout + result.stderr


def chat(sid, after=0):
    query = urllib.parse.urlencode({'session_id': sid, 'agent_type': 'codex',
                                  'working_directory': str(ROOT), 'after': after, 'limit': 100})
    with urllib.request.urlopen(BASE + '/api/sessions/live/' + sid + '/chat?' + query, timeout=10) as response:
        return json.load(response)


def expect(sid, messages, after=0, total=None):
    result = chat(sid, after)
    actual = [m.get('content') for m in result['messages'] if m.get('type') == 'user']
    assert actual == messages, (sid, actual, messages)
    assert result['total'] == (len(messages) if total is None else total), result


if MODE == 'seed':
    state = {'sid': str(uuid.uuid4()), 'other': str(uuid.uuid4()), 'round': 0}
    rollout(str(uuid.uuid4()), 'old marked conversation', state['sid'])
    rollout(str(uuid.uuid4()), 'unrelated conversation', state['other'])
    # Prime the real server cache using the old marker discovery path.
    expect(state['sid'], ['old marked conversation'])
    expect(state['other'], ['unrelated conversation'])
    STATE.write_text(json.dumps(state))
else:
    state = json.loads(STATE.read_text())
    if MODE == 'rotate':
        state['round'] += 1
        thread = str(uuid.uuid4())
        state['text'] = 'current conversation round ' + str(state['round'])
        state['path'] = str(rollout(thread, state['text']))  # intentionally no Coral marker
        bind(state['sid'], thread)
        # An explicit link must replace an already cached, still-existing old file.
        expect(state['sid'], [state['text']])
        expect(state['other'], ['unrelated conversation'])
        invalid = str(uuid.uuid4())
        bad_path = rollout(invalid, 'must not be associated')
        bad_path.write_text(json.dumps({'type': 'session_meta', 'payload': {'id': thread}}) + '\n')
        bind(state['sid'], invalid, success=False)
        expect(state['sid'], [state['text']])
        STATE.write_text(json.dumps(state))
    elif MODE == 'verify':
        # Called after a real process restart, WITHOUT rebinding: persisted identity wins.
        expect(state['sid'], [state['text']])
        expect(state['other'], ['unrelated conversation'])
        with open(state['path'], 'a') as transcript:
            transcript.write(json.dumps({'type': 'event_msg', 'payload': {
                'type': 'user_message', 'message': 'append after restart'}}) + '\n')
        # Separate polling clients must all receive the same new message exactly once.
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            list(pool.map(lambda _: expect(state['sid'], ['append after restart'], after=1, total=2), range(4)))
        expect(state['sid'], [], after=2, total=2)
        expect(state['sid'], [state['text'], 'append after restart'])
    else:
        raise SystemExit('unknown mode: ' + MODE)
print('Codex restart ' + MODE + ': PASS')
