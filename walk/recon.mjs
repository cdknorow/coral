// The dashboard as a first-time person meets it, through headless Chrome over CDP (trial branch; not for landing).
import CDP from 'chrome-remote-interface';
import fs from 'node:fs';
import { spawn } from 'node:child_process';
const out = process.env.WALK_OUT;
const chromePath = ['C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe'].find(fs.existsSync);
console.log('chrome at', chromePath);
const base = 'http://127.0.0.1:8450';
const chrome = spawn(chromePath, ['--remote-debugging-port=9222', '--user-data-dir=C:/walk/chrome-profile', '--headless=new', '--no-first-run', '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
let client;
for (let i = 0; i < 30 && !client; i++) { await new Promise(r => setTimeout(r, 1000)); try { client = await CDP({ port: 9222 }); } catch {} }
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
await Page.navigate({ url: base + '/' });
await new Promise(r => setTimeout(r, 4000));
await shot('browser-first-load'); await dump('browser-first-load');
await client.close(); chrome.kill();
