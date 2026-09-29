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
    await Page.enable(); await Page.navigate({ url: BASE }); for (let i = 0; i < 50; i++) { if (await ev(`!!document.getElementById('live-sessions-list')`)) break; await new Promise(r => setTimeout(r, 100)); }
    await ev(`import('/static/render.js').then(m => window.renderLiveSessions = m.renderLiveSessions)`);
    const metrics = await ev(`(() => {
      window.closeSidebarKebabs = () => {}; window.switchNavTab?.('agents');
      if (!document.getElementById('live-sessions-list')) throw new Error('live sessions list missing');
      window.renderLiveSessions([{session_id:'team-icon-1',name:'Icon agent',agent_type:'codex',board_project:'icon-team',working_directory:'/tmp/icon-team'}]);
      const trigger=document.querySelector('.group-kebab-btn'); trigger.click();
      const menu=trigger.nextElementSibling; document.body.append(menu); menu.style.cssText='display:block;position:fixed;left:8px;top:8px;width:240px';
      const textLeft = item => { const text=[...item.childNodes].find(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim()); const range=document.createRange(); range.selectNodeContents(text); return range.getBoundingClientRect().left; };
      const [team, settings, add] = [...menu.querySelectorAll('.overflow-menu-item')].slice(0, 3);
      const icon = item => item.querySelector('svg, .material-icons');
      const rect = item => icon(item).getBoundingClientRect();
      if (!team || !settings || !add) throw new Error(JSON.stringify({texts:[...menu.querySelectorAll('.overflow-menu-item')].map(x=>x.textContent.replace(/\s+/g,' ').trim())}));
      const tr=rect(team), sr=rect(settings), ar=rect(add);
      return {team:{iconWidth:tr.width,left:tr.left,textLeft:textLeft(team),gap:textLeft(team)-tr.right},settings:{iconWidth:sr.width,left:sr.left,textLeft:textLeft(settings),gap:textLeft(settings)-sr.right},add:{iconWidth:ar.width,left:ar.left,textLeft:textLeft(add),gap:textLeft(add)-ar.right}};
    })()`);
    fs.writeFileSync('/tmp/coral-1320-team-menu.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    for (const row of [metrics.team, metrics.add]) assert.ok(Math.abs(row.iconWidth - 14) <= 1, JSON.stringify(metrics));
    assert.ok(Math.max(metrics.team.left, metrics.settings.left, metrics.add.left) - Math.min(metrics.team.left, metrics.settings.left, metrics.add.left) <= 1, JSON.stringify(metrics));
    assert.ok([metrics.team, metrics.settings, metrics.add].every(row => Math.abs(row.gap - 8) <= 1), JSON.stringify(metrics));
    console.log(JSON.stringify(metrics));
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
