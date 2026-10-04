// Focused JSON report integration and safe/bounded rendering checks via CDP.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated server');
const sample = fs.readFileSync(path.join(__dirname,'fixtures/verification_report_2254.json'),'utf8');

(async()=>{
    const client = await CDP({port:Number(process.env.CDP_PORT||9222)});
    const {Page,Runtime,Input}=client;
    const ev=async expression=>{
        const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});
        if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const settle=()=>ev('new Promise(r=>setTimeout(r,80))');
    const shot=async(name,mobile=false)=>{
        if(!process.env.CORAL_SCREENSHOT_DIR)return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});
        const {data}=await Page.captureScreenshot({format:'png',...(mobile?{}:{clip:{x:20,y:20,width:720,height:820,scale:1}})});
        fs.writeFileSync(path.join(process.env.CORAL_SCREENSHOT_DIR,name+'.png'),Buffer.from(data,'base64'));
    };
    try{
        await Page.enable();
        await client.Emulation.setDeviceMetricsOverride({width:1200,height:950,deviceScaleFactor:1,mobile:false});
        await Page.navigate({url:BASE});await Page.loadEventFired();
        await ev(`(async()=>{
            const {state}=await import('/static/state.js');const files=await import('/static/changed_files.js');
            window.__files=files;window.__state=state;
            state.currentSession={type:'live',name:'UI',session_id:'report-test',board_project:'report-team'};
            const panel=document.getElementById('agentic-panel-files');document.body.append(panel);
            panel.style.cssText='display:flex;position:fixed;top:20px;left:20px;width:720px;height:820px;z-index:99999;background:var(--bg-primary);border:1px solid var(--border)';
            window.__content=${JSON.stringify(sample)};window.__inline=true;window.__reads=0;
            window.fetch=async(url)=>{
                const u=String(url);
                if(u.includes('/artifacts?'))return new Response(JSON.stringify({project:'report-team',artifacts:[{name:'verification_report',media_type:'application/json',available:true,...(window.__inline?{inline:true,task_id:2254,content_url:'/api/board/report-team/tasks/2254/artifact-content?source=completion&index=0'}:{uri:'coral://artifacts/'+'e'.repeat(64)})}],has_more:false}));
                if(u.includes('/artifact-content?')||u.startsWith('/api/artifacts/')){window.__reads++;return new Response(window.__content,{headers:{'Content-Type':window.__inline?'text/plain':'application/json','Content-Disposition':'inline; filename="verification_report"'}});}
                return new Response(JSON.stringify({files:[]}));
            };
            files.initFileSearch();files.syncFilesViewerSession();document.querySelector('[data-files-source="team-artifacts"]').click();
        })()`);await settle();
        await ev(`document.querySelector('.team-artifact-preview').click()`);await settle();
        assert.equal(await ev(`document.querySelector('.artifact-report-title').textContent`),'Task #2254');
        assert.equal(await ev(`document.querySelector('.artifact-report-outcome').textContent`),'Reported outcome: success');
        assert.deepEqual(await ev(`[...document.querySelectorAll('.artifact-report-section h2')].map(x=>x.textContent)`),['Summary','Verification','Files','Isolation','Limits']);
        const report=JSON.parse(sample);
        for(const value of [report.summary,report.isolation,...report.files,...report.verification,...report.limits]){
            assert.ok(await ev(`document.querySelector('.artifact-report-readable').textContent.includes(${JSON.stringify(value)})`),'all original values represented');
        }
        assert.equal(await ev(`document.querySelector('.inline-preview-header a').textContent`),'Download');
        await shot('verification-report-2254');
        await ev(`document.querySelectorAll('.artifact-report-controls button')[1].focus()`);
        await Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',text:'\r',windowsVirtualKeyCode:13});
        await Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
        assert.equal(await ev(`document.querySelector('.artifact-report-raw').hidden`),false);
        assert.equal(await ev(`document.querySelector('.artifact-report-raw').textContent`),sample,'raw JSON must be byte-for-byte original UTF-8 text');
        await ev(`document.querySelector('.artifact-report-controls button').click()`);
        assert.equal(await ev('window.__reads'),1,'toggle never refetches');
        // Managed application/json uses the same report renderer.
        await ev(`window._artifactBack();window.__inline=false;document.querySelector('.team-artifacts-heading button').click()`);await settle();
        await ev(`document.querySelector('.team-artifact-preview').click()`);await settle();
        assert.equal(await ev(`document.querySelector('.artifact-report-title').textContent`),'Task #2254');
        // Module-level adversarial and fallback checks run in the real browser.
        const probe=await ev(`(async()=>{
            const {renderJSONReport}=await import('/static/artifact_report.js');const c=document.createElement('div');
            const unusual={summary:'PASS in prose does not create an outcome',extra_fields:{'<img src=x onerror=alert(1)>':[0,false,null,'',[],{},'<script>window.reportExecuted=true<\\/script>'],long_path:'nested/'.repeat(60),camelCase:'kept'},emptyKey:{'':'kept too'}};
            const raw=JSON.stringify(unusual,null,3)+'\\n';const recognized=renderJSONReport(c,raw,'verification_report');
            const safe={recognized,text:c.textContent,scriptCount:c.querySelectorAll('script,img').length,outcome:c.querySelector('.artifact-report-outcome')?.textContent||'',raw:c.querySelector('.artifact-report-raw').textContent};
            const config=renderJSONReport(document.createElement('div'),JSON.stringify({theme:'dark',outcome:'pass',summary:'config prose'}),'settings.json');
            const schema=renderJSONReport(document.createElement('div'),JSON.stringify({task:17,outcome:'not evaluated',summary:'No inference',verification:[]}), 'evidence.json');
            const malformed=renderJSONReport(document.createElement('div'),'{broken','verification_report');
            let deep={end:'retained'};for(let i=0;i<12;i++)deep={next:deep};const deepRaw=JSON.stringify({summary:'deep',nested:deep});renderJSONReport(c,deepRaw,'verification_report');const depth={notice:c.textContent.includes('deeply nested'),raw:c.querySelector('pre').textContent===deepRaw,nodes:c.querySelectorAll('*').length};
            const wideRaw=JSON.stringify({summary:'wide',values:Array.from({length:700},(_,i)=>i)});renderJSONReport(c,wideRaw,'verification_report');const wide={notice:c.textContent.includes('too large'),raw:c.querySelector('pre').textContent===wideRaw,nodes:c.querySelectorAll('*').length};
            const outcomes=['failure','unknown'].map(outcome=>{renderJSONReport(c,JSON.stringify({outcome,summary:'PASS appears only in prose'}),'verification_report');return c.querySelector('.artifact-report-outcome').textContent});
            return {safe,config,schema,malformed,depth,wide,outcomes};
        })()`);
        assert.equal(probe.safe.recognized,true);assert.equal(probe.safe.scriptCount,0);assert.equal(probe.safe.outcome,'');
        for(const text of ['Extra fields','Camel Case','kept','0','false','null','""','[]','{}','<script>'])assert.ok(probe.safe.text.includes(text),text);
        assert.ok(probe.safe.raw.endsWith('\n'));assert.equal(probe.config,false);assert.equal(probe.schema,true);assert.equal(probe.malformed,false);
        assert.deepEqual(probe.outcomes,['Reported outcome: failure','Reported outcome: unknown']);
        for(const result of [probe.depth,probe.wide]){assert.equal(result.notice,true);assert.equal(result.raw,true);assert.ok(result.nodes<10);}
        // Mobile uses the existing overlay and wraps long report paths/prose.
        await ev('window._artifactBack()');
        await client.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
        await ev(`document.getElementById('agentic-panel-files').style.width='350px';document.getElementById('agentic-panel-files').style.zIndex='1';document.querySelector('.team-artifact-preview').click()`);await settle();
        assert.ok(await ev(`!!document.querySelector('.mobile-file-preview-overlay .artifact-report')`));
        assert.ok(await ev(`(()=>{const r=document.querySelector('.artifact-report');return r.scrollWidth<=r.clientWidth+1})()`),'mobile report must not overflow horizontally');
        await shot('verification-report-2254-mobile',true);
        await ev('window._artifactBack()');
        assert.equal(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`),false);
        console.log('PASS JSON reports: actual #2254 inline/managed, all fields, exact raw toggle, safe nested extras, conservative recognition, depth/node bounds, mobile wrapping/back');
    }finally{await client.close();}
})().catch(error=>{console.error(error);process.exitCode=1});
