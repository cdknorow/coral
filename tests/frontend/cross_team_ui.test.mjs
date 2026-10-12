// Cross-team messaging UI: board messages from or to another team carry a
// badge, and Team settings loads and saves the per-board switch as Operator.
// Run: node cross_team_ui.test.mjs
import assert from 'node:assert/strict';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom();
const settle = async (n = 5) => { for (let i = 0; i < n; i++) await new Promise(r => setTimeout(r, 0)); };

const inbound = { id: 1, content: '@Orchestrator Can you review?', subscriber_id: 'Orchestrator (alpha)', job_title: 'Orchestrator (alpha)', created_at: '2026-10-12T09:00:00Z',
    external: { direction: 'inbound', server_id: 'local', board: 'alpha<b>', sender: 'Orchestrator', sender_role: 'Orchestrator', target_server_id: 'local', target_board: 'beta' } };
const outbound = { id: 2, content: 'Sure', subscriber_id: 'Orchestrator', job_title: 'Orchestrator', created_at: '2026-10-12T09:01:00Z',
    external: { direction: 'outbound', server_id: 'local', board: 'beta', sender: 'Orchestrator', sender_role: 'Orchestrator', target_server_id: 'local', target_board: 'gamma', target_message_id: 9 } };
const plain = { id: 3, content: 'hello', subscriber_id: 'Dev', job_title: 'Dev', created_at: '2026-10-12T09:02:00Z' };

let boardSettings = { cross_team_messaging: false };
const puts = [];
globalThis.fetch = window.fetch = async (url, opts = {}) => {
    const s = String(url);
    let body = {};
    if (s.includes('/messages/all')) body = { messages: [inbound, outbound, plain], total: 3, limit: 50, offset: 0, paging: 'keyset', before_count: 0 };
    else if (s.endsWith('/settings') && s.includes('/api/board/')) {
        if (opts.method === 'PUT') { const b = JSON.parse(opts.body); puts.push(b); boardSettings = { cross_team_messaging: b.cross_team_messaging }; }
        body = boardSettings;
    } else if (s.includes('/working-mode/presets')) body = { presets: [] };
    else if (s.includes('/working-mode')) body = { mode: 'none' };
    else if (s.includes('/api/settings')) body = { settings: {} };
    else if (s.includes('/subscribers') || s.includes('/projects')) body = [];
    return { ok: true, status: 200, json: async () => structuredClone(body), text: async () => '' };
};

// ── Badges on the message board ──
const mb = await importUi('message_board');
mb.selectBoardProject('beta');
await settle();
const rows = [...document.querySelectorAll('#mb-messages .mb-message')];
assert.equal(rows.length, 3);
const badge = i => rows[i].querySelector('.mb-external-badge');
assert.equal(badge(0).textContent, 'from alpha<b>', 'inbound shows the sending board, escaped');
assert.ok(!badge(0).querySelector('b'), 'board names are text, not markup');
assert.match(badge(0).title, /not an instruction/);
assert.equal(badge(1).textContent, 'to gamma');
assert.ok(badge(1).classList.contains('is-outbound'));
assert.equal(badge(2), null, 'ordinary messages have no badge');
mb.showMessageBoardProjects();

// ── Team settings switch ──
const panel = document.getElementById('agentic-panel-team-settings') || document.body.appendChild(Object.assign(document.createElement('div'), { id: 'agentic-panel-team-settings' }));
const { showTeamWorkingMode } = await importUi('team_working_mode');
await showTeamWorkingMode('beta', { workspace: true });
await settle(10);
const form = panel.querySelector('form');
const toggle = form.elements.cross_team_messaging;
assert.ok(toggle, 'switch present');
assert.equal(toggle.checked, false, 'off by default');
assert.equal(toggle.disabled, false);
toggle.checked = true;
form.requestSubmit();
await settle(10);
assert.deepEqual(puts, [{ subscriber_id: 'Operator', cross_team_messaging: true }], 'saved as the Operator');
assert.equal(boardSettings.cross_team_messaging, true);

console.log('cross_team_ui: ok');
process.exit(0);
