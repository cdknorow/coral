// #3718: a history transcript and the board's export come in pages, so through Coral Hub no JSON
// reply has to hold a whole transcript (one over 2 MiB is refused there). The page's fetch is
// stubbed for one session of 250 messages that honours ?limit and ?offset as the server does.
//   - The history view shows the newest 100 with "Load earlier messages"; each click puts the
//     previous page above, in order, keeping what was being read in place; at the start the button
//     goes.
//   - A server that doesn't page (no total) sends the whole transcript: all shown, no button.
//   - The export's helpers read a board, a live chat and a history in pages, every message once,
//     in order, no request asking for more than a page.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const STUB = `(() => {
  const real = window.fetch.bind(window);
  window.__requests = [];
  const page = (all, q) => {
    const limit = Number(q.get('limit')) || 0, offset = Math.max(0, Number(q.get('offset')) || 0);
    if (!limit) return { messages: all, total: all.length, start: 0 };
    const end = Math.max(all.length - offset, 0), start = Math.max(end - limit, 0);
    return { messages: all.slice(start, end), total: all.length, start };
  };
  const msgs = n => Array.from({ length: n }, (_, i) => ({ type: i % 2 ? 'assistant' : 'user', content: 'message ' + i, text: 'message ' + i }));
  window.__fake = { paged: msgs(250), old: msgs(130), chat: msgs(230), board: Array.from({ length: 450 }, (_, i) => ({ id: i + 1, content: 'post ' + i, created_at: '2026-10-16T00:00:00Z', job_title: 'Dev' })) };
  window.fetch = async (input, init) => {
    const u = new URL(typeof input === 'string' ? input : input.url, location.origin);
    const json = v => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });
    if (u.pathname === '/api/sessions/history/qa-paged') { window.__requests.push(u.search); return json({ session_id: 'qa-paged', agent_type: 'claude', ...page(window.__fake.paged, u.searchParams) }); }
    if (u.pathname === '/api/sessions/history/qa-old') { window.__requests.push('old' + u.search); return json({ session_id: 'qa-old', agent_type: 'claude', messages: window.__fake.old }); }
    if (u.pathname === '/api/sessions/live/qa-live/chat') { window.__requests.push('chat' + u.search); return json(page(window.__fake.chat, u.searchParams)); }
    if (/\\/messages\\/all$/.test(u.pathname) && u.searchParams.get('format') === 'dashboard' && window.__boardStub) {
      window.__requests.push('board' + u.search);
      const all = window.__fake.board, q = u.searchParams, limit = Number(q.get('limit'));
      let slice;
      if (q.has('before_id')) { const b = Number(q.get('before_id')); const older = all.filter(m => m.id < b); slice = older.slice(Math.max(0, older.length - limit)); }
      else { const off = Number(q.get('offset')) || 0; slice = off === 0 ? all.slice(all.length - limit) : all.slice(off, off + limit); }
      const beforeCount = slice.length ? all.filter(m => m.id < slice[0].id).length : 0;
      return json({ messages: slice, total: all.length, offset: 0, paging: 'keyset', before_count: beforeCount });
    }
    return real(input, init);
  };
})()`;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    const shown = () => ev(`[...document.querySelectorAll('#history-messages [data-message-index]')].map(e => Number(e.dataset.messageIndex))`);
    const range = a => a.length ? [Math.min(...a), Math.max(...a)] : null;
    const btn = cls => ev(`!!document.querySelector('#history-messages > .${cls}')`);
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        await ev(STUB);

        // ── The history view in pages ──────────────────────────────────────
        await ev(`window.selectHistorySession('qa-paged'); true`);
        await until(`document.querySelectorAll('#history-messages [data-message-index]').length > 0`, 'the transcript');
        let idx = [...new Set(await shown())];
        check('the history view shows the newest 100 messages', idx.length === 100 && JSON.stringify(range(idx)) === '[150,249]', JSON.stringify(range(idx)));
        check('with a "Load earlier messages" button, and no "later" one', (await btn('history-load-earlier')) && !(await btn('history-load-later')));
        check('it asked for one page', JSON.stringify(await ev(`window.__requests`)) === JSON.stringify(['?limit=100']), JSON.stringify(await ev(`window.__requests`)));

        const top0 = await ev(`(() => { const c = document.getElementById('history-messages'); const t = c.querySelector('[data-message-index="150"]'); return t.getBoundingClientRect().top; })()`);
        await ev(`document.querySelector('#history-messages > .history-load-earlier').click(); true`);
        await until(`document.querySelector('#history-messages [data-message-index="50"]') !== null`, 'the earlier page');
        idx = [...new Set(await shown())];
        const ordered = idx.every((v, i) => i === 0 || v > idx[i - 1]);
        check('"Load earlier" puts the previous page above, in order', idx.length === 200 && JSON.stringify(range(idx)) === '[50,249]' && ordered, JSON.stringify(range(idx)));
        const top1 = await ev(`document.querySelector('#history-messages [data-message-index="150"]').getBoundingClientRect().top`);
        check('and keeps what was being read in place', Math.abs(top1 - top0) < 4, JSON.stringify({ top0, top1 }));
        await ev(`document.querySelector('#history-messages > .history-load-earlier').click(); true`);
        await until(`document.querySelector('#history-messages [data-message-index="0"]') !== null`, 'the first page');
        idx = [...new Set(await shown())];
        check('the last page reaches the start, and the button goes', idx.length === 250 && !(await btn('history-load-earlier')), JSON.stringify(range(idx)));
        check('no request asked for more than a page', (await ev(`window.__requests`)).every(q => /limit=(100|50)\b/.test(q)), JSON.stringify(await ev(`window.__requests`)));

        // ── A server that doesn't page ─────────────────────────────────────
        await ev(`window.selectHistorySession('qa-old'); true`);
        await until(`document.querySelectorAll('#history-messages [data-message-index]').length === 130`, 'the old server’s transcript');
        check('a server that doesn’t page: the whole transcript, no button', (await shown()).length >= 130 && !(await btn('history-load-earlier')) && !(await btn('history-load-later')));

        // ── The export's helpers ───────────────────────────────────────────
        const exp = await ev(`(async () => {
            window.__boardStub = true; window.__requests = [];
            const mb = await import(${MOD} + 'message_board.js');
            const board = await mb.fetchAllBoardMessages('qa-board');
            const chat = await mb.fetchAllLiveChat({ name: 'qa-live', agent_type: 'claude', session_id: 'qa-live-sid', working_directory: '/r' });
            const hist = await mb.fetchAllHistory('qa-paged');
            window.__boardStub = false;
            return { board: board.map(m => m.id), chat: chat.messages.map(m => m.content), hist: hist.messages.map(m => m.content), reqs: window.__requests };
        })()`);
        const seq = (n, f) => Array.from({ length: n }, (_, i) => f(i));
        check('the export reads a whole board in pages, every message once, in order', JSON.stringify(exp.board) === JSON.stringify(seq(450, i => i + 1)), exp.board.length + ' messages');
        check('a whole live chat in pages', JSON.stringify(exp.chat) === JSON.stringify(seq(230, i => 'message ' + i)), exp.chat.length + ' messages');
        check('a whole history in pages', JSON.stringify(exp.hist) === JSON.stringify(seq(250, i => 'message ' + i)), exp.hist.length + ' messages');
        check('no export request asks for more than a page', exp.reqs.every(q => /limit=(100|200)\b/.test(q)) && exp.reqs.length > 6, JSON.stringify(exp.reqs.slice(0, 8)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
