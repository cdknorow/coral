// Clear filters/search must restore the real history list, including while a search is pending (#6019).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { BASE } = require('./test_base');
const { framesJs } = require('./helpers/wait');
const { setTheme } = require('./helpers/contrast');
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async name => { const d=process.env.CORAL_SCREENSHOT_DIR; if(!d)return;fs.mkdirSync(d,{recursive:true});fs.writeFileSync(`${d}/${name}.png`,Buffer.from((await Page.captureScreenshot({format:'png'})).data,'base64')); };
    const input = value => ev(`(() => {const e=document.getElementById('history-search');e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    const restored = `document.getElementById('history-sessions-list').checkVisibility() && document.getElementById('chat-search-results').hidden && !document.getElementById('chat-search-results').childElementCount`;
    const clear = async () => {
        await ev(`document.querySelector('.hf-clear-btn').click();true`);
        await waitFor(restored,'Clear restores history list');
        assert.ok(await ev(`!document.getElementById('history-search').value && !new URL(location.href).searchParams.get('q') && document.querySelector('[data-chat-type="all"]').classList.contains('active') && document.getElementById('hf-active-count').style.display==='none'`),'query, chat type and badge reset');
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({source:PRELOAD});
        await Page.addScriptToEvaluateOnNewDocument({source:`(() => {
            const original=window.fetch;window.__searchRequests=[];window.__held=[];window.__empty=false;
            const result={status:'complete',total:1,results:[{session_id:'clear-history',type:'agent',hits:[{role:'user',excerpt:'Search needle',match_offsets:[[7,13]],locator:{session_id:'clear-history',message_index:0}}]}]};
            const json=x=>new Response(JSON.stringify(x),{headers:{'content-type':'application/json'}});
            window.fetch=(u,o)=>{
                const p=new URL(String(u),location.origin);
                if(p.pathname==='/api/sessions/history')return Promise.resolve(json({sessions:window.__empty?[]:[{session_id:'clear-history',summary_title:'Restored history row',source_type:'claude'}],total:window.__empty?0:1,page:1,page_size:50}));
                if(p.pathname==='/api/sessions/history/search'){
                    const q=p.searchParams.get('q');window.__searchRequests.push(q);
                    if(q.startsWith('hold'))return new Promise((resolve,reject)=>window.__held.push({q,release:()=>resolve(json(result)),fail:()=>reject(new Error('Late search failure'))}));
                    return Promise.resolve(json(result));
                }
                return original(u,o);
            };
        })()`});
        for(const width of [1280,390]) for(const theme of ['dark','light']) {
            await Emulation.setDeviceMetricsOverride({width,height:900,deviceScaleFactor:1,mobile:width<=767});
            await Page.navigate({url:BASE+'/chats'});await Page.loadEventFired();await install();await ev(`window.__coralReady.then(()=>true)`);await setTheme(ev,theme);
            await waitFor(`document.getElementById('history-sessions-list').checkVisibility() && document.getElementById('history-sessions-list').textContent.includes('Restored history row')`,'initial history list');
            await ev(`document.getElementById('hf-adv-toggle').click();document.querySelector('[data-chat-type="agent"]').click();true`);
            await input('needle');await waitFor(`document.querySelector('.chat-search-hit')?.checkVisibility()`,'search result');await shot(`${width}-${theme}-search`);
            // The exact reported steps, using the actual Clear filters button.
            await clear();await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Restored history row')`,'restored row');await shot(`${width}-${theme}-cleared`);
            await input('needle');await waitFor(`document.querySelector('.chat-search-hit')?.checkVisibility()`,'second search');
            await input('');await waitFor(restored,'empty search input restores list');
            // A slow success and a slow failure cannot refill the cleared live region.
            for(const fail of [false,true]) {
                const q=fail?'hold-error':'hold-success';await input(q);await waitFor(`window.__held.some(r=>r.q===${JSON.stringify(q)})`,'pending search');
                await clear();
                await ev(`window.__held.find(r=>r.q===${JSON.stringify(q)}).${fail?'fail':'release'}();true`);await ev(framesJs(2));
                assert.ok(await ev(restored),'late '+(fail?'error':'success')+' leaves list visible and results empty');
            }
            await input('hold-old');await waitFor(`window.__held.some(r=>r.q==='hold-old')`,'older query');await clear();
            await input('new query');await waitFor(`document.querySelector('.chat-search-hit')?.checkVisibility()`,'new query');
            await ev(`window.__held.find(r=>r.q==='hold-old').fail();true`);await ev(framesJs(2));
            assert.ok(await ev(`document.querySelector('.chat-search-hit')?.checkVisibility() && !document.getElementById('chat-search-results').textContent.includes('Late search failure')`),'older failure cannot overwrite new results');
            await ev(`window.__empty=true;true`);await clear();await waitFor(`document.getElementById('history-sessions-list').textContent.includes('No history found')`,'empty history after clear');
            console.log('PASS '+width+' '+theme+': Clear filters, empty input, pending success/failure, newer query and empty list');
        }
        // Clear can also win before the first lazy import's continuation runs.
        await Page.navigate({url:BASE+'/chats'});await Page.loadEventFired();await install();await ev(`window.__coralReady.then(()=>true)`);
        assert.equal(await ev(`import((window.__coralStatic||'/static/')+'lazy_panels.js').then(m=>m.loadChatSearch.module===null)`),true,'search is still lazy at initial Chats load');
        await ev(`(async()=>{const base=window.__coralStatic||'/static/';const f=await import(base+'history_filters.js'),s=await import(base+'search_filters.js');s.filterState.q='cancel-before-import';document.getElementById('history-search').value=s.filterState.q;window.__firstSearch=f.loadHistoryFiltered();document.querySelector('.hf-clear-btn').click();await window.__firstSearch;})()`);
        assert.ok(await ev(restored),'Clear before module load completes restores list');
        assert.deepEqual(await ev(`window.__searchRequests`),[],'cancelled first search sends no request');
    } finally {await client.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
