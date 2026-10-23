// The phone touch-target census through Coral Hub (#6130): cloud mode under the hub's CSP, a stubbed two-computer Hub (the one hub_history_board_nav_browser uses), at 390 px with touch: every visible control on
// the Hub's screens (nav menu, Chats and a chat with its tabs, boards and a board, Servers, Inbox, Docs, the account menu, Settings, Launch) is at least 44 x 44 px (helpers/phone_targets.js). The dashboard on this
// computer's own screens are phone_targets_census_browser's. PHONE_TARGETS_REPORT=<file> writes the report to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const { scene, tap: tapSel, problems, required, useThemeSetter } = require('./helpers/phone_targets');
const { BASE } = require('./test_base');
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const { Fetch, Network } = client;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, opts);
    const tap = sel => tapSel(ev, sel);
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        // the hub's page follows the system theme (#6087): the contrast pass switches the emulated colour scheme, not the dashboard's own setting
        useThemeSetter(async (_ev, theme) => { await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] }); await new Promise(r => setTimeout(r, 300)); }); // sleep-ok: the page repaints for the new scheme
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }] });
        safePaused(client, async e => {
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const b = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (b.base64Encoded ? Buffer.from(b.body, 'base64').toString('utf8') : b.body).replace('"mode":"local"', '"mode":"cloud"');
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: (e.responseHeaders || []).filter(h => !/^content-(security-policy|length)$/i.test(h.name)).concat([{ name: 'Content-Security-Policy', value: CSP }]), body: Buffer.from(html).toString('base64') });
        });
  await Page.addScriptToEvaluateOnNewDocument({source:`(() => {
   window.__cspViolations=[];document.addEventListener('securitypolicyviolation',e=>window.__cspViolations.push(e.violatedDirective));
   try{localStorage.setItem('coral-group-by-server','true');}catch(e){} // the sidebar groups agents under their computer (#6241)
   const original=window.fetch; window.__remoteRequests=[];
   const computers=[{id:'m1',label:'Laptop',status:'online',online:true,access_mode:'view',local_mode:'launch',effective_mode:'view'},{id:'m2',label:'Desktop',status:'online',online:true,access_mode:'interact',local_mode:'launch',effective_mode:'interact'}];
   const json=x=>Promise.resolve(new Response(JSON.stringify(x),{headers:{'content-type':'application/json'}}));
   window.fetch=(u,o={})=>{
    const p=new URL(String(u),location.href); const m=(o.method||'GET').toUpperCase();
    if(p.pathname==='/auth/session')return json({authenticated:true,email:'tester@example.com',csrf:'test'});
    if(p.pathname==='/api/sessions/live')return json({sessions:window.__noComputers?[]:[['m1','Review agent'],['m1','Docs agent'],['m2','Build agent']].map(([server,name],i)=>({session_id:'hub-agent-'+i,name:'hub-agent-'+i,display_name:name,agent_type:'claude',working_directory:'/tmp/hub-agent-'+i,server,sleeping:false,archived:false,status:'idle'})),servers:window.__noComputers?[]:computers});
    if(p.pathname==='/api/servers')return json({servers:computers});
    if(p.pathname==='/api/account')return json({artifacts_tab:true,artifacts_frame:true});
    if(p.pathname==='/api/settings'&&m==='GET')return json({settings:{}});
    const match=p.pathname.match(/^\\/api\\/remote\\/([^/]+)(\\/api\\/.*)$/);
    if(!match)return original(u,o);
    const [_,server,path]=match;window.__remoteRequests.push({server,path,method:m,query:p.search});
    if(path==='/api/sessions/history' && server==='m1' && window.__holdHistory) return new Promise(resolve=>{window.__releaseHistory=()=>resolve(new Response(JSON.stringify({sessions:[{session_id:'same-chat',summary_title:'OLD LAPTOP RESULT'}],total:1}),{headers:{'content-type':'application/json'}}));});
    if(path==='/api/sessions/history')return json({sessions:[{session_id:'same-chat',summary_title:server==='m1'?'Laptop conversation':'Desktop conversation',source_type:'claude'},{session_id:'chat-2',summary_title:'Second conversation on '+server,source_type:'claude'},{session_id:'chat-3',summary_title:'Third conversation on '+server,source_type:'codex'}],total:3,page:1,page_size:50});
    if(path==='/api/sessions/history/search'&&/[?&]q=zzz/.test(p.search))return json({status:'complete',results:[]});
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
        const menu = async () => { await ev(`document.getElementById('nav-select').click();true`); await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'phone nav'); };
        const pick = async arg => { await menu(); await ev(`document.querySelector('#nav-more-menu [data-arg="${arg}"]').click();true`); };
        await sc('hub: home', async () => { await waitFor(`document.querySelectorAll('button, a[href]').length > 5`, 'home'); }, { min: 4, expect: `document.querySelectorAll('li.server-group-header[data-server-group] .session-group-count').length >= 2` }); // the agents under their computers, each with its count after the name (#6241)
        await sc('hub: nav menu', async () => { await menu(); }, { min: 10, expect: `!document.getElementById('nav-more-menu').hidden && document.querySelectorAll('#nav-more-menu .nav-more-item').length >= 5` });
        await sc('hub: chats list', async () => { await ev(`document.querySelector('#nav-more-menu [data-arg="history"]').click();true`); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`, 'history'); }, { min: 4, expect: `document.getElementById('nav-select').textContent.includes('Chats')` });
        // the Chats page's search through the Hub (#6305): the results, the advanced filters, a search that finds nothing
        const histType = text => ev(`(() => { const i = document.getElementById('history-search'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await sc('hub: search results', async () => { await histType('needle'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length >= 1`, 'search hits', 10000); }, { min: 3, expect: `document.querySelectorAll('#chat-search-results .chat-search-hit').length >= 1` });
        await sc('hub: search, advanced filters', async () => { await tap('#hf-adv-toggle'); await waitFor(`document.getElementById('hf-advanced').style.display !== 'none'`, 'advanced filters', 5000); }, { min: 3, expect: `document.getElementById('hf-advanced').style.display !== 'none'` });
        await tap('#hf-adv-toggle');
        await sc('hub: search finds nothing', async () => { await histType('zzz'); await waitFor(`/No matching chats/.test(document.getElementById('chat-search-results').textContent)`, 'no results', 10000); }, { min: 3, expect: `/No matching chats/.test(document.getElementById('chat-search-results').textContent)` });
        await histType(''); await waitFor(`!document.getElementById('history-sessions-list').hidden`, 'the chat list again', 10000);
        await sc('hub: chat transcript', async () => { await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click();true`); await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m1')`, 'transcript'); }, { min: 10, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`hub: chat tab ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { min: 10, expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await ev(`document.querySelector('.mobile-history-back').click();true`);
        await sc('hub: boards list', async () => { await pick('board'); await waitFor(`document.querySelectorAll('#mb-projects-ul [data-project]').length===2`, 'boards'); }, { min: 4, expect: `document.querySelectorAll('#mb-projects-ul [data-project]').length === 2` });
        await sc('hub: board', async () => { await ev(`document.querySelector('#mb-projects-ul [data-project="@m1/same-team"]').click();true`); await waitFor(`document.getElementById('mb-messages').textContent.includes('Board message from m1')`, 'board'); }, { min: 10, expect: `document.getElementById('mb-messages').textContent.includes('Board message from m1')` });
        await sc('hub: servers', async () => { await pick('servers'); await waitFor(`document.getElementById('servers-view').getClientRects().length > 0`, 'servers'); }, { min: 8, expect: `document.getElementById('servers-view').getClientRects().length > 0 && /Laptop/.test(document.getElementById('servers-view').textContent)` });
        await sc('hub: inbox', async () => { await pick('hub-inbox'); }, { min: 3, expect: `document.getElementById('nav-select').textContent.includes('Inbox')` });
        await sc('hub: artifacts', async () => { await pick('artifacts'); await waitFor(`!!document.getElementById('artifacts-frame')`, 'artifacts frame'); }, { min: 3, expect: `document.getElementById('nav-select').textContent.includes('Artifacts')` });
        await sc('hub: docs', async () => { await pick('docs'); }, { min: 10, expect: `document.getElementById('nav-select').textContent.includes('Docs')` });
        await sc('hub: account menu', async () => { await tap('[aria-label="Account menu"]'); }, { min: 5, expect: `document.querySelector('[aria-label="Account menu"]').getAttribute('aria-expanded') === 'true'` });
        await ev(`document.body.click(); true`);
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        for (const g of await ev(`[...document.querySelectorAll('#settings-group-select option')].filter(o => !o.hidden).map(o => o.value)`).catch(() => [])) await sc(`hub: settings ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); return true; })()`); await ev(`(() => { const p = document.getElementById('settings-group-select'); p.value = '${g}'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); }, { min: 8, expect: `document.querySelector('#settings-modal .settings-pane:not([hidden])').dataset.group === '${g}'` });
        await ev(`window.hideSettingsModal(); true`);
        await sc('hub: launch', async () => { await ev(`(async () => { await window.showLaunchModal(); return true; })()`); }, { min: 5, expect: `document.querySelector('#launch-modal .modal-content').getClientRects().length > 0` });
        await ev(`(window.hideLaunchModal || (() => {}))(); true`);
        await sc('hub: toasts', async () => {
            await ev(`import((window.__coralStatic || '/static/') + 'utils.js').then(m => { m.showToast('Saved to the board.', false, 60000); m.showToast('Could not reach the agent.', true, 60000); m.showToast('Upload failed.', true, 60000, { action: { label: 'Retry', onClick() {} } }); return true; })`);
        }, { min: 3, expect: `document.querySelectorAll('.toast').length >= 3` });
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
    } finally { await client.close(); }
    const bad = problems(results).concat(required(results, ['hub: toasts', 'hub: home', 'hub: nav menu', 'hub: chats list', 'hub: search results', 'hub: search, advanced filters', 'hub: search finds nothing', 'hub: chat transcript', 'hub: chat tab notes', 'hub: chat tab commits', 'hub: boards list', 'hub: board', 'hub: servers', 'hub: inbox', 'hub: artifacts', 'hub: docs', 'hub: account menu', 'hub: settings general', 'hub: launch']));
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.n + ' controls, ' + r.small.length + ' small'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'controls under 44 px on a phone through the Hub:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} Hub scenes: every control is at least 44 x 44 px`);
})().catch(e => { console.error(e); process.exit(1); });
