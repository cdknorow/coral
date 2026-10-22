// The phone touch-target census through Coral Hub (#6130): cloud mode under the hub's CSP, a stubbed two-computer Hub (the one hub_history_board_nav_browser uses), at 390 px with touch: every visible control on
// the Hub's screens (nav menu, Chats and a chat with its tabs, boards and a board, Servers, Inbox, Docs, the account menu, Settings, Launch) is at least 44 x 44 px (helpers/phone_targets.js). The dashboard on this
// computer's own screens are phone_targets_census_browser's. PHONE_TARGETS_REPORT=<file> writes the report to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const { scene, problems } = require('./helpers/phone_targets');
const { BASE } = require('./test_base');
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const { Fetch, Network } = client;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, opts);
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
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
    if(path==='/api/sessions/history')return json({sessions:[{session_id:'same-chat',summary_title:server==='m1'?'Laptop conversation':'Desktop conversation',source_type:'claude'}],total:1,page:1,page_size:50});
    if(path==='/api/sessions/history/search')return json({status:'complete',results:[{session_id:'same-chat',type:'agent',hits:[{role:'user',excerpt:'needle from '+server,locator:{session_id:'same-chat',message_index:0}}]}]});
    if(path==='/api/sessions/history/same-chat')return json({agent_type:'claude',messages:[{type:'user',content:'needle from '+server},{type:'assistant',text:'Scoped to this computer.'}],total:2,start:0});
    if(path.endsWith('/notes'))return json({notes_md:'Notes from '+server});
    if(path==='/api/token-usage')return json({totals:{}});
    if(path.includes('/changes'))return Promise.resolve(new Response('Changes from '+server));
    if(path==='/api/tags'||/\\/(tags|events|tasks|agent-notes|files)$/.test(path))return json([]);
    if(path.endsWith('/git'))return json({commits:[]});
    if(path==='/api/board/projects')return json([{project:'same-team',board_label:'Release team',message_count:1,subscriber_count:1}]);
    if(path.endsWith('/messages/all'))return json({messages:[{id:1,subscriber_id:'Operator',job_title:'Operator',content:'Board message from '+server,created_at:'2026-10-22T12:00:00Z'}],total:1,before_count:0});
    if(path.endsWith('/subscribers'))return json([{subscriber_id:'Operator',job_title:'Owner'}]);
    if(path.endsWith('/paused'))return json({paused:false});
    if(path.endsWith('/sleep-status'))return json({sleeping:false});
    return json({});
   };
  })();`});
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`, 'dashboard'); await ev(`window.__coralReady.startup('liveList','settings')`);
        const menu = async () => { await ev(`document.getElementById('nav-select').click();true`); await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'phone nav'); };
        const pick = async arg => { await menu(); await ev(`document.querySelector('#nav-more-menu [data-arg="${arg}"]').click();true`); };
        await sc('hub: home', async () => { await waitFor(`document.querySelectorAll('button, a[href]').length > 5`, 'home'); }, { min: 4 });
        await sc('hub: nav menu', async () => { await menu(); }, { min: 10 });
        await sc('hub: chats list', async () => { await ev(`document.querySelector('#nav-more-menu [data-arg="history"]').click();true`); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`, 'history'); }, { min: 4 });
        await sc('hub: chat transcript', async () => { await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click();true`); await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m1')`, 'transcript'); }, { min: 10 });
        for (const t of ['events', 'tasks', 'notes', 'commits', 'changes']) await sc(`hub: chat tab ${t}`, async () => { await ev(`(() => { const b = [...document.querySelectorAll('.history-tab-btn')].find(x => x.dataset.tab === ${JSON.stringify(t)} || new RegExp('^' + ${JSON.stringify(t)}, 'i').test(x.textContent.trim())); if (b) b.click(); return !!b; })()`); }, { min: 10 });
        await ev(`document.querySelector('.mobile-history-back').click();true`);
        await sc('hub: boards list', async () => { await pick('board'); await waitFor(`document.querySelectorAll('#mb-projects-ul [data-project]').length===2`, 'boards'); }, { min: 4 });
        await sc('hub: board', async () => { await ev(`document.querySelector('#mb-projects-ul [data-project="@m1/same-team"]').click();true`); await waitFor(`document.getElementById('mb-messages').textContent.includes('Board message from m1')`, 'board'); }, { min: 10 });
        await sc('hub: servers', async () => { await pick('servers'); await waitFor(`document.getElementById('servers-view').getClientRects().length > 0`, 'servers'); }, { min: 8 });
        await sc('hub: inbox', async () => { await pick('hub-inbox'); }, { min: 3 });
        await sc('hub: docs', async () => { await pick('docs'); }, { min: 10 });
        await sc('hub: account menu', async () => { await ev(`(document.querySelector('[aria-label="Account menu"]') || {click(){}}).click(); true`); }, { min: 5 });
        await ev(`document.body.click(); true`);
        for (const g of ['general', 'agents', 'terminal', 'notifications', 'files', 'cleanup', 'network', 'security']) await sc(`hub: settings ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); document.getElementById('settings-tab-${g}').click(); return true; })()`); }, { min: 8 });
        await ev(`window.hideSettingsModal(); true`);
        await sc('hub: launch', async () => { await ev(`(async () => { await window.showLaunchModal(); return true; })()`); }, { min: 5 });
        await ev(`(window.hideLaunchModal || (() => {}))(); true`);
    } finally { await client.close(); }
    const bad = problems(results);
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.n + ' controls, ' + r.small.length + ' small'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'controls under 44 px on a phone through the Hub:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} Hub scenes: every control is at least 44 x 44 px`);
})().catch(e => { console.error(e); process.exit(1); });
