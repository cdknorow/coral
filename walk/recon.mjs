// The dashboard as a first-time person meets it, through headless Chrome over CDP (trial branch; not for landing).
import CDP from 'chrome-remote-interface';
import fs from 'node:fs';
import { spawn, spawnSync } from 'node:child_process';
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
// The screenshot hides the runner's user name, then puts everything back: the walk never launches from a path it changed.
const redact = async () => ev(`(() => { window.__undo = []; const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT); let t; while ((t = w.nextNode())) { if (t.nodeValue.includes('runneradmin')) { const o = t.nodeValue, n = t; window.__undo.push(() => { n.nodeValue = o; }); t.nodeValue = t.nodeValue.replaceAll('runneradmin', 'user-name'); } }
  document.querySelectorAll('input').forEach(i => { if (i.value && i.value.includes('runneradmin')) { const o = i.value, n = i; window.__undo.push(() => { n.value = o; }); i.value = i.value.replaceAll('runneradmin', 'user-name'); } }); return window.__undo.length; })()`);
const unredact = async () => ev(`(() => { (window.__undo || []).forEach(f => f()); window.__undo = []; return 1; })()`);
const shot = async (name) => { await redact(); const r = await Page.captureScreenshot({ format: 'png' }); await unredact(); fs.writeFileSync(`${out}/${++n}-${name}.png`, Buffer.from(r.data, 'base64')); console.log('SHOT', n, name); };
const dump = async (label) => {
    const d = await ev(`(() => { const vis = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
      const dlg = [...document.querySelectorAll('dialog[open], [role=dialog], .modal, .modal-overlay')].filter(vis).map(e => e.innerText.replace(/\\s+/g, ' ').slice(0, 700));
      const openDlg = [...document.querySelectorAll('dialog[open], [role=dialog], .modal, .modal-overlay')].filter(vis);
      const scope = openDlg.length ? openDlg[openDlg.length - 1] : document;
      return JSON.stringify({ url: location.href, title: document.title, dialogs: dlg,
        buttons: [...scope.querySelectorAll('button, [role=button], a, [data-on-click]')].filter(vis).map(e => (e.innerText || e.getAttribute('aria-label') || e.title || '').trim().replace(/\\s+/g, ' ').slice(0, 50)).filter(Boolean).slice(0, 60),
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
await step('3 launch agent', async () => {
    await clickText(/Launch agent One agent in a folder/); await sleep(2500);
    await shot('A4-launch-dialog'); await dump('A4-launch-dialog');
    if (await ev(`/Launch New Session/.test(document.body.innerText)`)) { await clickText(/AI Agent Launch Claude/); await sleep(2500); await shot('A4b-agent-form'); await dump('A4b-agent-form'); }
});
await step('4 launch the agent', async () => {
    await clickText(/^(Launch|Launch Agent|Start|Launch agent)$/); await sleep(6000);
    await shot('A5-after-launch'); await dump('A5-after-launch');
});
await step('5 the agent: chat and terminal', async () => {
    await sleep(3000); await shot('A6-agent-chat'); await dump('A6-agent-chat');
    await clickText(/^terminal$/); await sleep(3500); await shot('A7-agent-terminal'); await dump('A7-agent-terminal');
    const t = await ev(`(document.querySelector('.xterm, .terminal, #terminal-container') || document.body).innerText.replace(/\\s+/g, ' ').slice(0, 600)`);
    console.log('TERMINAL TEXT ' + t);
});
await step('6 Browse in the launch dialog (a folder picker, as a person would see)', async () => {
    await Page.navigate({ url: base + '/' }); await sleep(3000);
    await clickText(/Launch agent One agent in a folder/); await sleep(2000);
    await clickText(/^Browse$/); await sleep(2500);
    await shot('A8-browse'); await dump('A8-browse');
    const r = spawnSync('pwsh', ['-NoProfile', '-Command', ". ./walk/common.ps1; Shot 'A8-browse-desktop'"], { encoding: 'utf8' }); console.log(r.stdout);
    await clickText(/^Cancel$/); await sleep(800);
});
await step('7 team: name it, launch it', async () => {
    await Page.navigate({ url: base + '/' }); await sleep(3000);
    await clickText(/Launch team Agents with roles/); await sleep(2500);
    await clickText(/^Launch Team$/); await sleep(2000);
    await shot('B1-team-no-name'); await dump('B1-team-no-name');
    await typeInto('#team-board-name', 'first-team'); await sleep(500);
    await shot('B2-team-named');
    await clickText(/^Launch Team$/); await sleep(10000);
    await shot('B3-after-team-launch'); await dump('B3-after-team-launch');
});
await step('8 the team after launch', async () => {
    await sleep(6000);
    await shot('B4-team-later'); await dump('B4-team-later');
    await clickText(/^Orchestrator$/); await sleep(3500);
    await shot('B5-orchestrator'); await dump('B5-orchestrator');
});
await step('9 the team view', async () => {
    await clickText(/Open team view for first-team/); await sleep(3500);
    await shot('B6-team-view'); await dump('B6-team-view');
});
await client.close(); chrome.kill();
