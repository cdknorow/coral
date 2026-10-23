// The reminder dialog (#6329), which replaces window.prompt (not available in the native app): the instruction and how often, the checks said next to the field, a refusal from the server said
// in the dialog, the keyboard (Enter in the minutes box starts it, Ctrl+Enter in the instruction box does, Enter there adds a line, Escape closes), focus in and back, 44 px controls on a touch screen,
// 4.5:1 contrast, desktop and 390 px, both themes. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server. SHOT_DIR=<dir> saves a picture of each state.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => {
        if (!process.env.SHOT_DIR) return;
        await new Promise(r => setTimeout(r, 250)); // sleep-ok: nothing to wait for but a transition before the capture
        fs.writeFileSync(`${process.env.SHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const press = async (key, code, vk, modifiers = 0) => {
        const base = { key, code, windowsVirtualKeyCode: vk, modifiers };
        await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(key === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
    };
    const state = () => ev(`(() => { const d = document.getElementById('reminder-dialog'); if (!d) return null; const q = s => d.querySelector(s); const vis = e => !!e && !e.hidden && e.getClientRects().length > 0;
        const r = e => { const b = e.getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; };
        const st = e => { const c = getComputedStyle(e); return [c.borderTopWidth, c.paddingLeft, c.borderTopLeftRadius, c.fontSize, c.fontFamily].join('|'); };
        return { fields: q('#reminder-message') ? [st(q('#reminder-message')), st(q('#reminder-minutes'))] : [st(q('#reminder-minutes'))], padL: parseFloat(getComputedStyle(q('#reminder-minutes')).paddingLeft), open: d.open, title: q('h3').textContent, intro: (q('.reminder-intro') || {}).textContent || '', message: !!q('#reminder-message'), msgVal: (q('#reminder-message') || {}).value, min: q('#reminder-minutes').value,
            focus: document.activeElement && document.activeElement.id, msgErr: vis(q('#reminder-message-error')) ? q('#reminder-message-error').textContent : '', minErr: vis(q('#reminder-minutes-error')) ? q('#reminder-minutes-error').textContent : '',
            srvErr: vis(q('.reminder-server-error')) ? q('.reminder-server-error').textContent : '', msgInvalid: (q('#reminder-message') || {}).getAttribute?.('aria-invalid') || null, minInvalid: q('#reminder-minutes').getAttribute('aria-invalid'),
            go: q('.reminder-go').textContent, goDisabled: q('.reminder-go').disabled, goH: r(q('.reminder-go')).h, cancelH: r(q('.reminder-cancel')).h, inputH: r(q('#reminder-minutes')).h,
            inView: d.getBoundingClientRect().left >= 0 && d.getBoundingClientRect().right <= innerWidth + 1 && d.getBoundingClientRect().bottom <= innerHeight + 1 }; })()`);
    const typeInto = async (id, text) => { await ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
    const open = (opts) => ev(`(async () => { window.__subs = []; window.__open = document.getElementById('opener'); const m = await import(${S} + 'reminder_dialog.js'); window.__res = ${JSON.stringify(opts.res || { ok: true })};
        window.__opener.focus(); m.openReminderDialog({ title: ${JSON.stringify(opts.title)}, intro: (every) => 'Sends this instruction to QA ' + every + ' until you stop it.', askMessage: ${!!opts.ask}, minutes: ${opts.minutes || 5}, submitLabel: ${JSON.stringify(opts.label || 'Start reminder')},
            onSubmit: async (v) => { window.__subs.push(v); return window.__res; } }); return true; })()`);

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
            await ev(`window.prompt = () => { throw new Error('window.prompt must not be used'); }; const b = document.createElement('button'); b.id = '__opener'; b.textContent = 'Remind agent'; b.style.cssText = 'position:fixed;left:8px;top:120px;z-index:5'; document.body.append(b); true`);

            // the agent's reminder: the instruction and the minutes
            await open({ title: 'Remind QA', ask: true });
            let s = await state();
            check(`${tag}: it opens as a dialog with its title, a sentence, the instruction box and minutes (5)`, s.open && s.title === 'Remind QA' && s.intro.length > 10 && s.message && s.min === '5', JSON.stringify([s.open, s.title, s.min]));
            check(`${tag}: the sentence says what happens, with the agent and the minutes`, s.intro === 'Sends this instruction to QA every 5 minutes until you stop it.', s.intro);
            check(`${tag}: the minutes box has the dashboard's field style, the same as the instruction box (border, padding, radius, font), and padding`, s.fields[0] === s.fields[1] && s.padL >= 8, JSON.stringify(s.fields));
            for (const [val, words] of [['2.5', 'every 2.5 minutes'], ['0.5', 'every 30 seconds'], ['1', 'every minute'], ['60', 'every hour'], ['120', 'every 2 hours'], ['abc', 'at the interval below']]) {
                await typeInto('reminder-minutes', val);
                const t = await ev(`document.querySelector('#reminder-dialog .reminder-intro').textContent`);
                check(`${tag}: typing ${val} minutes: "${words}"`, t === `Sends this instruction to QA ${words} until you stop it.`, t);
            }
            await typeInto('reminder-minutes', '5');
            check(`${tag}: focus starts in the instruction box`, s.focus === 'reminder-message', s.focus);
            check(`${tag}: it fits the window`, s.inView);
            if (view.phone) check(`${tag}: buttons and fields are 44 px on a touch screen (${s.goH}/${s.cancelH}/${s.inputH})`, s.goH >= 44 && s.cancelH >= 44 && s.inputH >= 44);
            const au = await auditRegion(ev, ['#reminder-dialog']);
            check(`${tag}: 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, (au.fails || []).length === 0);
            await shot(`${tag}-open`);

            // validation, said beside the field
            await ev(`document.querySelector('#reminder-dialog .reminder-go').click(); true`); await new Promise(r => setTimeout(r, 150)); // sleep-ok: the check runs in the click's own task
            s = await state();
            check(`${tag}: an empty instruction is refused beside its box, which is marked and has focus; nothing was sent`, /Write what the agent/.test(s.msgErr) && s.msgInvalid === 'true' && s.focus === 'reminder-message' && (await ev('window.__subs.length')) === 0, JSON.stringify([s.msgErr, s.msgInvalid, s.focus]));
            await shot(`${tag}-invalid`);
            await typeInto('reminder-message', 'Check the build and report');
            s = await state();
            check(`${tag}: typing clears that message`, s.msgErr === '' && s.msgInvalid === null);
            for (const [val, re] of [['', /Enter how many minutes/], ['abc', /Enter how many minutes/], ['0.2', /half a minute/], ['2000', /24 hours/]]) {
                await ev(`(() => { const e = document.getElementById('reminder-minutes'); e.value = ${JSON.stringify(val)}; return true; })()`);
                await ev(`document.querySelector('#reminder-dialog .reminder-go').click(); true`); await new Promise(r => setTimeout(r, 120)); // sleep-ok: the check runs in the click's own task
                s = await state();
                check(`${tag}: minutes "${val}" is refused with its reason, field marked, focus on it`, re.test(s.minErr) && s.minInvalid === 'true' && s.focus === 'reminder-minutes' && (await ev('window.__subs.length')) === 0, s.minErr);
            }

            // a refusal from the server stays in the dialog and the buttons come back
            await ev(`window.__res = { ok: false, error: 'This agent is asleep.' }; true`);
            await typeInto('reminder-minutes', '2.5');
            await press('Enter', 'Enter', 13); await new Promise(r => setTimeout(r, 250)); // sleep-ok: the request is a resolved promise
            s = await state();
            check(`${tag}: Enter in the minutes box starts it; a server refusal is said in the dialog, which stays open, buttons back`, s.open && s.srvErr === 'This agent is asleep.' && !s.goDisabled, JSON.stringify([s.open, s.srvErr, s.goDisabled]));
            const sent = await ev('window.__subs');
            check(`${tag}: what was sent is the instruction and 2.5 minutes as 150 seconds`, sent.length === 1 && sent[0].message === 'Check the build and report' && sent[0].seconds === 150, JSON.stringify(sent));

            // keyboard: Enter in the instruction adds a line; Ctrl+Enter sends; success closes and focus goes back
            await ev(`window.__res = { ok: true }; document.getElementById('reminder-message').focus(); true`);
            await press('Enter', 'Enter', 13); await new Promise(r => setTimeout(r, 150)); // sleep-ok: the check runs in the key's own task
            s = await state();
            check(`${tag}: Enter in the instruction box adds a line and sends nothing`, s.open && /\n$/.test(s.msgVal) && (await ev('window.__subs.length')) === 1, JSON.stringify(s.msgVal));
            await press('Enter', 'Enter', 13, 2); await new Promise(r => setTimeout(r, 300)); // sleep-ok: the request is a resolved promise
            s = await state();
            check(`${tag}: Ctrl+Enter in the instruction box starts it and the dialog closes`, !s.open && (await ev('window.__subs.length')) === 2);
            check(`${tag}: focus is back on the button that opened it`, (await ev(`document.activeElement && document.activeElement.id`)) === '__opener');

            // the task's reminder: minutes only; Escape closes
            await open({ title: 'Remind the assignee', ask: false, label: 'Start reminders', minutes: 10 });
            s = await state();
            check(`${tag}: a task's reminder has only the minutes (10), focus there, and its own button words`, s.open && !s.message && s.min === '10' && s.focus === 'reminder-minutes' && s.go === 'Start reminders', JSON.stringify([s.message, s.min, s.focus, s.go]));
            await press('Escape', 'Escape', 27); await new Promise(r => setTimeout(r, 250)); // sleep-ok: the dialog closes over a frame
            s = await state();
            check(`${tag}: Escape closes it, sends nothing and gives the focus back`, !s.open && (await ev('window.__subs.length')) === 0 && (await ev(`document.activeElement && document.activeElement.id`)) === '__opener');
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} reminder dialog checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
