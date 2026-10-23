// The waiting step of "Connect to Coral Hub" (#6323): after Connect the dialog shows only "Waiting for you to approve", the code, the Open link and Cancel (the access question was
// answered before Connect and went with it), a Copy code button sits beside the code, and the code a person selects survives the status polls (the panel writes a text only when it changed).
// Desktop (1280) and phone (390), both themes, 4.5:1 contrast, 44 px copy button on a touch screen. SHOT_DIR=<dir> saves a picture of each state.
// The /api/cloud/* routes are scripted in the page (as editions_browser does). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const CODE = 'J3CD-G5X3';
const VERIFY = 'https://app.coralhub.dev/enroll?code=J3CD-G5X3';
// A scripted connector: window.__cloud is the status; window.__reads counts status reads. The 3 s poll is shortened to 0.3 s so the suite waits for the poll, not the clock.
const STUB = `
  { const realSetTimeout = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 3000 ? 300 : ms, ...rest); }
  window.__reads = 0; window.__copied = [];
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: (t) => { window.__copied.push(t); return Promise.resolve(); } } });
  window.__cloud = { state: 'enrolling', hub_url: 'https://app.coralhub.dev', configured: true, max_mode: 'launch', max_mode_saved: true, allow_hub_auto_accept: true, offer_at_launch: true, user_code: '${CODE}', verification_url: '${VERIFY}' };
  const realFetch = window.fetch.bind(window);
  const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } }));
  window.fetch = (url, opts = {}) => {
    const path = String(url).replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
    if (!path.startsWith('/api/cloud/')) return realFetch(url, opts);
    if (path === '/api/cloud/status') window.__reads++;
    if (path === '/api/cloud/connect/cancel') { Object.assign(window.__cloud, { state: 'idle', user_code: '', verification_url: '' }); return json({ ok: true }); }
    return json({ ...window.__cloud });
  };`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => {
        if (!process.env.SHOT_DIR) return;
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: nothing to wait for but a transition before the capture
        fs.writeFileSync(`${process.env.SHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const screen = () => ev(`(() => { const m = document.getElementById('editions-modal'); if (!m || m.style.display === 'none') return null;
        const q = s => m.querySelector(s); const vis = e => !!e && !e.hidden && e.getClientRects().length > 0;
        const copy = q('[data-cloud="copy-code"]'), cb = copy && copy.getBoundingClientRect(), code = q('[data-cloud="usercode"]').getBoundingClientRect();
        return { status: q('[data-cloud="status"]').textContent, code: q('[data-cloud="usercode"]').textContent, link: q('[data-cloud="link"]').getAttribute('href'), linkShown: vis(q('[data-cloud="link"]')), cancel: vis(q('[data-cloud="cancel"]')),
            radios: [...m.querySelectorAll('input[type=radio]')].filter(vis).length, boxes: [...m.querySelectorAll('input[type=checkbox]')].filter(vis).length, question: vis(q('[data-cloud="ask-row"]')), note: /change this any time/.test([...m.querySelectorAll('.editions-ask-note')].filter(vis).map(e => e.textContent).join(' ')),
            copy: copy ? { label: copy.getAttribute('aria-label'), icon: copy.querySelector('.material-icons').textContent, w: Math.round(cb.width), h: Math.round(cb.height), shown: vis(copy), besideCode: Math.abs((cb.top + cb.height / 2) - (code.top + code.height / 2)) < 20 && cb.left >= code.right - 1 } : null,
            said: q('[data-cloud="copy-code-said"]').textContent, inView: m.querySelector('.modal-content').getBoundingClientRect().bottom <= innerHeight + 1 }; })()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of [{ n: 'desktop-dark', w: 1280, h: 800, phone: false, theme: 'dark' }, { n: 'desktop-light', w: 1280, h: 800, phone: false, theme: 'light' }, { n: 'phone-dark', w: 390, h: 844, phone: true, theme: 'dark' }, { n: 'phone-light', w: 390, h: 844, phone: true, theme: 'light' }]) {
            const tag = view.n;
            console.log(`\n${tag}`);
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 2, mobile: view.phone });
            await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await setTheme(ev, view.theme);
            await ev(STUB);
            await ev(`import(${S} + 'editions.js').then(m => m.showHubSetup('connect')).then(() => true)`);
            await waitFor(`(() => { const u = document.querySelector('#editions-modal [data-cloud="usercode"]'); return !!u && u.textContent === ${JSON.stringify(CODE)}; })()`, 'the code', 8000);
            let s = await screen();
            check(`${tag}: it says it is waiting, shows the code and the Open link, and Cancel`, s.status === 'Waiting for you to approve…' && s.code === CODE && s.linkShown && s.link === VERIFY && s.cancel, JSON.stringify([s.status, s.code, s.link, s.cancel]));
            check(`${tag}: no access choices: no radio, no checkbox, not the question, not its note`, s.radios === 0 && s.boxes === 0 && !s.question && !s.note, JSON.stringify([s.radios, s.boxes, s.question, s.note]));
            check(`${tag}: a Copy code button sits beside the code`, s.copy && s.copy.shown && s.copy.label === 'Copy code' && s.copy.besideCode, JSON.stringify(s.copy));
            check(`${tag}: it is ${view.phone ? '44 px on a touch screen' : 'at least 32 px'}`, s.copy && s.copy.w >= (view.phone ? 44 : 32) && s.copy.h >= (view.phone ? 44 : 32), s.copy && `${s.copy.w}x${s.copy.h}`);
            check(`${tag}: the dialog fits the window`, s.inView);
            const au = await auditRegion(ev, ['#editions-modal']);
            check(`${tag}: 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, (au.fails || []).length === 0);
            await shot(`${tag}-waiting`);

            // copy: writes the code exactly as shown, says so, and goes back
            await ev(`document.querySelector('#editions-modal [data-cloud="copy-code"]').click(); true`);
            await waitFor(`document.querySelector('#editions-modal [data-cloud="copy-code"]').getAttribute('aria-label') === 'Copied'`, 'the Copied state', 3000);
            s = await screen();
            check(`${tag}: Copy writes exactly the code that is shown`, JSON.stringify(await ev('window.__copied')) === JSON.stringify([CODE]), JSON.stringify(await ev('window.__copied')));
            check(`${tag}: a brief Copied state (icon and name) and an announcement`, s.copy.icon === 'check' && s.copy.label === 'Copied' && s.said === 'Code copied', JSON.stringify([s.copy.icon, s.copy.label, s.said]));
            await shot(`${tag}-copied`);
            await waitFor(`document.querySelector('#editions-modal [data-cloud="copy-code"]').getAttribute('aria-label') === 'Copy code'`, 'back to Copy code', 4000);
            s = await screen();
            check(`${tag}: and it goes back to Copy code`, s.copy.icon === 'content_copy' && s.said === '');

            // a selection survives the polls: select the code by hand, let two more status reads happen, the selection and the very text node are still there
            await ev(`(() => { const u = document.querySelector('#editions-modal [data-cloud="usercode"]'); window.__node = u.firstChild; const r = document.createRange(); r.selectNodeContents(u); const g = getSelection(); g.removeAllRanges(); g.addRange(r); window.__r0 = window.__reads; return true; })()`);
            await waitFor(`window.__reads >= window.__r0 + 2`, 'two more polls', 6000);
            const sel = await ev(`(() => { const u = document.querySelector('#editions-modal [data-cloud="usercode"]'); return { text: getSelection().toString(), sameNode: u.firstChild === window.__node, inside: u.contains(getSelection().anchorNode), reads: window.__reads - window.__r0 }; })()`);
            check(`${tag}: a selected code is still selected after ${sel.reads} polls, and its text was not rewritten`, sel.text === CODE && sel.sameNode && sel.inside, JSON.stringify(sel));
            // the status line is the same: its text node is kept while the words do not change
            const same = await ev(`(() => { const st = document.querySelector('#editions-modal [data-cloud="status"]'); const n = st.firstChild; window.__r1 = window.__reads; return new Promise(res => { const t = setInterval(() => { if (window.__reads >= window.__r1 + 2) { clearInterval(t); res(st.firstChild === n); } }, 50); }); })()`);
            check(`${tag}: the status line is not rewritten by a poll either`, same === true);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} waiting step checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
