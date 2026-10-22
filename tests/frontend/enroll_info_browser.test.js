// The hub connect page's "How access starts" control (#6065, from #6005): the dashboard's (i) icon button (a circle with the info icon), not a "?" glyph, in both themes, with the 44 px target on touch and at phone width with a mouse too, a visible
// focus ring, readable colours (4.5:1) and the note opening under it. The page is the real template (the hub renders it; its Go template actions are filled in here) on this server's static files.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const html = fs.readFileSync(path.join(__dirname, '../../coral-go/pkg/frontend/templates/cloud/enroll.html'), 'utf8')
    .replace(/\{\{-?\s*\$base :=[^}]*\}\}\s*/, '').replace(/\{\{\s*\$base\s*\}\}/g, '/static/');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    try {
        await Page.enable();
        for (const [name, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true], ['phone, fine pointer', 390, 844, false]]) {
            for (const theme of ['light', 'dark']) {
                await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
                await Emulation.setTouchEmulationEnabled({ enabled: mobile });
                await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
                await Page.navigate({ url: BASE + '/static/theme_init.js' }); await Page.loadEventFired();
                await ev(`(() => { const d = new DOMParser().parseFromString(${JSON.stringify(html.replace('<script type="module" src="/static/cloud/enroll.js"></script>', '').replace(/<script src="[^"]*theme_init.js"><\/script>/, ''))}, 'text/html');
                    document.head.innerHTML = d.head.innerHTML; document.body.className = d.body.className; document.body.innerHTML = d.body.innerHTML;
                    document.getElementById('entry').hidden = true; document.getElementById('review').hidden = false; return true; })()`);
                await waitFor(`document.fonts.ready.then(() => true) && !!document.querySelector('.cloud-more > summary .material-icons')`, `${name} ${theme}: the page`, 10000);
                await ev(`document.fonts.ready.then(() => true)`);
                await installAudit(ev); await setTheme(ev, theme);
                const s = await ev(`(() => { const sm = document.querySelector('.cloud-more > summary'), r = sm.getBoundingClientRect(), cs = getComputedStyle(sm), ic = sm.querySelector('.material-icons');
                    return { text: sm.textContent.trim(), name: sm.getAttribute('aria-label'), hidden: ic.getAttribute('aria-hidden'), w: Math.round(r.width), h: Math.round(r.height), radius: cs.borderRadius, glyph: getComputedStyle(ic).fontFamily.includes('Material Icons'), overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
                assert.equal(s.text, 'info', `${name} ${theme}: the (i) icon, not a "?"`); assert.equal(s.name, 'How access starts'); assert.equal(s.hidden, 'true'); assert.ok(s.glyph, 'the icon font is applied');
                assert.ok(/50%|px/.test(s.radius) && s.w === s.h, `${name} ${theme}: a circle ${JSON.stringify(s)}`);
                if (mobile || w < 768) assert.ok(s.w >= 44 && s.h >= 44, `${name}: a 44 px target ${JSON.stringify(s)}`); else assert.ok(s.w >= 20, 'visible size');
                assert.equal(s.overflow, 0, `${name}: no horizontal overflow`);
                const closed = await auditRegion(ev, ['.cloud-more'], { focus: ['.cloud-more > summary'] });
                assert.deepEqual(closed.fails, [], `${name} ${theme}: readable and a visible focus ring: ` + JSON.stringify(closed.fails));
                await ev(`document.querySelector('.cloud-more > summary').click(); true`);
                const o = await ev(`(() => { const d = document.querySelector('.cloud-more'), p = d.querySelector('p').getBoundingClientRect(); return { open: d.open, noteShown: p.height > 0 && p.width > 0 }; })()`);
                assert.deepEqual([o.open, o.noteShown], [true, true], `${name} ${theme}: the note opens under it`);
                const opened = await auditRegion(ev, ['.cloud-more'], { focus: ['.cloud-more > summary'] });
                assert.deepEqual(opened.fails, [], `${name} ${theme}: open state readable: ` + JSON.stringify(opened.fails));
                const dir = process.env.CORAL_SCREENSHOT_DIR;
                if (dir) { fs.mkdirSync(dir, { recursive: true }); await new Promise(r => setTimeout(r, 300)); fs.writeFileSync(`${dir}/enroll-${name}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); } // sleep-ok: a screenshot settles layout
            }
        }
        console.log('PASS the hub connect page uses the dashboard (i)');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
