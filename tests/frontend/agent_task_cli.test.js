// coral-agent task CLI, end to end: a real terminal agent on the test server
// claims and completes the tasks the operator gave it (agents not on a board).
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const PORT = new URL(BASE).port;
// Built on the fly unless CORAL_AGENT_BIN points at a coral-agent binary.
let BIN = process.env.CORAL_AGENT_BIN;
if (!BIN) {
  BIN = path.join(os.tmpdir(), `coral-agent-test-${process.pid}`);
  execFileSync('go', ['build', '-o', BIN, './cmd/coral-agent/'], { cwd: path.join(__dirname, '..', '..', 'coral-go'), stdio: 'inherit' });
}
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const json = async (method, p, body) => { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { status: r.status, data: await r.json().catch(() => null) }; };

// Run coral-agent as the test agent: its identity comes from CORAL_SESSION_NAME
// (TMUX unset so the caller's own tmux session is not picked up).
function agent(sessionName, ...args) {
  const env = { ...process.env, CORAL_URL: BASE, CORAL_SESSION_NAME: sessionName };
  delete env.TMUX; delete env.CORAL_PORT;
  try { return { code: 0, out: execFileSync(BIN, args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }) }; }
  catch (e) { return { code: e.status, out: (e.stdout || '') + (e.stderr || '') }; }
}

(async () => {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-agent-e2e-'));
  const launch = await json('POST', '/api/sessions/launch', { working_dir: work, agent_type: 'terminal', display_name: 'E2E Agent' });
  check('launch a terminal agent', launch.status === 200 && launch.data && launch.data.session_id, JSON.stringify(launch.data).slice(0, 200));
  const sid = launch.data.session_id;
  const name = launch.data.name || launch.data.agent_name || path.basename(work);
  const sessionName = launch.data.session_name || `terminal-${sid}`;

  const t1 = await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/tasks`, { title: 'Add a battle log', session_id: sid });
  const t2 = await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/tasks`, { title: 'Write engine tests', body: 'Cover ties and forfeits.', session_id: sid });
  check('the operator creates two tasks', t1.status === 200 && t2.status === 200, `${t1.status} ${t2.status}`);

  let r = agent(sessionName, 'task', 'list');
  check('coral-agent task list shows both as pending', r.code === 0 && /#\d+\s+\[pending\]\s+Add a battle log/.test(r.out) && /\[pending\]\s+Write engine tests/.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('claim takes the first task', r.code === 0 && new RegExp(`Claimed task #${t1.data.id}: Add a battle log`).test(r.out) && new RegExp(`coral-agent task complete ${t1.data.id}`).test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'current');
  check('current shows the claimed task', r.code === 0 && /in progress\): Add a battle log/.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('the next claim shows the details', r.code === 0 && /Write engine tests/.test(r.out) && /Cover ties and forfeits\./.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'complete', String(t1.data.id));
  check('complete marks it done', r.code === 0 && new RegExp(`Task #${t1.data.id} completed`).test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('with nothing pending, claim says so and exits non-zero', r.code !== 0 && /No pending tasks/.test(r.out), r.out.trim());
  const list = await json('GET', `/api/sessions/live/${encodeURIComponent(name)}/tasks?session_id=${sid}`);
  const byId = Object.fromEntries((list.data || []).map(t => [t.id, t.completed]));
  check('the dashboard sees done / in progress', byId[t1.data.id] === 1 && byId[t2.data.id] === 2, JSON.stringify(byId));
  r = agent('terminal-00000000-0000-0000-0000-000000000000', 'task', 'claim');
  check('an unknown session is refused', r.code !== 0 && /Unknown session/.test(r.out), r.out.trim());

  await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`, { agent_type: 'terminal', session_id: sid });
  const failed = results.filter(x => x === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
