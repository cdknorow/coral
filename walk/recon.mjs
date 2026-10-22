// The dashboard as a first-time person meets it, through headless Chrome over CDP (trial branch; not for landing).
import CDP from 'chrome-remote-interface';
import fs from 'node:fs';
const out = process.env.WALK_OUT;
const targets = await CDP.List({ port: 9333 });
console.log('targets', JSON.stringify(targets.map(t => [t.type, t.url, t.title])));
const target = targets.find(t => t.type === 'page' && /127\.0\.0\.1|localhost/.test(t.url)) || targets.find(t => t.type === 'page');
const client = await CDP({ port: 9333, target });
const { Page, Runtime } = client;
await Page.enable();
let n = 100;
const ev = async (expr) => (await Runtime.evaluate({ expression: expr, returnByValue: true, awaitPromise: true })).result.value;
const shot = async (name) => { const r = await Page.captureScreenshot({ format: 'png' }); fs.writeFileSync(`${out}/${++n}-${name}.png`, Buffer.from(r.data, 'base64')); console.log('shot', n, name); };
const dump = async (label) => {
    const d = await ev(`(() => { const vis = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
      return JSON.stringify({ url: location.href, title: document.title,
        buttons: [...document.querySelectorAll('button, [role=button], a.btn, .card, [data-on-click]')].filter(vis).map(e => (e.innerText || e.getAttribute('aria-label') || e.title || '').trim().slice(0, 60)).filter(Boolean).slice(0, 40),
        inputs: [...document.querySelectorAll('input, select, textarea')].filter(vis).map(e => [e.type, e.name || e.id, e.placeholder || '', e.getAttribute('aria-label') || ''].join('|')).slice(0, 20),
        text: document.body.innerText.replace(/\\s+/g, ' ').slice(0, 900) }); })()`);
    console.log('PAGE ' + label + ' ' + d);
};
await shot('app-first-load'); await dump('app-first-load');
await client.close();
