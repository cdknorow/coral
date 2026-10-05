// Static-only browser fixture: no Coral server, live inputs or backend required.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const staticRoot = path.resolve(__dirname, '../../coral-go/internal/server/frontend/static');
const html = '<!doctype html><html><body><section data-section="live-sessions"><span class="section-count-badge"></span><ul id="live-sessions-list"></ul></section></body></html>';
(async () => {
    const server = http.createServer(async (req, res) => {
        if (req.url === '/') {res.setHeader('Content-Type','text/html');res.end(html);return;}
        const pathname = new URL(req.url, 'http://localhost').pathname;
        const file = path.resolve(staticRoot, '.' + pathname.replace(/^\/static/, ''));
        if (!pathname.startsWith('/static/') || !file.startsWith(staticRoot + path.sep)) {res.writeHead(404).end();return;}
        try {res.setHeader('Content-Type','text/javascript');res.end(await fs.readFile(file));}
        catch {res.writeHead(404).end();}
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    let client;
    const timeout = setTimeout(() => {console.error('FAIL metadata browser regression exceeded 30 seconds');process.exitCode=1;client?.close();server.closeAllConnections();server.close();},30000);
    try {
        client = await CDP({port:Number(process.env.CDP_PORT || 9222)});
        const ev = async expression => {
            const r=await client.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});
            if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));
            return r.result.value;
        };
        await client.Page.enable();
        await client.Page.navigate({url:`http://127.0.0.1:${server.address().port}`});await client.Page.loadEventFired();
        await ev(`(async()=>{
            localStorage.clear();localStorage.setItem('coral-group-by-team','true');
            window.failures=[];window.liveRequests=0;window.metadataUnavailable=false;
            window.snapshot=[
                {session_id:'fixture-lead',name:'repo',display_name:'Lead Developer',board_project:'coral-task-workflows',agent_type:'claude',working_directory:'/fixture/repo',status:'idle'},
                {session_id:'fixture-ui',name:'repo',display_name:'UI Specialist',board_project:'coral-task-workflows',agent_type:'codex',working_directory:'/fixture/repo',status:'idle'}
            ];
            window.fetch=async url=>{
                if(url==='/api/sessions/live'){
                    liveRequests++;
                    return metadataUnavailable
                        ? new Response(JSON.stringify({error:'agent_metadata_unavailable'}),{status:503,headers:{'Content-Type':'application/json'}})
                        : new Response(JSON.stringify(snapshot),{status:200,headers:{'Content-Type':'application/json'}});
                }
                return new Response('{}',{headers:{'Content-Type':'application/json'}});
            };
            window.s=(await import('/static/state.js')).state;
            s.liveSessions=[];s.killedSessions={};
            window.api=await import('/static/api.js');
            window.load=()=>api.loadLiveSessions(reason=>failures.push(reason));
            window.inspect=()=>({
                state:s.liveSessions.map(x=>({id:x.session_id,name:x.display_name,team:x.board_project})),
                rows:[...document.querySelectorAll('#live-sessions-list .session-group-item[data-session-id]')].map(x=>({id:x.dataset.sessionId,text:x.textContent,team:x.closest('.session-team-group')?.querySelector('.group-name-line')?.textContent})),
                badge:document.querySelector('.section-count-badge').textContent
            });
        })()`);
        assert.equal(await ev('load()'),true);
        const before=await ev('inspect()');
        assert.equal(before.rows.length,2);assert.equal(before.badge,'2');
        for(const [id,name] of [['fixture-lead','Lead Developer'],['fixture-ui','UI Specialist']]) {
            const row=before.rows.find(x=>x.id===id);
            assert.ok(row,id+' row rendered');assert.ok(row.text.includes(name));assert.ok(row.team.includes('coral-task-workflows'));
        }
        await ev(`window.savedState=s.liveSessions;window.savedHtml=document.getElementById('live-sessions-list').innerHTML;metadataUnavailable=true`);
        for(let i=0;i<3;i++)assert.equal(await ev('load()'),false);
        assert.deepEqual(await ev('inspect()'),before,'503 must preserve names, identities, team groups and count');
        assert.equal(await ev(`s.liveSessions===savedState && document.getElementById('live-sessions-list').innerHTML===savedHtml`),true,'failed snapshots must not replace known state or markup');
        assert.deepEqual(await ev('failures'),['sessions_fetch_http','sessions_fetch_http','sessions_fetch_http']);
        await ev(`metadataUnavailable=false;snapshot=[{...snapshot[0],display_name:'Lead Updated',board_project:'recovered-team'}]`);
        assert.equal(await ev('load()'),true);
        const after=await ev('inspect()');
        assert.deepEqual(after.state,[{id:'fixture-lead',name:'Lead Updated',team:'recovered-team'}]);
        assert.equal(after.rows.length,1);assert.equal(after.badge,'1');
        assert.ok(after.rows[0].text.includes('Lead Updated'));assert.ok(after.rows[0].team.includes('recovered-team'));
        assert.equal(await ev('liveRequests'),5);
        console.log('PASS: named/team rows and state retained across three agent_metadata_unavailable 503s; next healthy snapshot updates name/team and removes obsolete row.');
    } finally {
        clearTimeout(timeout);
        if(client)await client.close();
        server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
    }
})().catch(error=>{console.error(error);process.exitCode=1;});
