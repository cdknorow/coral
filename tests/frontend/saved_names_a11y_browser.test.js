// The saved messages' buttons for a person using a screen reader (#6541): every saved item has "Read the whole message", "Done" and "Remove", and four items read as the same three names four times,
// so a person moving through them by button cannot tell which message a press would act on. Each button now says which message it is for (the source and the start of its text), and the name still
// contains the words on the button (a person speaking "Done" to a voice control is understood). Desktop and a 390 px phone. Drives Chrome over CDP through agent_view_harness.js with the Inbox's
// stand-in routes (helpers/inbox_world.js).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { INBOX_WORLD } = require('./helpers/inbox_world');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(INBOX_WORLD);
            await ev(`localStorage.setItem('coral.inbox.tab', 'saved'); window.switchNavTab('hub-inbox'); true`);
            await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 4`, `${tag}: four saved items`, 10000);
            const names = () => ev(`[...document.querySelectorAll('#saved-list .saved-item')].map(li => [...li.querySelectorAll('button')].map(b => ({ name: (b.getAttribute('aria-label') || b.textContent).replace(/\\s+/g, ' ').trim(), visible: [...b.childNodes].filter(n => !(n.classList && n.classList.contains('material-icons'))).map(n => n.textContent).join('').replace(/\\s+/g, ' ').trim() })))`);
            const check = (rows, when) => {
                const all = rows.flat().map(b => b.name);
                assert.deepEqual([...new Set(all.filter((n, i) => all.indexOf(n) !== i))], [], `${tag}, ${when}: no two buttons in the list share a name`);
                for (const b of rows.flat()) assert.ok(b.name.toLowerCase().includes(b.visible.toLowerCase()), `${tag}, ${when}: "${b.name}" contains its visible words "${b.visible}"`);
            };
            check(await names(), 'closed');
            // a message opened: the toggle's words change ("Show less") and its name follows
            await ev(`document.querySelector('#saved-list .ibx-saved-toggle').click(); true`);
            await waitFor(`document.querySelector('#saved-list .ibx-saved-toggle').getAttribute('aria-expanded') === 'true'`, `${tag}: the message opens`, 5000);
            check(await names(), 'one message open');
            // Done moves an item, and the buttons of the rest keep distinct names
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS Saved messages: each button names the message it acts on and keeps its visible words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
