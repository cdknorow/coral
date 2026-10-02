#!/usr/bin/env python3
"""HTTP regression: board-only membership must survive a fresh agent restart."""
import argparse
import json
from pathlib import Path
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--workdir', required=True)
    parser.add_argument('--data-dir', required=True)
    args = parser.parse_args()

    def api(method, path, body=None):
        request = urllib.request.Request(
            args.base_url + path,
            data=None if body is None else json.dumps(body).encode(),
            headers={'Content-Type': 'application/json'}, method=method,
        )
        with urllib.request.urlopen(request, timeout=15) as response:
            result = json.load(response)
        if isinstance(result, dict) and result.get('error'):
            raise AssertionError(result['error'])
        return result

    def live_session(sid):
        # The live API is what determines sidebar team grouping. Wait for
        # discovery of the renamed terminal, not just the restart HTTP 200.
        for _ in range(40):
            rows = api('GET', '/api/sessions/live')
            row = next((row for row in rows if row['session_id'] == sid), None)
            if row is not None:
                return row
            time.sleep(0.25)
        raise AssertionError(f'replacement session {sid} never appeared')

    board = 'restart-membership-' + uuid.uuid4().hex[:12]
    current = None
    try:
        # Deliberately launch without board_name, then join via the board API
        # (the coral-board join path). A launch-team-only test misses this bug.
        current = api('POST', '/api/sessions/launch', {
            'agent_type': 'claude', 'working_dir': args.workdir,
            'display_name': 'UI',
        })
        sid = current['session_id']
        name = current['session_name']
        assert not live_session(sid).get('board_project'), 'fixture already has a board'
        original = api('POST', f'/api/board/{board}/subscribe', {
            'subscriber_id': 'UI', 'job_title': 'UI',
            'session_name': name, 'receive_mode': 'all',
        })
        assert live_session(sid)['board_project'] == board, 'join did not take effect'

        # A second restart also checks that the recovered association persists.
        for attempt in range(2):
            old_sid, old_name = sid, name
            current = api('POST', f'/api/sessions/live/{name}/restart', {
                'agent_type': 'claude', 'session_id': sid,
            })
            sid, name = current['session_id'], current['session_name']
            assert sid != old_sid and name != old_name, 'restart reused old identity'
            row = live_session(sid)
            assert row.get('board_project') == board, (
                f'restart {attempt + 1} lost team membership: '
                f'expected {board!r}, got {row.get("board_project")!r}'
            )
            assert row.get('board_job_title') == 'UI', 'restart lost board role'
            assert row.get('display_name') == 'UI', 'restart lost display name'
            subs = api('GET', f'/api/board/{board}/subscribers')
            assert len(subs) == 1, f'restart duplicated subscription: {subs}'
            sub = subs[0]
            assert sub['id'] == original['id'], 'restart replaced stable membership'
            assert sub['subscriber_id'] == 'UI', 'restart changed subscriber identity'
            assert sub['session_name'] == name, 'subscription still targets old terminal'
            assert sub['receive_mode'] == 'all', 'restart reset notification preferences'
            state = json.loads((Path(args.data_dir) / f'board_state_{name}.json').read_text())
            assert state['project'] == board and state['job_title'] == 'UI', (
                'coral-board CLI state lost board or role'
            )
        print('PASS: board-only membership, sidebar grouping, subscription target, '
              'role and CLI state survive two restarts')
    finally:
        if current and current.get('session_id') and current.get('session_name'):
            api('POST', f'/api/sessions/live/{current["session_name"]}/kill', {
                'agent_type': 'claude', 'session_id': current['session_id'],
            })
        api('DELETE', f'/api/board/{board}/subscribe', {'subscriber_id': 'UI'})


if __name__ == '__main__':
    main()
