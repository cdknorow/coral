// The "auto-accept turned off" notice shows once per pause (#4521, a coral-hub report): the paused code is resent on every
// connect, so it came back on every reload. Each pause is remembered in this browser by server|session_id|paused_at: a pause
// toasts once, not again after a reload, not when its row was briefly absent or came without the code, and a new pause (a new
// paused_at) toasts again; the owner's own codes never toast; the list is capped (200, the oldest dropped); and without
// storage the page still shows each pause once. Driven with the session entries the feed delivers (noteAutoAcceptPauses).
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic || '/static/')`;
    const load = async () => {
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await l; await coralReady();
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
    };
    const row = (paused_at, extra = {}) => ({ session_id: 'pt-1', name: 'pt-1', display_name: 'Pause bot', agent_type: 'claude', server: 'local',
        auto_accept: false, auto_accept_paused_code: 'expired', auto_accept_paused_at: paused_at, ...extra });
    // Feeds the entries to the notice and returns how many "turned off" toasts it showed.
    const feed = async entries => ev(`(async () => { const before = [...document.querySelectorAll('.toast')].filter(t => /Auto-accept turned off/.test(t.textContent)).length;
        (await import(${S} + 'auto_accept.js')).noteAutoAcceptPauses(${JSON.stringify(entries)});
        return [...document.querySelectorAll('.toast')].filter(t => /Auto-accept turned off/.test(t.textContent)).length - before; })()`);
    const stored = () => ev(`(() => { const k = Object.keys(localStorage).find(k => k.startsWith('coral-auto-accept-seen')); return k ? JSON.parse(localStorage.getItem(k)) : []; })()`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await load();
        await ev(`Object.keys(localStorage).filter(k => k.startsWith('coral-auto-accept-seen')).forEach(k => localStorage.removeItem(k)); true`);

        console.log('A pause toasts once');
        assert.equal(await feed([row(1000)]), 1, 'the first time');
        assert.equal(await feed([row(1000)]), 0, 'not on the next feed update');
        assert.deepEqual(await stored(), ['local|pt-1|1000'], 'remembered by server|session|paused_at');

        console.log('Not again after a reload (the feed resends the code on connect)');
        await load();
        assert.equal(await feed([row(1000)]), 0, 'no toast after a reload');

        console.log('Not when the row was briefly absent, or came without the code');
        assert.equal(await feed([]), 0);
        assert.equal(await feed([row(1000)]), 0, 'back after being absent: no toast');
        assert.equal(await feed([{ session_id: 'pt-1', name: 'pt-1', server: 'local', auto_accept: false }]), 0, 'an entry without the auto-accept fields');
        assert.equal(await feed([row(1000)]), 0, 'and the full entry again: no toast');

        console.log('A new pause toasts again');
        assert.equal(await feed([row(2000)]), 1, 'a new paused_at');
        assert.equal(await feed([row(2000)]), 0);
        await load();
        assert.equal(await feed([row(2000)]), 0, 'and is remembered across a reload too');

        console.log("The owner's own codes never toast");
        assert.equal(await feed([row(3000, { auto_accept_paused_code: 'stopped_by_owner' })]), 0);

        console.log('Capped at 200, the oldest dropped');
        const many = Array.from({ length: 230 }, (_, i) => row(10_000 + i, { session_id: 'pt-many-' + i, name: 'm' + i }));
        assert.equal(await feed(many), 230);
        const list = await stored();
        assert.equal(list.length, 200, 'at most 200 kept');
        assert.equal(list[list.length - 1], 'local|pt-many-229|10229', 'the newest kept');
        assert.ok(!list.includes('local|pt-1|1000'), 'the oldest dropped');

        console.log('Without storage: each pause still once in this page');
        await load();
        await ev(`Storage.prototype.__set = Storage.prototype.setItem; Storage.prototype.__get = Storage.prototype.getItem;
            Storage.prototype.setItem = () => { throw new DOMException('quota', 'QuotaExceededError'); }; Storage.prototype.getItem = () => { throw new DOMException('denied', 'SecurityError'); }; true`);
        try {
            assert.equal(await feed([row(5000, { session_id: 'pt-nostore', name: 'ns' })]), 1, 'shown');
            assert.equal(await feed([row(5000, { session_id: 'pt-nostore', name: 'ns' })]), 0, 'once (the page remembers it)');
        } finally {
            await ev(`Storage.prototype.setItem = Storage.prototype.__set; Storage.prototype.getItem = Storage.prototype.__get; true`);
        }

        await ev(`Object.keys(localStorage).filter(k => k.startsWith('coral-auto-accept-seen')).forEach(k => localStorage.removeItem(k)); true`);
        console.log('auto_accept_pause_toast: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
