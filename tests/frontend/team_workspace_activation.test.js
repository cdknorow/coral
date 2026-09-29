const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await new Promise(resolve => setTimeout(resolve, 500));
    await ev(`import('/static/agentic_state.js').then(m => window.switchAgenticTab = m.switchAgenticTab)`);
    await ev(`import('/static/team_availability.js').then(m => window.showTeamAvailabilityWorkspace = m.showTeamAvailabilityWorkspace)`);
    await ev(`import('/static/team_working_mode.js').then(m => window.showTeamWorkingModeWorkspace = m.showTeamWorkingModeWorkspace)`);
    await ev(`import('/static/render.js').then(m => window.renderLiveSessions = m.renderLiveSessions)`);
    await ev(`(() => {
      document.body.innerHTML = '<main id="agentic-state" class="agentic-state"><div id="agentic-block-top" class="agentic-block" style="display:flex"><div class="agentic-tab active" id="agentic-tab-board">Board</div><div class="agentic-tab" id="agentic-tab-files">Files</div><div id="agentic-panel-board" class="agentic-panel active">board</div><div id="agentic-panel-files" class="agentic-panel">files</div></div><ul id="live-sessions-list"></ul></main>';
      document.getElementById('agentic-state').style.cssText = 'display:flex !important;position:fixed;inset:0;z-index:100;width:100vw;height:90vh;background:#222';
      window.__calls = []; window.__delayedAlpha = false;
      window.fetch = async url => {
        const request = String(url); window.__calls.push(request);
        if (request.includes('/api/board/alpha/status')) { const response = new Response(JSON.stringify({summary:{available:1},observed_at:new Date().toISOString(),agents:[{name:'Alpha agent',agent_type:'codex',role:'Developer',availability:'available',available:true,reason:'Idle',tasks:[]}],unassigned_tasks:[],health_report:[]})); return window.__delayedAlpha ? new Promise(resolve => setTimeout(() => resolve(response), 350)) : response; }
        if (String(url).includes('/api/board/beta/status')) return new Response(JSON.stringify({summary:{busy:1},observed_at:new Date().toISOString(),agents:[{name:'Beta agent',agent_type:'codex',role:'QA',availability:'busy',available:false,reason:'Working',tasks:[{scope:'board',id:2,status:'in_progress',title:'Beta task'}]}],unassigned_tasks:[],health_report:[]}));
        if (String(url).includes('/working-mode/presets')) return new Response(JSON.stringify({presets:[{id:'none',name:'None',builtin:true,instructions:''}]}));
        if (String(url).includes('/working-mode')) return new Response(JSON.stringify({mode:'none',dependency_guidance:false,custom_instructions:''}));
        if (String(url).includes('/api/settings/prompt-inspection')) return new Response(JSON.stringify({orchestrator:{},worker:{},task:{}}));
        if (String(url).includes('/api/settings')) return new Response(JSON.stringify({settings:{board_health_monitor:'false'}}));
        return new Response('{}');
      };
      window.closeSidebarKebabs = () => {};
      window.confirm = () => false;
    })()`);

    // No agent is selected, and another pane starts active. Opening Team view must activate Board/workspace.
    await ev(`window.switchAgenticTab('files','top'); window.renderLiveSessions([{session_id:'alpha-1',name:'Alpha agent',agent_type:'codex',board_project:'alpha',working_directory:'/tmp/alpha'}]); document.querySelector('.group-kebab-btn').click(); [...document.querySelectorAll('.group-kebab .overflow-menu-item')].find(x => x.textContent.includes('Team view')).click(); new Promise(r => setTimeout(r, 120))`);
    await new Promise(resolve => setTimeout(resolve, 300));
    fs.writeFileSync('/tmp/coral-1314-team-alpha.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    const alpha = await ev(`(() => ({route:location.hash,title:document.querySelector('#team-availability-workspace h2')?.textContent,team:document.querySelector('#team-availability-workspace header p')?.textContent,active:document.querySelector('#agentic-tab-board')?.classList.contains('active'),boardVisible:document.querySelector('#agentic-panel-board')?.classList.contains('active'),agents:[...document.querySelectorAll('#team-availability-workspace .availability-agent strong')].map(x=>x.textContent)}))()`);
    assert.equal(alpha.route, '#team-view=alpha');
    assert.equal(alpha.title, 'Team view');
    assert.equal(alpha.team, 'alpha');
    assert.equal(alpha.active, true);
    assert.equal(alpha.boardVisible, true);
    assert.deepEqual(alpha.agents, ['Alpha agent']);

    // Selecting another team replaces both route and rendered content.
    await ev(`window.renderLiveSessions([{session_id:'beta-1',name:'Beta agent',agent_type:'codex',board_project:'beta',working_directory:'/tmp/beta'}]); document.querySelector('.group-kebab-btn').click(); [...document.querySelectorAll('.group-kebab .overflow-menu-item')].find(x => x.textContent.includes('Team view')).click(); new Promise(r => setTimeout(r, 120))`);
    await new Promise(resolve => setTimeout(resolve, 300));
    fs.writeFileSync('/tmp/coral-1314-team-beta.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    const beta = await ev(`(() => ({route:location.hash,team:document.querySelector('#team-availability-workspace header p')?.textContent,agents:[...document.querySelectorAll('#team-availability-workspace .availability-agent strong')].map(x=>x.textContent),task:document.querySelector('#team-availability-workspace')?.textContent.includes('Beta task')}))()`);
    assert.equal(beta.route, '#team-view=beta');
    assert.equal(beta.team, 'beta');
    assert.deepEqual(beta.agents, ['Beta agent']);
    assert.equal(beta.task, true);
    // A late response for the previous team must not overwrite the active beta workspace.
    await ev(`window.__delayedAlpha = true; window.renderLiveSessions([{session_id:'alpha-2',name:'Alpha agent',agent_type:'codex',board_project:'alpha',working_directory:'/tmp/alpha'}]); document.querySelector('.group-kebab-btn').click(); [...document.querySelectorAll('.group-kebab .overflow-menu-item')].find(x => x.textContent.includes('Team view')).click(); window.renderLiveSessions([{session_id:'beta-2',name:'Beta agent',agent_type:'codex',board_project:'beta',working_directory:'/tmp/beta'}]); document.querySelector('.group-kebab-btn').click(); [...document.querySelectorAll('.group-kebab .overflow-menu-item')].find(x => x.textContent.includes('Team view')).click(); new Promise(r => setTimeout(r, 500))`);
    const stale = await ev(`(() => ({route:location.hash,team:document.querySelector('#team-availability-workspace header p')?.textContent,agents:[...document.querySelectorAll('#team-availability-workspace .availability-agent strong')].map(x=>x.textContent),alphaRequests:window.__calls.filter(x=>x.includes('/alpha/status')).length,betaRequests:window.__calls.filter(x=>x.includes('/beta/status')).length}))()`);
    assert.equal(stale.route, '#team-view=beta');
    assert.equal(stale.team, 'beta');
    assert.deepEqual(stale.agents, ['Beta agent']);
    assert.ok(stale.alphaRequests >= 2); assert.ok(stale.betaRequests >= 2);

    // Team settings must not silently apply edits to the wrong team.
    await ev(`window.showTeamWorkingModeWorkspace('alpha'); new Promise(r => setTimeout(r, 150))`);
    await ev(`document.querySelector('#team-settings-workspace textarea[name="custom_instructions"]').value = 'alpha-only draft'`);
    await ev(`window.showTeamWorkingModeWorkspace('beta'); new Promise(r => setTimeout(r, 80))`);
    assert.equal(await ev(`document.querySelector('#team-settings-workspace .working-mode-team')?.textContent`), 'alpha');
    assert.equal(await ev(`location.hash`), '#team-settings=alpha');
    await ev(`window.confirm = () => true; window.showTeamWorkingModeWorkspace('beta'); new Promise(r => setTimeout(r, 150))`);
    assert.equal(await ev(`document.querySelector('#team-settings-workspace .working-mode-team')?.textContent`), 'beta');
    assert.equal(await ev(`location.hash`), '#team-settings=beta');
    await ev(`window.fetch = window.__reviewFetch; window.confirm = () => false; true`);
    console.log(JSON.stringify({ alpha, beta, settingsSwitchGuarded: true }));
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
