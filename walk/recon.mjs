// The dashboard as a first-time person meets it, through headless Chrome over CDP (trial branch; not for landing).
import CDP from 'chrome-remote-interface';
import fs from 'node:fs';
import { spawn } from 'node:child_process';
const out = process.env.WALK_OUT;
const chromePath = ['C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe'].find(fs.existsSync);
const base = 'http://127.0.0.1:8450';
const chrome = spawn(chromePath, ['--remote-debugging-port=9222', '--user-data-dir=C:/walk/chrome-profile', '--headless=new', '--no-first-run', '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
let client;
for (let i = 0; i < 30 && !client; i++) { await new Promise(r => setTimeout(r, 1000)); try { client = await CDP({ port: 9222 }); } catch {} }
const { Page, Runtime, Input } = client;
await Page.enable();
let n = 100;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const ev = async (expr) => (await Runtime.evaluate({ expression: expr, returnByValue: true, awaitPromise: true })).result.value;
const shot = async (name) => { const r = await Page.captureScreenshot({ format: 'png' }); fs.writeFileSync(`${out}/${++n}-${name}.png`, Buffer.from(r.data, 'base64')); console.log('SHOT', n, name); };
const dump = async (label) => {
    const d = await ev(`(() => { const vis = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
      const dlg = [...document.querySelectorAll('dialog[open], [role=dialog], .modal, .modal-overlay')].filter(vis).map(e => e.innerText.replace(/\\s+/g, ' ').slice(0, 700));
      return JSON.stringify({ url: location.href, title: document.title, dialogs: dlg,
        buttons: [...document.querySelectorAll('button, [role=button], a, [data-on-click]')].filter(vis).map(e => (e.innerText || e.getAttribute('aria-label') || e.title || '').trim().replace(/\\s+/g, ' ').slice(0, 50)).filter(Boolean).slice(0, 60),
        inputs: [...document.querySelectorAll('input, select, textarea')].filter(vis).map(e => [e.type, e.name || e.id, e.placeholder || '', e.value ? 'v=' + String(e.value).slice(0, 40) : '', e.getAttribute('aria-label') || ''].join('|')).slice(0, 20),
        text: document.body.innerText.replace(/\\s+/g, ' ').slice(0, 1100) }); })()`);
    console.log('PAGE ' + label + ' ' + d);
};
// click the smallest visible element whose own text matches re
const clickText = async (re, scope = 'document') => {
    const pos = await ev(`(() => { const re = ${re}; const vis = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
      const els = [...${scope}.querySelectorAll('button, [role=button], a, [data-on-click], label, summary, li, div, span')].filter(e => vis(e) && re.test((e.innerText || '').trim().replace(/\\s+/g, ' ')) && (e.innerText || '').length < 140);
      els.sort((a, b) => a.innerText.length - b.innerText.length); const e = els[0]; if (!e) return null; e.scrollIntoView({ block: 'center' }); const r = e.getBoundingClientRect();
      return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2, t: e.innerText.trim().replace(/\\s+/g, ' ').slice(0, 60) }); })()`);
    if (!pos) { console.log('CLICK not found', re); return false; }
    const p = JSON.parse(pos);
    await Input.dispatchMouseEvent({ type: 'mousePressed', x: p.x, y: p.y, button: 'left', clickCount: 1 });
    await Input.dispatchMouseEvent({ type: 'mouseReleased', x: p.x, y: p.y, button: 'left', clickCount: 1 });
    console.log('CLICK', p.t);
    return true;
};
const typeInto = async (selector, text) => {
    await ev(`document.querySelector(${JSON.stringify(selector)}).focus()`);
    await Input.insertText({ text });
};
const step = async (name, fn) => { try { console.log('=== STEP ' + name); await fn(); } catch (e) { console.log('STEP FAILED ' + name + ': ' + e.message); } };

await step('1 first load', async () => {
    await Page.navigate({ url: base + '/' }); await sleep(3500);
    await shot('A1-create-password'); await dump('A1-create-password');
});
await step('2 create password', async () => {
    await typeInto('input[name=new-password], input[autocomplete=new-password]', 'walk-pass-Correct-Horse-9');
    await typeInto('input[name=confirm-password]', 'walk-pass-Correct-Horse-9');
    await shot('A2-password-typed');
    await clickText(/^Create password and open Coral$/); await sleep(4000);
    await shot('A3-home'); await dump('A3-home');
});
await step('3 launch agent dialog', async () => {
    await clickText(/Launch agent|New agent|Launch an agent/i); await sleep(2500);
    await shot('A4-launch-dialog'); await dump('A4-launch-dialog');
});
await step('4 launch the agent', async () => {
    await clickText(/^Launch$/); await sleep(5000);
    await shot('A5-agent'); await dump('A5-agent');
});
await step('5 home again, team', async () => {
    await Page.navigate({ url: base + '/' }); await sleep(3000);
    await dump('B1-home-again');
    await clickText(/Launch team|Team|Launch a team/i); await sleep(2500);
    await shot('B2-team-dialog'); await dump('B2-team-dialog');
});
await client.close(); chrome.kill();
