// The keyboard-only census through Coral Hub at desktop width (#6188): cloud mode under the hub's CSP, a stubbed two-computer Hub (the one hub_history_board_nav_browser uses), at 1280 px: a real Tab
// walk (helpers/keyboard_census.js) reaches every visible control on the Hub's screens (Chats and a chat with its tabs, boards and a board, Servers, Inbox, Docs, the account menu, Settings, Launch), each
// stop shows focus, and the dialogs trap Tab, close on Escape and give focus back. KEYBOARD_REPORT=<file> writes the findings to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const { tap: tapSel, required } = require('./helpers/phone_targets');
const kb = require('./helpers/keyboard_census');
const { BASE } = require('./test_base');
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const { Fetch, Network } = client;
    const results = [];
    const found = [];
    const sc = async (name, setup, { expect } = {}) => {
        try { await setup(); if (expect) { await new Promise(r => setTimeout(r, 400)); if (!(await ev(expect))) throw new Error('the scene is not showing (' + expect.slice(0, 80) + ')'); } } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 300) }); return; } // sleep-ok: the scene's own transition settles
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: the scene's own transitions settle
        const r = await kb.walk(ev, client.Input);
        results.push({ name, stops: r.stops.length, path: r.stops.map(x => x.d).join(' > ') });
        found.push(...kb.problems(name, r));
        if (!r.stops.length) found.push(`${name}: Tab reached no control at all`);
    };
    const tap = sel => tapSel(ev, sel);
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }] });
        safePaused(client, async e => {
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const b = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (b.base64Encoded ? Buffer.from(b.body, 'base64').toString('utf8') : b.body).replace('"mode":"local"', '"mode":"cloud"');
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: (e.responseHeaders || []).filter(h => !/^content-(security-policy|length)$/i.test(h.name)).concat([{ name: 'Content-Security-Policy', value: CSP }]), body: Buffer.from(html).toString('base64') });
        });
  await Page.addScriptToEvaluateOnNewDocument({source:`(() => {
   window.__cspViolations=[];document.addEventListener('securitypolicyviolation',e=>window.__cspViolations.push(e.violatedDirective));
   const original=window.fetch; window.__remoteRequests=[];
   const computers=[{id:'m1',label:'Laptop',status:'online',online:true,access_mode:'view',local_mode:'launch',effective_mode:'view'},{id:'m2',label:'Desktop',status:'online',online:true,access_mode:'interact',local_mode:'launch',effective_mode:'interact'}];
   const json=x=>Promise.resolve(new Response(JSON.stringify(x),{headers:{'content-type':'application/json'}}));
   window.fetch=(u,o={})=>{
    const p=new URL(String(u),location.href); const m=(o.method||'GET').toUpperCase();
    if(p.pathname==='/auth/session')return json({authenticated:true,email:'tester@example.com',csrf:'test'});
    if(p.pathname==='/api/sessions/live')return json({sessions:[],servers:window.__noComputers?[]:computers});
    if(p.pathname==='/api/servers')return json({servers:computers});
    if(p.pathname==='/api/settings'&&m==='GET')return json({settings:{}});
    const match=p.pathname.match(/^\\/api\\/remote\\/([^/]+)(\\/api\\/.*)$/);
    if(!match)return original(u,o);
    const [_,server,path]=match;window.__remoteRequests.push({server,path,method:m,query:p.search});
    if(path==='/api/sessions/history' && server==='m1' && window.__holdHistory) return new Promise(resolve=>{window.__releaseHistory=()=>resolve(new Response(JSON.stringify({sessions:[{session_id:'same-chat',summary_title:'OLD LAPTOP RESULT'}],total:1}),{headers:{'content-type':'application/json'}}));});
    if(path==='/api/sessions/history')return json({sessions:[{session_id:'same-chat',summary_title:server==='m1'?'Laptop conversation':'Desktop conversation',source_type:'claude'},{session_id:'chat-2',summary_title:'Second conversation on '+server,source_type:'claude'},{session_id:'chat-3',summary_title:'Third conversation on '+server,source_type:'codex'}],total:3,page:1,page_size:50});
    if(path==='/api/sessions/history/search')return json({status:'complete',results:[{session_id:'same-chat',type:'agent',hits:[{role:'user',excerpt:'needle from '+server,locator:{session_id:'same-chat',message_index:0}}]}]});
    if(path==='/api/sessions/history/same-chat')return json({agent_type:'claude',messages:[{type:'user',content:'needle from '+server},{type:'assistant',text:'Scoped to this computer.'}],total:2,start:0});
    if(path.endsWith('/notes'))return json({notes_md:'Notes from '+server});
    if(path==='/api/token-usage')return json({totals:{}});
    if(path.includes('/changes'))return Promise.resolve(new Response('Changes from '+server));
    if(path==='/api/tags'||/\\/(tags|events|tasks|agent-notes|files)$/.test(path))return json([]);
    if(path.endsWith('/git'))return json({commits:[]});
    if(path==='/api/board/projects')return json([{project:'same-team',board_label:'Release team',message_count:1,subscriber_count:1}]);
    if(path.endsWith('/messages/all'))return json({messages:[{id:1,subscriber_id:'Operator',job_title:'Operator',content:'Board message from '+server,created_at:'2026-10-22T12:00:00Z'},{id:2,subscriber_id:'Operator',job_title:'Operator',content:'Second message from '+server,created_at:'2026-10-22T12:00:20Z'},{id:3,subscriber_id:'Operator',job_title:'Operator',content:'Third message from '+server,created_at:'2026-10-22T12:00:40Z'}],total:3,before_count:0});
    if(path.endsWith('/subscribers'))return json([{subscriber_id:'Operator',job_title:'Owner'}]);
    if(path.endsWith('/paused'))return json({paused:false});
    if(path.endsWith('/sleep-status'))return json({sleeping:false});
    return json({});
   };
  })();`});
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`, 'dashboard'); await ev(`window.__coralReady.startup('liveList','settings')`);
                const nav = async tab => { await ev(`window.switchNavTab('${tab}'); true`); };
        await sc('hub: home', async () => { await waitFor(`document.querySelectorAll('button, a[href]').length > 5`, 'home'); });
        await sc('hub: chats list', async () => { await nav('history'); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`, 'history'); });
        await sc('hub: chat transcript', async () => { await tap('#history-sessions-list [data-on-click="history-open"]'); await waitFor(`document.getElementById('history-messages').textContent.includes('needle from')`, 'chat'); });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`hub: chat tab ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await sc('hub: boards list', async () => { await nav('board'); await waitFor(`document.querySelectorAll('#mb-projects-ul [data-project]').length===2`, 'boards'); });
        await sc('hub: board', async () => { await ev(`document.querySelector('#mb-projects-ul [data-project="@m1/same-team"]').click();true`); await waitFor(`document.getElementById('mb-messages').textContent.includes('Board message from m1')`, 'board'); });
        await sc('hub: servers', async () => { await nav('servers'); await waitFor(`document.getElementById('servers-view').getClientRects().length > 0`, 'servers'); });
        await sc('hub: inbox', async () => { await nav('hub-inbox'); }, { expect: `document.getElementById('nav-select').textContent.includes('Inbox') || !!document.getElementById('nav-tab-hub-inbox')` });
        await sc('hub: docs', async () => { await nav('docs'); });
        await sc('hub: account menu', async () => { await tap('[aria-label="Account menu"]'); }, { expect: `document.querySelector('[aria-label="Account menu"]').getAttribute('aria-expanded') === 'true'` });
        await ev(`document.body.click(); true`);
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        for (const g of await ev(`[...document.querySelectorAll('#settings-group-select option')].filter(o => !o.hidden).map(o => o.value)`).catch(() => [])) await sc(`hub: settings ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); return true; })()`); await ev(`(() => { const p = document.getElementById('settings-group-select'); p.value = '${g}'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); });
        await ev(`window.hideSettingsModal(); true`);
        const tagVisibleModal = () => ev(`(() => { document.querySelectorAll('[data-census-box]').forEach(e => e.removeAttribute('data-census-box')); const m = [...document.querySelectorAll('dialog[open], .modal')].filter(e => e.getClientRects().length && getComputedStyle(e).display !== 'none').pop(); if (m) m.setAttribute('data-census-box', ''); return !!m; })()`);
        const dlg = async (name, { opener, open }) => { try { const bad = await kb.dialogCheck(ev, client.Input, { opener, box: '[data-census-box]', open: async () => { await open(); for (let i = 0; i < 30 && !(await tagVisibleModal()); i++) await new Promise(r => setTimeout(r, 100)); } }); results.push({ name, stops: 0, path: '' }); found.push(...bad.map(b => `${name}: ${b}`)); } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 300) }); } }; // sleep-ok: the dialog appears when its code has loaded
        await dlg('hub: dialog launch', { opener: '[aria-label="Account menu"]', open: () => ev(`(async () => { await window.showLaunchModal(); return true; })()`) });
        await dlg('hub: dialog settings', { opener: '[aria-label="Account menu"]', open: () => ev(`(async () => { await window.showSettingsModal(); return true; })()`) });
    } finally { await client.close(); }
    const bad = results.filter(r => r.error).map(r => `${r.name}: the scene did not open (${r.error})`).concat(found, required(results, ['hub: home', 'hub: chats list', 'hub: chat transcript', 'hub: chat tab commits', 'hub: boards list', 'hub: board', 'hub: servers', 'hub: inbox', 'hub: docs', 'hub: account menu', 'hub: dialog launch', 'hub: dialog settings']));
    if (process.env.KEYBOARD_REPORT) { require('node:fs').writeFileSync(process.env.KEYBOARD_REPORT, results.map(r => `${r.name}: ${r.error || r.stops + ' stops'}${process.env.KEYBOARD_PATHS ? '\n    ' + r.path : ''}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'keyboard-only problems on a desktop through the Hub:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} Hub scenes: Tab reaches every control and shows where it is`);
})().catch(e => { console.error(e); process.exit(1); });
