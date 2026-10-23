'use strict';
// The stubbed two-computer Coral Hub the dashboard is served as (hub_history_board_nav_browser's): cloud mode under the hub's CSP, two computers (Laptop, Desktop) with a chat, a board and their pages. Shared by the
// Hub's keyboard and announcements censuses (#6226). window.__computers is the computer list /api/servers answers (change .online to change a status) and window.__extraBoard appends messages to the board.
const { safePaused } = require('./paused');
const BASE = require('../test_base').BASE;
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

/** Set the page up as the Hub's before the first navigation: the viewport, the cloud boot flag with the hub's CSP, and the stubbed API. */
async function setupHubWorld({ client, Page, Emulation, width, height, mobile }) {
    const { Fetch, Network } = client;
    await Promise.all([Page.enable(), Network.enable()]);
    await Network.setBypassServiceWorker({ bypass: true });
    await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
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
   const computers=window.__computers=[{id:'m1',label:'Laptop',status:'online',online:true,access_mode:'view',local_mode:'launch',effective_mode:'view'},{id:'m2',label:'Desktop',status:'online',online:true,access_mode:'interact',local_mode:'launch',effective_mode:'interact'}];
   const json=x=>Promise.resolve(new Response(JSON.stringify(x),{headers:{'content-type':'application/json'}}));
   window.fetch=(u,o={})=>{
    const p=new URL(String(u),location.href); const m=(o.method||'GET').toUpperCase();
    if(p.pathname==='/auth/session')return json({authenticated:true,email:'tester@example.com',csrf:'test'});
    if(p.pathname==='/api/account')return json({artifacts_tab:true,artifacts_frame:true});
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
    if(path.endsWith('/messages/all')){const base=[{id:1,subscriber_id:'Operator',job_title:'Operator',content:'Board message from '+server,created_at:'2026-10-22T12:00:00Z'},{id:2,subscriber_id:'Operator',job_title:'Operator',content:'Second message from '+server,created_at:'2026-10-22T12:00:20Z'},{id:3,subscriber_id:'Operator',job_title:'Operator',content:'Third message from '+server,created_at:'2026-10-22T12:00:40Z'}];const all=base.concat(window.__extraBoard||[]);return json({messages:all,total:all.length,before_count:0});}
    if(path.endsWith('/subscribers'))return json([{subscriber_id:'Operator',job_title:'Owner'}]);
    if(path.endsWith('/paused'))return json({paused:false});
    if(path.endsWith('/sleep-status'))return json({sleeping:false});
    return json({});
   };
  })();`});
}

module.exports = { setupHubWorld, BASE };
