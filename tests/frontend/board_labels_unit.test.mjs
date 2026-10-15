// Board ids in the dashboard (#3538, the dashboard half of #3536): keyLabel shows a board's label
// (noted from the payloads' board_label), with a short id only when two known boards share it;
// keys stay keys for comparing; the launch's duplicate-name refusal becomes join (board_id) or
// another (new_board), and cancel launches nothing.
import assert from 'node:assert/strict';
import { keyLabel, boardLabel, boardKeyName, noteBoardLabel, noteBoardLabels } from '../../coral-go/pkg/frontend/static/server_base.js';
import { retryPayload, candidateDetail, isBoardNameExists } from '../../coral-go/pkg/frontend/static/board_choice.js';

// A board made before ids: its key is its name, and its label too.
assert.equal(keyLabel('sprint'), 'sprint', 'no label known: the key name');
noteBoardLabels([{ board_project: 'sprint', board_label: 'sprint' }]);
assert.equal(keyLabel('sprint'), 'sprint');

// A new board: a b- id, shown by its label.
noteBoardLabels([{ board_project: 'b-7k3m9q2x4p', board_label: 'payments', server: null }]);
assert.equal(keyLabel('b-7k3m9q2x4p'), 'payments');
assert.equal(boardKeyName('b-7k3m9q2x4p'), 'b-7k3m9q2x4p', 'a key stays a key');

// Two boards with one label: the new one shows a short id; the older one (key = label) does not.
noteBoardLabel(null, 'b-q2w3e4r5t6', 'sprint');
assert.equal(keyLabel('b-q2w3e4r5t6'), 'sprint (b-q2w3)');
assert.equal(keyLabel('sprint'), 'sprint');
assert.equal(boardLabel('b-q2w3e4r5t6'), 'sprint', 'boardLabel has no short id (the rename field)');

// Remote boards are keyed by server: the same id on two servers keeps two labels.
noteBoardLabels([{ project: 'b-7k3m9q2x4p', board_label: 'infra' }], 'project', 'm2');
assert.equal(keyLabel('@m2/b-7k3m9q2x4p'), 'infra');
assert.equal(keyLabel('b-7k3m9q2x4p'), 'payments');

// A rename moves the label; the short id goes when the clash does.
noteBoardLabel(null, 'b-q2w3e4r5t6', 'sprint-2');
assert.equal(keyLabel('b-q2w3e4r5t6'), 'sprint-2');
assert.equal(keyLabel('sprint'), 'sprint');

// A row without a label (an older Coral) leaves what is known; never an empty label.
noteBoardLabels([{ board_project: 'b-7k3m9q2x4p' }, { board_project: 'b-7k3m9q2x4p', board_label: '' }]);
assert.equal(keyLabel('b-7k3m9q2x4p'), 'payments');

// The 409 and the retry.
const res409 = { ok: false, status: 409, data: { code: 'board_name_exists', candidates: [{ board_id: 'sprint', board_label: 'sprint', created_at: '2026-10-12T10:00:00Z', members: 4 }] } };
assert.equal(isBoardNameExists(res409), true);
assert.equal(isBoardNameExists({ ok: false, status: 409, data: { code: 'ambiguous_board' } }), false);
assert.equal(isBoardNameExists({ ok: false, status: 400, data: { code: 'board_name_exists' } }), false);
const body = { working_dir: '/w', agent_type: 'claude', board_name: 'sprint' };
assert.deepEqual(JSON.parse(JSON.stringify(retryPayload(body, { board_id: 'sprint' }))), { working_dir: '/w', agent_type: 'claude', board_name: 'sprint', board_id: 'sprint' }, 'join: its id');
assert.deepEqual(JSON.parse(JSON.stringify(retryPayload(body, { new_board: true }))), { working_dir: '/w', agent_type: 'claude', board_name: 'sprint', new_board: true }, 'another: new_board');
assert.equal(retryPayload(body, null), null, 'cancel: no launch');
assert.match(candidateDetail(res409.data.candidates[0], 'en-US'), /^created Oct 1[12](, 2026)?, 4 members$/);
assert.equal(candidateDetail({ members: 1 }, 'en-US'), '1 member');
assert.equal(candidateDetail({ created_at: 'nonsense' }, 'en-US'), '');

console.log('board_labels_unit: ok');
