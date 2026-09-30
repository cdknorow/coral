const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
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
    await Page.enable(); await Page.navigate({ url: BASE }); await new Promise(r => setTimeout(r, 500));
    await ev(`import('/static/team_availability.js').then(m => window.showTeamAvailabilityWorkspace = m.showTeamAvailabilityWorkspace)`);
    await ev(`(() => {
      document.body.innerHTML = '<main id="agentic-state" class="agentic-state"><div class="agentic-block"></div></main>';
      const s=document.getElementById('agentic-state'); s.style.cssText='display:flex!important;position:fixed;left:0;top:0;height:100vh;background:#222';
      window.fetch = async () => new Response(JSON.stringify({summary:{busy:1},observed_at:new Date().toISOString(),agents:[{name:'Frontend Development Engineer With A Long Readable Name',agent_type:'codex',role:'Frontend Developer',availability:'busy',available:false,reason:'Working through a long task list',tasks:[{scope:'board',id:1351,status:'in_progress',title:'Implement responsive Team view rows without collapsing names into single characters'},{scope:'board',id:1352,status:'pending',title:'Verify task actions remain visible at narrow pane widths'}]}],unassigned_tasks:[],health_report:[]}));
    })()`);
    const measure = async width => {
      await ev(`(() => { const s=document.getElementById('agentic-state'); s.style.width='${width}px'; s.style.minWidth='${width}px'; s.style.maxWidth='${width}px'; window.showTeamAvailabilityWorkspace('responsive-team'); return new Promise(r=>setTimeout(r,180)); })()`);
      return ev(`(() => { const w=document.getElementById('team-availability-workspace'), c=w.querySelector('.availability-content'), card=w.querySelector('.availability-agent'), name=w.querySelector('.availability-agent-heading strong'), task=w.querySelector('li'); const range=document.createRange(); range.selectNodeContents(name); return {paneWidth:w.getBoundingClientRect().width, cardWidth:card.getBoundingClientRect().width, cardHeight:card.getBoundingClientRect().height, nameWidth:name.getBoundingClientRect().width, nameLines:range.getClientRects().length, nameText:name.textContent, taskVisible:task.getBoundingClientRect().height>0, overflow:c.scrollWidth>c.clientWidth, grid:getComputedStyle(card).gridTemplateColumns}; })()`);
    };
    const wide = await measure(760); fs.writeFileSync('/tmp/coral-1351-team-wide.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    const narrow = await measure(360); fs.writeFileSync('/tmp/coral-1351-team-narrow.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    assert.ok(wide.nameLines <= 3, JSON.stringify(wide)); assert.ok(narrow.nameLines <= 3, JSON.stringify(narrow));
    assert.ok(wide.cardHeight < 340 && narrow.cardHeight < 450, JSON.stringify({wide,narrow}));
    assert.equal(wide.taskVisible, true); assert.equal(narrow.taskVisible, true);
    assert.equal(wide.overflow, false); assert.equal(narrow.overflow, false);
    assert.equal(narrow.grid.split(' ').length, 1); assert.ok(wide.grid.split(' ').length >= 2);
    console.log(JSON.stringify({wide,narrow}));
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
