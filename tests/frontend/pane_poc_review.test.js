const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT) });
  const { Page, Runtime, Emulation, Input } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const shot = async name => fs.writeFileSync(`/tmp/coral-1304-${name}.png`, Buffer.from((await Page.captureScreenshot()).data, 'base64'));
  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await new Promise(resolve => setTimeout(resolve, 700));
    await ev(`import('/static/team_working_mode.js').then(m => window.showTeamWorkingModeWorkspace = m.showTeamWorkingModeWorkspace)`);
    await ev(`(() => {
      document.body.innerHTML = '<main id="agentic-state" class="agentic-state"></main>';
      const state = document.getElementById('agentic-state');
      state.innerHTML = '<div class="agentic-block" id="left-chat"><textarea id="chat-draft">draft preserved</textarea><div id="chat-scroll">chat scroll</div></div><div class="agentic-block">right board</div>';
      state.style.cssText = 'display:flex !important;position:fixed;inset:0;z-index:100;width:100vw;height:90vh;background:#222';
    })()`);
    const baseline = await ev(`(() => { const s=document.getElementById('agentic-state'); return {chat:document.getElementById('chat-draft').value, scroll:document.getElementById('chat-scroll').scrollTop, blocks:s.querySelectorAll('.agentic-block').length}; })()`);
    await ev(`window.showTeamWorkingModeWorkspace('review-team')`);
    await ev(`new Promise(r=>setTimeout(r,400))`);
    const mounted = await ev(`(() => { const w=document.getElementById('team-settings-workspace'),s=document.getElementById('agentic-state'); return {workspace:!!w, active:s.classList.contains('team-settings-workspace-active'), chat:document.getElementById('chat-draft')?.value, blocks:s.querySelectorAll('.agentic-block').length, tabs:[...w.querySelectorAll('[role=tab]')].map(x=>x.textContent.trim())}; })()`);
    assert.equal(mounted.workspace, true); assert.equal(mounted.active, true); assert.equal(mounted.chat, baseline.chat);
    console.log('right block display:', await ev(`getComputedStyle(document.querySelectorAll('.agentic-block')[1]).display`));
    await shot('desktop');
    await ev(`document.querySelector('[data-settings-tab="prompts"]').click()`);
    assert.equal(await ev(`document.querySelector('[data-settings-panel="prompts"]').hidden`), false);
    await ev(`document.querySelector('[data-settings-tab="global"]').click()`);
    assert.equal(await ev(`document.querySelector('[data-settings-panel="global"]').hidden`), false);
    await ev(`history.back()`); await ev(`new Promise(r=>setTimeout(r,300))`);
    assert.equal(await ev(`document.getElementById('team-settings-workspace')`), null);
    assert.equal(await ev(`document.getElementById('agentic-state').classList.contains('team-settings-workspace-active')`), false);
    await ev(`history.forward()`); await ev(`new Promise(r=>setTimeout(r,300))`);
    const afterForward = await ev(`!!document.getElementById('team-settings-workspace')`);
    console.log('forward workspace restored:', afterForward);
    assert.equal(afterForward, true);
    assert.equal(await ev(`location.hash`), '#team-settings=review-team');
    await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
    await ev(`window.showTeamWorkingModeWorkspace('review-team')`); await ev(`new Promise(r=>setTimeout(r,300))`);
    const narrow = await ev(`(() => { const w=document.getElementById('team-settings-workspace'),r=w.getBoundingClientRect(); return {width:r.width,viewport:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth}; })()`);
    await shot('mobile');
    assert.ok(narrow.width <= narrow.viewport); assert.equal(narrow.overflow,false);
    await ev(`(async()=>{document.querySelector('.team-settings-close').click(); await new Promise(r=>setTimeout(r,300))})()`);
    await ev(`import('/static/team_availability.js').then(m=>window.showTeamAvailabilityWorkspace=m.showTeamAvailabilityWorkspace)`);
    await ev(`(() => { const agents=Array.from({length:24},(_,i)=>({name:'Agent '+(i+1),agent_type:i%2?'terminal':'claude',role:i%3?'Frontend Dev':'QA Engineer',availability:['available','busy','task_idle','queued','waiting','needs_input'][i%6],available:i%6===0,reason:i%2?'Has assigned work':'Ready',tasks:[{scope:'board',id:100+i,status:i%3?'pending':'in_progress',title:'Fixture task '+(i+1)}],reminder:i%4===0,reminder_interval_seconds:900,subscriber_id:'agent-'+i})); window.__reviewFetch=window.fetch; window.fetch=async (url,opts)=>url.includes('/api/board/review-team/status')?new Response(JSON.stringify({agents,unassigned_tasks:[{scope:'board',id:999,status:'pending',title:'Unassigned fixture'}],summary:{available:4,busy:4,task_idle:4,queued:4,waiting:4,needs_input:4},health_report:['Blocked prerequisite #42 needs rewire','Stale assignment #77'],observed_at:new Date().toISOString()}),{status:200,headers:{'Content-Type':'application/json'}}):window.__reviewFetch(url,opts); })()`);
    await ev(`(async()=>{window.showTeamAvailabilityWorkspace('review-team'); await new Promise(r=>setTimeout(r,500))})()`);
    const teamView = await ev(`(() => { const w=document.getElementById('team-availability-workspace'),c=w?.querySelector('.availability-content'); c.scrollTop=c.scrollHeight-c.clientHeight; const last=[...c.querySelectorAll('.availability-agent')].at(-1); return {mounted:!!w, title:w?.querySelector('h2')?.textContent, agents:w?.querySelectorAll('.availability-agent').length, unassigned:w?.querySelector('.availability-unassigned')?.textContent||'', health:w?.querySelector('.availability-health')?.textContent||'', overflow:document.documentElement.scrollWidth>innerWidth, contentHeight:c?.clientHeight, contentScrollHeight:c?.scrollHeight, contentOverflow:getComputedStyle(c||w).overflowY, lastRowReachable:last?.getBoundingClientRect().bottom <= c?.getBoundingClientRect().bottom + 1}; })()`);
    assert.equal(teamView.mounted,true); assert.equal(teamView.agents,24); assert.ok(teamView.unassigned.includes('Unassigned')); assert.ok(teamView.health.includes('Blocked prerequisite')); assert.equal(teamView.overflow,false); assert.ok(teamView.contentScrollHeight > teamView.contentHeight); assert.equal(teamView.contentOverflow,'auto'); assert.equal(teamView.lastRowReachable,true);
    assert.equal(await ev(`location.hash`), '#team-view=review-team');
    await ev(`history.back()`); await ev(`new Promise(r=>setTimeout(r,300))`);
    assert.equal(await ev(`document.getElementById('team-availability-workspace')`), null);
    await ev(`history.forward()`); await ev(`new Promise(r=>setTimeout(r,400))`);
    assert.equal(await ev(`!!document.getElementById('team-availability-workspace')`), true);
    await shot('team-view-mobile');
    console.log(JSON.stringify({ baseline, mounted, narrow, teamView }));
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
