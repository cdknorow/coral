// A pulsing status dot costs no main-thread paints (#3945). The pulses used to animate box-shadow, which cannot run on the
// compositor: every frame repainted (about 60 paints a second per page with one pulsing dot and nothing else changing). The
// ring is now an ::after animated with transform and opacity only. A trace of an idle page with every pulsing kind on screen
// must show almost no Paint events, and each ring must really be animating (not just absent). Run against an isolated dev server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Tracing } = client;
    const ev = async x => { const r = await Runtime.evaluate({ expression: x, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails)); return r.result.value; };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a trace window
    const paints = async ms => {
        const events = []; Tracing.dataCollected(d => events.push(...d.value));
        const done = new Promise(r => Tracing.tracingComplete(r));
        await Tracing.start({ traceConfig: { includedCategories: ['devtools.timeline'] } });
        await sleep(ms); await Tracing.end(); await done;
        return events.filter(e => e.name === 'Paint').length;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await sleep(2000); // sleep-ok: the baseline trace needs startup work to be over; nothing marks that moment
        const idle = await paints(3000);
        await ev(`(() => { const c = document.createElement('div'); c.id = 'probe-dots'; c.style.cssText = 'position:fixed;left:8px;top:300px;display:flex;gap:8px;z-index:9999;align-items:center';
            const add = (tag, cls, text) => { const d = document.createElement(tag); d.className = cls; if (text) d.textContent = text; c.appendChild(d); };
            for (const s of ['working', 'waiting', 'stuck']) { add('span', 'session-dot ' + s); add('span', 'status-dot ' + s); }
            add('span', 'status-dot-working'); add('span', 'team-distill-dot'); add('span', 'badge waiting-badge', 'waiting');
            document.body.appendChild(c); return true; })()`);
        for (let i = 0; i < 200 && !(await ev(`[...document.querySelectorAll('#probe-dots > *')].every(e => e.getAnimations().length > 0)`)); i++) await sleep(20);   // every dot is animating
        const rings = await ev(`[...document.querySelectorAll('#probe-dots > *')].map(e => ({ cls: e.className, ring: getComputedStyle(e, '::after').animationName, pos: getComputedStyle(e).position, shadow: getComputedStyle(e).boxShadow }))`);
        const pulsing = await paints(3000);
        await ev(`document.getElementById('probe-dots').remove(); true`);
        for (const r of rings) {
            assert.equal(r.ring, 'pulse-ring', `${r.cls}: its ring animates (${JSON.stringify(r)})`);
            assert.equal(r.shadow, 'none', `${r.cls}: no box-shadow is animated on the dot (${r.shadow})`);
        }
        assert.ok(pulsing <= idle + 15, `nine pulsing dots add no per-frame paints: ${pulsing} in 3 s (idle page: ${idle}); the box-shadow pulses made about 180 a second`);
        console.log(`PASS pulsing dots paint ${pulsing} times in 3 s (idle page ${idle}); ${rings.length} rings animate on the compositor`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
