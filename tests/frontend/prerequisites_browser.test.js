// Read-only fake prerequisites; no installers, agents, or analytics requests leave the fixture.
const assert=require('node:assert/strict');const fs=require('node:fs');const CDP=require('chrome-remote-interface');
const base=process.env.CORAL_URL;if(!base||/:8420(\/|$)/.test(base))throw Error('Use isolated server');
(async()=>{
 const c=await CDP({port:Number(process.env.CDP_PORT||9222)});
 const ev=async expression=>{const r=await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const settle=()=>ev('new Promise(r=>setTimeout(r,90))');
 const click=async s=>{await ev(`document.querySelector(${JSON.stringify(s)}).click()`);await settle();};
 const text=()=>ev(`document.querySelector('#fixture .acf-cli-warning').textContent`);
 const shot=async name=>{if(!process.env.CORAL_SCREENSHOT_DIR)return;fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});const {data}=await c.Page.captureScreenshot({format:'png'});fs.writeFileSync(process.env.CORAL_SCREENSHOT_DIR+'/'+name+'.png',Buffer.from(data,'base64'));};
 try{
 await c.Page.enable();await c.Emulation.setDeviceMetricsOverride({width:1100,height:900,deviceScaleFactor:1,mobile:false});
 await c.Page.addScriptToEvaluateOnNewDocument({source:`window.calls=[];window.pending=[];window.cliMode='normal';window.cli={claude:{found:false,status:'missing',agent_type:'claude',install_command:'install claude externally'},codex:{found:false,status:'missing',agent_type:'codex'}};window.statusData={startup_complete:true,tmux_available:false,tmux_required:true,effective_backend:'tmux',tmux_install_command:'brew install tmux'};window.statusMode='normal';
 window.WebSocket=class{constructor(){this.readyState=0}send(){}close(){}addEventListener(){}removeEventListener(){}};
 const real=fetch;window.fetch=async(url,options={})=>{const u=new URL(url,location.origin);const json=(data,status=200)=>new Response(JSON.stringify(data),{status,headers:{'Content-Type':'application/json'}});if(!u.pathname.startsWith('/api/'))return real(url,options);calls.push({url:u.pathname+u.search,method:options.method||'GET',cache:options.cache});
 if(u.pathname==='/api/system/cli-check'){if(cliMode==='http')return json({error:'backend failure'},503);if(cliMode==='network')throw Error('Network unavailable');const data=cli[u.searchParams.get('type')]||{found:true,status:'available'};if(cliMode==='defer')return new Promise(r=>pending.push(()=>r(json(data))));return json(data);}
 if(u.pathname==='/api/system/status'){if(statusMode==='http')return json({},502);return json(statusData);}
 if(u.pathname==='/api/settings'||u.pathname==='/api/agent-models')return json({});return json([]);
 };`});
 await c.Page.navigate({url:base});await c.Page.loadEventFired();await settle();
 await ev(`(async()=>{window.checks=await import('/static/prerequisites.js');const p=document.createElement('div');p.id='fixture';p.className='modal-content';p.style.cssText='position:fixed;top:140px;left:20px;width:560px;height:700px;overflow:auto;z-index:99999;background:var(--bg-primary);padding:16px';document.body.append(p);window.renderAgentConfigForm('fixture',{showPreset:false,showName:false,value:{agentType:'claude'}})})()`);await settle();
 assert.match(await text(),/claude CLI was not found/);assert.match(await text(),/Install in your terminal/);await shot('prerequisites-missing');
 await ev(`window.cli.claude={found:true,status:'available',agent_type:'claude'}`);await click('#fixture .acf-cli-warning button');
 assert.equal(await ev(`document.querySelector('#fixture .acf-cli-warning').style.display`),'none');
 assert.ok(await ev(`calls.some(x=>x.url.includes('source=cli_recheck')&&x.cache==='no-store')`));
 await ev(`window.cliMode='http';window._checkAgentCLI('claude',document.querySelector('#fixture .acf-cli-warning'),true)`);await settle();assert.match(await text(),/HTTP 503/);
 const failedCalls=await ev(`calls.filter(x=>x.url.includes('/cli-check')).length`);await ev(`window._checkAgentCLI('claude',document.querySelector('#fixture .acf-cli-warning'))`);assert.equal(await ev(`calls.filter(x=>x.url.includes('/cli-check')).length`),failedCalls+1,'failed explicit recheck discarded the prior available cache');assert.match(await text(),/HTTP 503/);
 await ev(`window.cliMode='network'`);await click('#fixture .acf-cli-warning button');assert.match(await text(),/Network unavailable/);
 await ev(`window.cliMode='normal';window.cli.claude={available:true}`);await click('#fixture .acf-cli-warning button');assert.match(await text(),/unexpected response/,'available is not the found contract');
 for(const status of ['probe_failed','timeout']){
 await ev(`window.cli.claude={found:true,status:${JSON.stringify(status)},agent_type:'claude'}`);await click('#fixture .acf-cli-warning button');assert.match(await text(),status==='timeout'?/found, but version check timed out/:/found, but version check failed/);
 }
 // The old provider request ignores AbortSignal deliberately: its response must still be dropped.
 await ev(`window.cliMode='defer';window.cli.claude={found:true,status:'available',agent_type:'claude'};window._checkAgentCLI('claude',document.querySelector('#fixture .acf-cli-warning'),true);void 0`);await settle();assert.equal(await ev(`document.querySelector('#fixture .acf-cli-warning button').disabled`),true);
 await ev(`window.cliMode='normal';const s=document.querySelector('#fixture .acf-agent-type');s.value='codex';s.dispatchEvent(new Event('change',{bubbles:true}))`);await settle();assert.match(await text(),/codex CLI was not found/);
 await ev(`pending.splice(0).forEach(r=>r())`);await settle();assert.match(await text(),/codex CLI was not found/);
 await ev(`window.setAgentConfig('fixture',{agentType:'terminal'})`);assert.equal(await ev(`document.querySelector('#fixture .acf-cli-warning').style.display`),'none');
 await ev(`window.setAgentConfig('fixture',{agentType:'codex'})`);await settle();assert.match(await text(),/codex CLI was not found/);
 // Independent config forms must not share warning DOM or race generations.
 await ev(`const p=document.createElement('div');p.id='fixture2';document.body.append(p);window.renderAgentConfigForm('fixture2',{showPreset:false,value:{agentType:'claude'}});window._checkAgentCLI('claude',document.querySelector('#fixture2 .acf-cli-warning'),true)`);await settle();assert.equal(await ev(`document.querySelector('#fixture2 .acf-cli-warning').style.display`),'none');assert.match(await text(),/codex/);
 assert.match(await ev(`document.getElementById('tmux-check-message').textContent`),/tmux-backed/);
 await ev(`window.statusData.tmux_available=true`);await click('#tmux-check-again');assert.equal(await ev(`document.getElementById('tmux-missing-banner').style.display`),'none');
 await ev(`window.statusData={startup_complete:true,tmux_available:false,tmux_required:false,effective_backend:'pty'};window.checks.checkTmux(true)`);await settle();assert.equal(await ev(`document.getElementById('tmux-missing-banner').style.display`),'none','PTY is not blocked by missing tmux');
 await ev(`window.statusMode='http';window.checks.checkTmux(true)`);await settle();assert.match(await ev(`document.getElementById('tmux-check-message').textContent`),/HTTP 502/);
 await ev(`window.statusMode='normal';window.statusData={startup_complete:true,tmux_available:false}`);await click('#tmux-check-again');assert.match(await ev(`document.getElementById('tmux-check-message').textContent`),/tmux-backed/);assert.equal(await ev(`document.querySelector('.tmux-missing-banner-cmd').hidden`),true,'no hardcoded macOS install command');
 assert.ok(await ev(`calls.some(x=>x.url==='/api/system/status'&&x.cache==='no-store')`));
 assert.equal(await ev(`calls.filter(x=>x.method!=='GET'&&x.url!=='/api/tracking/event').length`),0,'checks never install or launch');
 await c.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});await ev(`document.getElementById('fixture2').style.display='none';document.getElementById('fixture').style.width='340px';document.getElementById('fixture').style.left='8px'`);await shot('prerequisites-mobile');
 console.log('PASS prerequisites: found contract, missing->available explicit recheck, HTTP/network/invalid/probe states, stale provider and preset changes, independent forms, tmux recovery/cache bypass/PTY/neutral fallback, no installs');
 }finally{await c.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
