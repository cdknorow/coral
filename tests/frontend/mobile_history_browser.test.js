// Phone Chats uses the same list, filters and search controls across breakpoints (#6013).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { BASE } = require('./test_base');
const { framesJs } = require('./helpers/wait');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = process.env.CORAL_SCREENSHOT_DIR;
    const shot = async name => { if (!out) return; fs.mkdirSync(out, { recursive: true }); fs.writeFileSync(`${out}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const size = async width => { await Emulation.setDeviceMetricsOverride({ width, height: 844, deviceScaleFactor: 1, mobile: width <= 767 }); await ev(framesJs(2)); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const original = window.fetch; window.__historyRequests=[]; window.__historyEmpty=false;
            const json = x => Promise.resolve(new Response(JSON.stringify(x), {headers:{'content-type':'application/json'}}));
            window.fetch = (u, o) => {
                const p = new URL(String(u),location.origin); const sid='phone-history-fixture';
                if(p.pathname==='/api/sessions/history') return json({sessions:window.__historyEmpty?[]:[{session_id:sid,summary_title:'Phone history example',source_type:'claude',last_timestamp:'2026-10-21T10:00:00Z'}],total:window.__historyEmpty?0:1,page:1,page_size:50});
                if(p.pathname==='/api/sessions/history/search') { window.__historyRequests.push(p.search); return json({status:'complete',total:1,results:[{session_id:sid,type:'agent',hits:[{role:'user',excerpt:'Find the phone needle',match_offsets:[[15,21]],locator:{session_id:sid,message_index:0}}]}]}); }
                if(p.pathname==='/api/sessions/history/'+sid) return json({session_id:sid,agent_type:'claude',messages:[{type:'user',content:'Find the phone needle'}],total:1,start:0});
                if(p.pathname.startsWith('/api/sessions/history/'+sid+'/')) return json([]);
                return original(u,o);
            };
        })()` });
        await size(390); await Page.navigate({ url: BASE + '/chats' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(()=>true)`);
        await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Phone history example')`, 'fixture history');
        await installAudit(ev);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme); await shot(`${theme}-390-list`);
            {
                assert.ok(await ev(`document.getElementById('history-search').checkVisibility()`), 'Search reachable on phone Chats');
                assert.ok(await ev(`document.getElementById('history-sessions-list').checkVisibility()`), 'chat list reachable');
                assert.equal(await ev(`document.querySelector('#chats-empty-view .welcome-shell').checkVisibility()`), false, 'desktop instruction hidden on phone');
                assert.equal(await ev(`document.documentElement.scrollWidth<=innerWidth`), true, 'no horizontal page overflow');
                const c = await auditRegion(ev, ['#mobile-history-list']);
                assert.ok(c.checked && !c.fails.length, theme+' text contrast '+JSON.stringify(c.fails));
            }
        }
        await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click();true`);
        await waitFor(`document.getElementById('history-session-view').checkVisibility() && document.getElementById('history-messages').textContent.includes('needle')`, 'list row opens transcript');
        await ev(`document.querySelector('.mobile-history-back').click();true`);
        await waitFor(`document.getElementById('history-search').checkVisibility()`, 'Chats returns from list selection');
        await ev(`window.__historySection=document.querySelector('[data-section="history"]'); document.getElementById('hf-adv-toggle').click(); true`);
        assert.ok(await ev(`document.getElementById('hf-advanced').checkVisibility()`), 'advanced filters reachable');
        await ev(`document.querySelector('[data-chat-type="agent"]').click();document.getElementById('history-search').value='needle';document.getElementById('history-search').dispatchEvent(new Event('input',{bubbles:true}));true`);
        await waitFor(`document.querySelector('.chat-search-hit') && window.__historyRequests.some(q=>q.includes('needle'))`, 'search from phone input');
        for (const theme of ['dark','light']) { await setTheme(ev,theme); await shot(`${theme}-390-search`); }
        await size(1280);
        assert.ok(await ev(`document.querySelector('.sidebar').contains(window.__historySection) && window.__historySection===document.querySelector('[data-section="history"]') && document.getElementById('history-search').value==='needle' && document.querySelector('[data-chat-type="agent"]').classList.contains('active')`), 'desktop restores same controls with filter/query state');
        assert.ok(await ev(`(() => { const v=document.getElementById('chats-empty-view').getBoundingClientRect(),s=document.querySelector('#chats-empty-view > .welcome-shell').getBoundingClientRect();return Math.abs((s.top+s.bottom)-(v.top+v.bottom))<2; })()`), 'desktop Chats hero stays vertically centered beside the sidebar');
        await shot('light-1280-search');
        await size(390);
        assert.ok(await ev(`document.querySelector('#mobile-history-list').contains(window.__historySection) && document.getElementById('hf-advanced').checkVisibility()`), 'phone restores same expanded filters');
        await ev(`document.querySelector('.chat-search-hit').click();true`);
        await waitFor(`document.querySelector('#history-messages .chat-search-target')?.textContent.includes('needle')`, 'search hit opens transcript');
        assert.equal(await ev(`document.getElementById('history-search').checkVisibility()`),false,'list hidden while reading transcript');
        assert.equal(await ev(`document.querySelector('.mobile-history-back').getAttribute('aria-label')`), 'Back to chats', 'return button says where it goes');
        const target = await ev(`(() => {const r=document.querySelector('.mobile-history-back').getBoundingClientRect(); return {w:r.width,h:r.height};})()`);
        assert.ok(target.w>=44 && target.h>=44,'Chats return target '+JSON.stringify(target));
        for (const theme of ['dark','light']) { await setTheme(ev,theme); await shot(`${theme}-390-transcript`); }
        await ev(`document.querySelector('.mobile-history-back').focus();true`);
        await client.Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
        await client.Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
        await waitFor(`document.getElementById('history-search').checkVisibility()`, 'keyboard returns to Chats');
        assert.equal(await ev(`document.activeElement.id`),'history-search','return focuses Search');
        assert.ok(await ev(`location.pathname==='/chats' && document.getElementById('history-search').value==='needle'`),'query preserved on return');
        await ev(`history.back();true`);
        await waitFor(`document.getElementById('history-session-view').checkVisibility()`, 'browser Back reopens chat');
        await ev(`history.forward();true`);
        await waitFor(`document.getElementById('history-search').checkVisibility()`, 'browser Forward returns to list');
        await Page.addScriptToEvaluateOnNewDocument({ source: 'window.__historyEmpty=true;' });
        await Page.navigate({url:BASE+'/chats'}); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(()=>true)`);
        await waitFor(`document.getElementById('history-sessions-list').checkVisibility() && document.getElementById('history-sessions-list').textContent.includes('No history found')`, 'empty history');
        for (const theme of ['dark','light']) { await setTheme(ev,theme); await shot(`${theme}-390-empty`); }
        console.log('PASS phone History list, search, advanced filters, same-node resize, hit navigation, keyboard return, browser Back/Forward, empty state, both themes');
    } finally { await client.close(); }
})().catch(e=>{console.error(e);process.exitCode=1;});
