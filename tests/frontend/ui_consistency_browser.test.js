// Actual templates + shipped renderers, with an isolated static server and API fixtures.
// No application server, telemetry, agent messages, or real mutations are used.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const CDP = require('chrome-remote-interface');
const root = path.resolve(__dirname, '../../coral-go/internal/server/frontend');
const baseline = process.env.CORAL_UI_BASELINE;
const shots = process.env.CORAL_SCREENSHOT_DIR;
function template(name) {
    const old = baseline && path.join(baseline, 'templates', name);
    return fs.readFileSync(old && fs.existsSync(old) ? old : path.join(root, 'templates', name), 'utf8')
        .replace(/{{\s*template "([^"]+)" \.\s*}}/g, (_, file) => template(file))
        .replace(/{{[^}]+}}/g, '');
}
let html = template('index.html').replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, '')
    .replace(/<link[^>]+(?:https:|manifest|preconnect)[^>]*>/g, '');
html = html.replace('</head>', (process.env.CORAL_FIXTURE_FONTS ? fs.readFileSync(process.env.CORAL_FIXTURE_FONTS, 'utf8') : '') + '<script src="/static/vendor/marked.min.js"></script><script src="/static/vendor/purify.min.js"></script></head>');
const fixtures = async () => {
    localStorage.clear();
    window.s = (await import('/static/state.js')).state;
    window.mods = {};
    for (const name of ['render','controls','live_chat','tasks','workflows','scheduler','docs','cost_dashboard','agent_ui','modals','agentic_state','sidebar','utils','team_working_mode','message_board']) mods[name] = await import('/static/' + name + '.js');
    Object.assign(window, mods.tasks, mods.workflows, mods.scheduler, mods.docs, mods.modals);
    window.switchAgenticTab = mods.agentic_state.switchAgenticTab;
    window.toggleAgenticPanel = mods.sidebar.toggleAgenticPanel;
    window.uiWrites = [];
    const stamp = '2026-10-05T21:14:36Z';
    const task = {id:2505,title:'Unify typography and controls across Coral',body:'Verify the current layout at narrow widths.\n\nKeep **all controls** and code formatting.\n\n```js\nconst ready = true;\n```',priority:'high',status:'in_progress',assigned_to:'UI',claimed_by:'UI',created_at:stamp,blocked_by:[]};
    const workflow = {id:1,name:'Review and verify workspace changes',description:'Check the implementation, review evidence, and publish the result.',enabled:true,steps:[{name:'Verify',type:'command',command:'node tests/frontend/ui_consistency_browser.test.js'}],step_count:1,last_run:{status:'completed',started_at:stamp}};
    const job = {id:1,name:'Daily verification',description:'Review pending tasks and report failed checks.',cron_expr:'0 9 * * *',timezone:'America/Los_Angeles',enabled:true,agent_type:'codex',prompt:'Run the current workspace checks and summarize the results.',repo_path:'/workspace/coral',max_duration_s:3600,next_fire_at:stamp};
    window.fetch = async (url, options = {}) => {
        if (options.method && options.method !== 'GET' && !String(url).endsWith('/subscribe')) uiWrites.push({url,method:options.method}); // board viewer subscription is also mocked
        const u = new URL(url, location.origin), p = u.pathname;
        let data = {};
        if(p.endsWith('/working-mode'))data={mode:'none',custom_instructions:'Keep shared workspace changes isolated.'};
        else if(p.endsWith('/working-mode/presets'))data={presets:[{id:'none',name:'None',instructions:'Use the task requirements to guide implementation.',builtin:true}],selected:'none'};
        else if(p==='/api/settings')data={settings:{board_health_monitor:'false'}};
        else if(p==='/api/board/projects')data=[{project:'coral-task-workflows',subscriber_count:4,message_count:2}];
        else if(p.endsWith('/messages/all'))data={messages:[{id:1,job_title:'Orchestrator',subscriber_id:'lead',content:'Review the UI consistency evidence before accepting this task.',created_at:stamp},{id:2,job_title:'UI',subscriber_id:'ui',content:'The current layout is preserved. **Keyboard checks pass.**',created_at:stamp}],total:2};
        else if(p==='/api/workflows')data={workflows:[workflow]};
        else if(p==='/api/workflows/1')data=workflow;
        else if(p==='/api/workflows/1/runs')data={runs:[{id:1,status:'completed',started_at:stamp,finished_at:stamp,completed_steps:1,total_steps:1}]};
        else if(p==='/api/scheduled/jobs')data={jobs:[job]};
        else if(p.includes('/scheduled/jobs/1/runs'))data={runs:[]};
        else if(p==='/api/agent-docs')data=[{name:'README',title:'Workspace guide'}];
        else if(p==='/api/agent-docs/README')data={content:'# Workspace guide\n\nUse the Files tabs to browse repository files and team artifacts.\n\n## Review a change\n\n1. Open the task.\n2. Check the evidence.\n3. Leave a concise result.\n\n```sh\ncoral-board read\n```\n\n| Source | Contents |\n| --- | --- |\n| Files | Changed repository files |\n| Team Artifacts | Shared evidence |'};
        else if(p==='/api/token-usage/summary')data={totals:{input_tokens:48000,output_tokens:9200,cache_read_tokens:16000,num_sessions:4,cost_usd:2.48},by_agent_type:[{agent_type:'codex',num_sessions:4,input_tokens:48000,output_tokens:9200,cost_usd:2.48}],by_agent:[{session_id:'fixture-0',agent_name:'UI',board_name:'coral-task-workflows',cost_usd:2.48,input_tokens:48000,output_tokens:9200}]};
        else if(p==='/api/token-usage/timeseries')data={series:[]};
        else if(p.includes('/token-usage/'))data=[];
        else if(p==='/api/themes')data={themes:[]};
        else if(p==='/api/system/privacy')data={remote_access_enabled:false,telemetry_enabled:false};
        else if(p==='/api/agent/ui')data=[];
        else if(p.endsWith('/subscribers'))data=[{subscriber_id:'UI'},{subscriber_id:'Lead Developer'}];
        else if(p.endsWith('/tasks'))data={tasks:[task]};
        else if(p.endsWith('/artifacts'))data={project:'coral-task-workflows',session_id:s.currentSession?.session_id,artifacts:[]};
        else if(p.includes('/chat'))data={messages:[],total:0,has_more:false};
        else if(p.includes('/files'))data={files:[]};
        return new Response(JSON.stringify(data),{headers:{'Content-Type':'application/json'}});
    };
    s.liveSessions=['Orchestrator','Lead Developer','Backend Dev','UI'].map((name,i)=>({name:'coral-task-workflows',session_id:'fixture-'+i,display_name:name,board_project:'coral-task-workflows',agent_type:'codex',working_directory:'/workspace/coral',waiting_for_input:i===0,working:i===3,status:'idle',summary:'Review workspace consistency',branch:'main'}));
    s.currentSession={...s.liveSessions[3],type:'live'};s.currentBoardTasks=[task];s.settings={};s.killedSessions={};
    mods.render.renderLiveSessions(s.liveSessions);
    document.getElementById('welcome-screen').style.display='none';document.getElementById('startup-loading').style.display='none';
    document.querySelectorAll('.session-view').forEach(e=>e.style.display='none');document.getElementById('live-session-view').style.display='flex';
    document.getElementById('terminal-header-label').textContent='UI';document.getElementById('capture-wrapper').classList.add('chat-mode');document.getElementById('command-pane').classList.add('chat-mode');
    mods.controls.renderQuickActions();
    mods.live_chat.renderTranscript([{type:'user',content:'Keep the workspace layout and make the fonts, icons and controls consistent.'},{type:'assistant',text:'I am checking the existing controls.',phase:'commentary',tool_uses:[{id:'fixture-tool',name:'Bash',input:{command:'node tests/frontend/ui_consistency_browser.test.js'}}]},{type:'tool_result',tool_use_id:'fixture-tool',content:'PASS: fixture checks completed.'},{type:'assistant',text:'The shared controls use the same type scale.\n\n- **Actions:** explicit labels and visible focus.\n- **Metadata:** readable in both themes.\n- **Layout:** sidebar and composer keep their dimensions.\n\n```js\nconst selected = "Files";\n```\n\nReview the task evidence before accepting the change.'}],document.getElementById('live-history-messages'),'codex');
    await mods.tasks.loadBoardTasks('coral-task-workflows');
    await mods.agent_ui.refreshAgentUI();
    window.uiView = async name => {
        document.getElementById('team-working-mode-dialog')?.close();
        mods.modals.hideSettingsModal();mods.tasks.hideTaskDetailModal();
        document.querySelectorAll('.session-view').forEach(e=>e.style.display='none');
        document.querySelector('.layout')?.classList.toggle('sidebar-hidden', ['workflows','analytics','docs'].includes(name));
        document.body.classList.toggle('mobile-session-open',innerWidth<768);
        document.getElementById('agentic-state').classList.remove('mobile-panel-overlay');
        if(['workspace','tasks','agent-ui','settings','team-settings'].includes(name)){
            s.currentSession={...s.liveSessions[3],type:'live'};
            mods.utils.showView('live-session-view');
            window.switchAgenticTab(name==='agent-ui'?'agent-ui':'tasks','top');
            if(name==='agent-ui')await mods.agent_ui.refreshAgentUI();
            if(name==='tasks')mods.tasks.showTaskDetailModal(2505);
            if(name==='settings')await mods.modals.showSettingsModal();
            if(name==='team-settings')mods.team_working_mode.showTeamWorkingMode('coral-task-workflows');
            if(name==='agent-ui'&&innerWidth<768)window.toggleAgenticPanel(true);
        } else if(name==='workflows'){mods.workflows.showWorkflowsTab();await new Promise(r=>setTimeout(r,30));await mods.workflows.selectWorkflow(1)}
        else if(name==='docs'){await mods.docs.showDocsTab();await mods.docs.selectDoc('README')}
        else if(name==='analytics'){await mods.cost_dashboard.showCostDashboard();mods.cost_dashboard.stopCostDashboard()}
        else if(name==='board')mods.message_board.selectBoardProject('coral-task-workflows');
        else if(name==='scheduler'){mods.scheduler.initScheduler();await new Promise(r=>setTimeout(r,30));await mods.scheduler.selectScheduledJob(1)}
    };
};
(async()=>{
    let client;
    const server=http.createServer((req,res)=>{
        res.setHeader('Cache-Control','no-store');
        if(req.url==='/'){res.setHeader('Content-Type','text/html');res.end(html);return;}
        const pathname=new URL(req.url,'http://localhost').pathname;
        const file=path.resolve(root,'.'+pathname);
        if(!pathname.startsWith('/static/')||!file.startsWith(root+path.sep)){res.writeHead(404).end();return;}
        let source=baseline?path.resolve(baseline,'.'+pathname):file;
        if(!baseline||!fs.existsSync(source))source=file;
        try{res.setHeader('Content-Type',file.endsWith('.css')?'text/css':file.endsWith('.js')?'text/javascript':'application/octet-stream');res.end(fs.readFileSync(source))}catch{res.writeHead(404).end()}
    });
    await new Promise(r=>server.listen(0,'127.0.0.1',r));
    const timeout=setTimeout(()=>{console.error('UI consistency test exceeded 60s');process.exit(1)},60000);
    try{
        client=await CDP({port:Number(process.env.CDP_PORT||9222)});const c=client;
        const ev=async expression=>{const r=await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value};
        const wait=()=>ev('new Promise(r=>setTimeout(r,80))');
        await c.Page.enable();await c.Network.enable();await c.Network.setCacheDisabled({cacheDisabled:true});
        await c.Emulation.setDeviceMetricsOverride({width:1440,height:960,deviceScaleFactor:1,mobile:false});
        await c.Page.navigate({url:`http://127.0.0.1:${server.address().port}/`});await c.Page.loadEventFired();
        await ev('('+fixtures.toString()+')()');await ev('document.fonts.ready');
        const geometry=await ev(`({sidebar:document.querySelector('.sidebar').getBoundingClientRect().width,composer:document.getElementById('command-pane').getBoundingClientRect().height})`);
        assert.equal(geometry.sidebar,320);assert.equal(geometry.composer,125,'composer footprint is unchanged');
        if(!baseline){
            assert.equal(await ev(`getComputedStyle(document.querySelector('.chat-bubble.assistant .message-text')).fontSize`),'14px');
            assert.equal(await ev(`getComputedStyle(document.querySelector('.tool-call-icon')).fontSize`),'16px');
            await ev(`document.documentElement.style.setProperty('--chat-prose-font','Georgia');document.documentElement.style.setProperty('--chat-prose-size','19px')`);
            assert.match(await ev(`getComputedStyle(document.querySelector('.chat-bubble.assistant .message-text')).fontFamily`),/Georgia/);
            assert.equal(await ev(`getComputedStyle(document.querySelector('.chat-bubble.assistant .message-text')).fontSize`),'19px');
            assert.match(await ev(`getComputedStyle(document.querySelector('.chat-bubble.assistant pre code')).fontFamily`),/mono/i);
            await ev(`document.documentElement.style.cssText=''`);
        }
        for(const theme of ['light','dark'])for(const [device,width,height] of [['desktop',1440,960],['mobile',390,844]]){
            await c.Emulation.setDeviceMetricsOverride({width,height,deviceScaleFactor:1,mobile:width<768});
            const palette=process.env.CORAL_UI_THEME_DIR?JSON.parse(fs.readFileSync(path.join(process.env.CORAL_UI_THEME_DIR,theme==='light'?'Happy-Morning.json':'Dark.json'))).variables:{};
            await ev(`document.documentElement.style.cssText='';document.documentElement.dataset.theme='${theme}';Object.entries(${JSON.stringify(palette)}).forEach(([k,v])=>document.documentElement.style.setProperty(k,v))`);
            for(const view of ['workspace','tasks','settings','team-settings','board','agent-ui','workflows','scheduler','docs','analytics']){
                await ev(`uiView('${view}')`);await wait();
                if(view==='tasks'||view==='settings'){
                    const modal=view==='tasks'?'task-detail-modal':'settings-modal';
                    assert.ok(await ev(`(()=>{const r=document.getElementById('${modal}').querySelector('.modal-content').getBoundingClientRect();return r.width>0&&r.height>0&&r.left>=-1&&r.right<=innerWidth+1&&r.bottom<=innerHeight+1})()`),`${view} fits ${device}`);
                }
                const required={workspace:'.chat-bubble.assistant .message-text',tasks:'#task-detail-content',settings:'#settings-working-dir','team-settings':'.working-mode-form',board:'.mb-message-body','agent-ui':'.agent-ui-request',workflows:'.wf-step-item',scheduler:'.sched-header',docs:'#docs-content h1',analytics:'.cost-card-value'};
                assert.ok(await ev(`Array.from(document.querySelectorAll('${required[view]}')).some(node=>node.getBoundingClientRect().width>0)`),`${view} renderer produced visible content`);
                if(!baseline && view==='team-settings') {
                    assert.equal(await ev(`document.querySelector('.team-settings-actions [type=submit]').disabled`),false);
                    await ev(`document.querySelector('.working-mode-form [name=custom_instructions]').value='Preserve this draft';document.querySelector('[data-settings-tab=workflow]').focus()`);
                    await c.Input.dispatchKeyEvent({type:'keyDown',key:'ArrowRight',code:'ArrowRight'});
                    assert.equal(await ev(`document.activeElement.dataset.settingsTab`),'prompts');
                    await ev(`document.querySelector('[data-settings-tab=workflow]').click()`);
                    assert.equal(await ev(`document.querySelector('.working-mode-form [name=custom_instructions]').value`),'Preserve this draft');
                    await ev(`document.querySelector('[data-settings-tab=workflow]').focus()`);
                    const bounds=await ev(`{const r=document.querySelector('#team-working-mode-dialog').getBoundingClientRect();({left:r.left,right:r.right,bottom:r.bottom,width:innerWidth,height:innerHeight})}`);
                    assert.ok(bounds.left>=0&&bounds.right<=bounds.width&&bounds.bottom<=bounds.height,'team dialog fits');
                }
                if(!baseline && view==='tasks') {
                    assert.ok(await ev(`Array.from(document.querySelectorAll('#task-detail-modal-footer button')).filter(b=>b.getBoundingClientRect().width).every(b=>{const r=b.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.bottom<=innerHeight})`),'all task actions remain visible');
                    await c.Input.dispatchKeyEvent({type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
                    await c.Input.dispatchKeyEvent({type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
                    await ev(`document.querySelector('#task-detail-modal .modal-close-btn').focus()`);
                    assert.equal(await ev(`getComputedStyle(document.activeElement).outlineStyle`),'solid');
                }
                if(!baseline && view==='workflows') {
                    assert.ok(await ev(`Array.from(document.querySelectorAll('.wf-detail-actions button')).every(b=>{const r=b.getBoundingClientRect();return r.width>0&&r.left>=0&&r.right<=innerWidth+1})`),'workflow actions fit instead of squeezing title');
                }
                if(!baseline && view==='settings') {
                    await ev(`document.getElementById('settings-working-dir').focus();document.getElementById('settings-working-dir').value='/workspace/keyboard-fixture'`);
                    assert.equal(await ev(`document.activeElement.value`),'/workspace/keyboard-fixture');
                    assert.notEqual(await ev(`getComputedStyle(document.activeElement).boxShadow`),'none','focused field has a visible ring');
                    assert.ok(await ev(`document.querySelector('#settings-modal input[type=checkbox]').getBoundingClientRect().height<36`),'native checkbox has not inherited text field sizing');
                    if(device==='mobile')assert.ok(await ev(`Array.from(document.querySelectorAll('#settings-modal .modal-footer .btn')).every(b=>b.getBoundingClientRect().height>=44)`),'mobile dialog action targets');
                    await ev(`document.activeElement.blur()`);
                }
                if(shots){fs.mkdirSync(shots,{recursive:true});fs.writeFileSync(path.join(shots,`${theme}-${device}-${view}.png`),Buffer.from((await c.Page.captureScreenshot({format:'png'})).data,'base64'))}
            }
        }
        if(!baseline){
            await c.Emulation.setDeviceMetricsOverride({width:1440,height:960,deviceScaleFactor:1,mobile:false});
            await ev(`uiView('tasks')`);await wait();
            await ev(`document.querySelector('#task-detail-modal .modal-close-btn').focus()`);
            await c.Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',text:'\r',windowsVirtualKeyCode:13});
            await c.Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
            assert.equal(await ev(`document.getElementById('task-detail-modal').style.display`),'none','keyboard closes the real task dialog');
            await ev(`uiView('workspace')`);await wait();
            await ev(`document.querySelector('.work-group-summary').click()`);
            assert.ok(await ev(`document.querySelector('.work-group').open`),'tool disclosure still opens');
            await ev(`document.querySelector('.work-group-summary').click()`);
            await c.Emulation.setEmulatedMedia({features:[{name:'prefers-reduced-motion',value:'reduce'}]});
            await ev(`{const probe=document.createElement('span');probe.className='wf-status-dot running';probe.id='motion-probe';document.body.append(probe)}`);
            assert.equal(await ev(`getComputedStyle(document.getElementById('motion-probe')).animationName`),'none');
            await c.Emulation.setEmulatedMedia({features:[]});
            await ev(`document.getElementById('motion-probe').remove();uiView('settings')`);await wait();
            // 200% browser zoom has half the CSS viewport at the same pixel size.
            await c.Emulation.setDeviceMetricsOverride({width:720,height:480,deviceScaleFactor:2,mobile:false});
            // Zoom must keep a scrolling form and its actions in the dialog.
            assert.ok(await ev(`{const b=document.querySelector('#settings-modal .modal-body');b.scrollHeight>b.clientHeight}`));
            assert.ok(await ev(`Array.from(document.querySelectorAll('#settings-modal .modal-footer button')).every(b=>{const r=b.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.bottom<=innerHeight})`),'settings actions remain visible in a 200%-equivalent viewport');
            await c.Emulation.setDeviceMetricsOverride({width:1440,height:960,deviceScaleFactor:1,mobile:false});
        }
        assert.deepEqual(await ev('uiWrites'),[],'no mutations other than mocked viewer subscription');
        console.log('PASS actual-source workspace/tasks/settings/team settings/board/Agent UI/workflows/scheduler/docs/analytics in light/dark desktop/mobile; custom prose font/size and mono preserved; modal/action bounds, keyboard, forms, 200% zoom, reduced motion; no real mutations.',JSON.stringify(geometry));
        if(shots)fs.writeFileSync(path.join(shots,'geometry.json'),JSON.stringify(geometry));
    }finally{clearTimeout(timeout);if(client){await client.Page.navigate({url:'about:blank'});await client.close()}await new Promise(r=>server.close(r))}
})().catch(error=>{console.error(error);process.exitCode=1});
