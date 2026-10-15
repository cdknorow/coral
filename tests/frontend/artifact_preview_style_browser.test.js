// #3593: an HTML artifact's preview shows its own inline <style> (and data: fonts and
// images), still in an empty sandbox: a <script> doesn't run, and a url() or @import to
// anywhere loads nothing (a counting server sees no request).
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const http = require('node:http');
const BASE = require('./test_base').BASE;

(async () => {
  const hits = [];
  const sink = http.createServer((req, res) => { hits.push(req.method + ' ' + req.url); res.writeHead(200, { 'Content-Type': 'text/css' }); res.end('p{color:rgb(9,9,9)}'); });
  await new Promise(r => sink.listen(0, '127.0.0.1', r));
  const sinkUrl = 'http://127.0.0.1:' + sink.address().port;
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime, Target } = client;
  // A sandboxed (opaque-origin) frame can run in a process of its own: attach to frames as
  // they're created, so the test can look inside the preview's frame.
  const frameSessions = [];
  client.on('Target.attachedToTarget', ({ sessionId, targetInfo }) => { if (targetInfo.type === 'iframe') frameSessions.push({ sessionId, targetInfo }); });
  await Target.setAutoAttach({ autoAttach: true, waitForDebuggerOnStart: false, flatten: true });
  const ev = async expression => {
    const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  };
  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await Page.loadEventFired();
    const uri = 'coral://artifacts/' + 'e'.repeat(64);
    const html = '<style>@import url("' + sinkUrl + '/import.css"); p.styled{color:rgb(1,2,3);font-family:"Q3593",monospace;background-image:url("' + sinkUrl + '/bg.png")}'
      + '@font-face{font-family:"Q3593";src:url("data:font/woff2;base64,AAAA")}</style>'
      + '<p class="styled">styled text</p><img src="' + sinkUrl + '/img.png">'
      + '<script>document.title="SCRIPT-RAN";parent.__pwn3593=1<\\/script>';
    // The same setup as artifact_preview_safety: a live session, and the preview's entry points.
    await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession={type:'live',session_id:'artifact-style-test'};await import((window.__coralStatic||'/static/')+'changed_files.js');})()`);
    const framed = await ev(`(async()=>{
      const m=await import((window.__coralStatic||'/static/')+'preview_pane.js'); m.closeAllTabs();
      window.fetch=async()=>new Response(${JSON.stringify(html)},{status:200,headers:{'Content-Type':'text/html','Content-Disposition':'inline; filename="page.html"'}});
      window.openFilePreview(${JSON.stringify(uri)});
      const end=Date.now()+5000; while(!document.querySelector('#preview-body iframe') && Date.now()<end) await new Promise(r=>setTimeout(r,20));
      return !!document.querySelector('#preview-body iframe');
    })()`);
    assert.equal(framed, true, 'the HTML artifact is shown in an iframe');
    await new Promise(r => setTimeout(r, 1500)); // sleep-ok: proves a negative (no load reaches the sink)

    // Read the framed document from inside it: the sandbox makes it opaque to the page, so
    // through CDP, in the frame itself (in the page's frame tree, or a frame target of its own).
    let inFrame;
    const { frameTree } = await Page.getFrameTree();
    const child = (frameTree.childFrames || []).map(c => c.frame).find(f => f.url.startsWith('about:'));
    if (child) {
      const { executionContextId } = await Page.createIsolatedWorld({ frameId: child.id, worldName: 'q3593' });
      inFrame = async expression => {
        const r = await Runtime.evaluate({ expression, contextId: executionContextId, returnByValue: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
      };
    } else {
      const target = frameSessions.find(f => f.targetInfo.url.startsWith('about:')) || frameSessions[frameSessions.length - 1];
      assert.ok(target, 'the preview iframe is there (frame tree or frame target)');
      const { executionContextId } = await client.send('Page.createIsolatedWorld', { frameId: target.targetInfo.targetId, worldName: 'q3593' }, target.sessionId);
      inFrame = async expression => {
        const r = await client.send('Runtime.evaluate', { expression, contextId: executionContextId, returnByValue: true }, target.sessionId);
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
      };
    }
    const style = await inFrame(`(()=>{const p=document.querySelector('p.styled');const cs=p&&getComputedStyle(p);return {found:!!p,color:cs&&cs.color,font:cs&&cs.fontFamily,title:document.title}})()`);
    const pwned = await ev('window.__pwn3593 === undefined');
    await ev(`(async()=>{(await import((window.__coralStatic||'/static/')+'preview_pane.js')).closeAllTabs()})()`);

    assert.equal(style.found, true, 'the artifact rendered in the frame');
    assert.equal(style.color, 'rgb(1, 2, 3)', 'its inline <style> applies (#3593)');
    assert.match(style.font, /Q3593/, 'its font-family rule applies');
    assert.notEqual(style.title, 'SCRIPT-RAN', 'no script ran in the frame');
    assert.equal(pwned, true, 'no script reached the page');
    assert.deepEqual(hits, [], 'no url(), @import or <img> request left the frame');
    console.log('PASS artifact preview: inline styles apply, nothing runs, nothing loads');
  } finally {
    sink.closeAllConnections();
    await new Promise(r => sink.close(r));
    await client.close();
  }
})().catch(e => { console.error(e); process.exit(1); });
