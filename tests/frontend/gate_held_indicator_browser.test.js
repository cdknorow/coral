// Gate phase 2 (#3578): an agent's tooltip says how many automated messages Coral holds for it
// (held_inputs from the live list), as a safe count; no line when none, and junk never renders.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        const tip = held => ev(`(() => {
            const div = document.createElement('div');
            div.innerHTML = window.buildSessionTooltip({ name: 'claude-x', agent_type: 'claude', session_id: 'x', status: 'idle', held_inputs: ${JSON.stringify(held)} });
            const cell = div.querySelector('.tt-held');
            return { text: cell ? cell.textContent : null, imgs: div.querySelectorAll('img').length };
        })()`);
        let r = await tip(2);
        check('two held: one line, plural', r.text === '2 automated messages held until the agent is free', JSON.stringify(r));
        r = await tip(1);
        check('one held: singular', r.text === '1 automated message held until the agent is free', JSON.stringify(r));
        for (const v of [0, -3, 'abc', null, '<img src=x onerror=alert(1)>']) {
            r = await tip(v);
            check(`no line for ${JSON.stringify(v)}`, r.text === null && r.imgs === 0, JSON.stringify(r));
        }
        r = await tip(5000);
        check('a huge count is capped', r.text && r.text.startsWith('999 '), JSON.stringify(r));
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
