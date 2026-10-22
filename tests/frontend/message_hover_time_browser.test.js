// #6063: real message renderers, existing timestamp values. Mouse, keyboard and touch;
// today vs earlier date and full local title. Optional before/after UI captures.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { swapTheme } = require('./helpers/theme');
const { framesJs } = require('./helpers/wait');
const BASE = require('./test_base').BASE;
(async()=>{
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const before = process.env.TIME_CAPTURE_BEFORE === '1';
    const longChat = process.env.TIME_LONG_CHAT === '1';
    const shot = async name => {
        if (!process.env.SHOT_DIR) return;
        await ev(framesJs(2));
        fs.mkdirSync(process.env.SHOT_DIR,{recursive:true});
        fs.writeFileSync(path.join(process.env.SHOT_DIR,name+'.png'),Buffer.from((await Page.captureScreenshot()).data,'base64'));
    };
    const hover = async selector => {
        const r=await ev(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+Math.min(12,r.height/2)}})()`);
        await client.Input.dispatchMouseEvent({type:'mouseMoved',...r});
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({source:PRELOAD});
        for (const [width,theme] of [[1280,'dark'],[1280,'light'],[390,'dark'],[390,'light']]) {
            await Emulation.setDeviceMetricsOverride({width,height:900,deviceScaleFactor:1,mobile:width===390});
            await Emulation.setTouchEmulationEnabled({enabled:width===390});
            await Page.navigate({url:BASE});await Page.loadEventFired();await install();
            await ev(`(()=>{
                const today=new Date();today.setHours(17,30,0,0);const old=new Date(today);old.setDate(old.getDate()-1);
                window.__sent=[today.toISOString(),old.toISOString()];window.__copiedTimeText=[];Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.__copiedTimeText.push(text)}}});
                const a=window.__m.add('Timestamp demo','',0,{sleeping:false,session_id:'11111111-2222-4333-8444-555555556063'});
                a.msgs=[{type:'user',content:'Please check the release notes.',key:'00000000000000000001',timestamp:window.__sent[0]},
                {type:'assistant',text:'The release notes are ready. The build and the focused checks passed.',key:'00000000000000000002',timestamp:window.__sent[1]}];
                if(${longChat}) for(let i=0;i<12;i++) a.msgs.push({type:i%2?'assistant':'user',content:'Please check the next release item.',text:'The next release item is ready for review.',key:String(i+3).padStart(20,'0'),timestamp:window.__sent[i%2]});
                const original=window.fetch;window.fetch=(url,opts={})=>{
                    if(String(url).includes('/api/board/b-hover-time/'))return Promise.resolve(new Response(JSON.stringify(String(url).includes('/messages/all')?{messages:[{id:1,job_title:'Orchestrator',subscriber_id:'Orchestrator',content:'Please check the release notes.',created_at:window.__sent[0]},{id:2,job_title:'QA Engineer',subscriber_id:'QA Engineer',content:'The release notes and checks are ready.',created_at:window.__sent[1]}],total:2,before_count:0}:[]),{headers:{'Content-Type':'application/json'}}));
                    return original(url,opts);
                };
            })()`);
            await select('Timestamp demo','11111111-2222-4333-8444-555555556063');
            await waitFor(`document.querySelectorAll('#live-history-messages .chat-bubble').length===${longChat?14:2}`,'chat fixture');
            await swapTheme(ev,theme);
            // Headless Chrome can report hover:none at desktop width too.
            // Visibility follows that capability; width alone doesn't give a mouse.
            const noHover = await ev(`matchMedia('(hover: none)').matches`);
            console.log(`timestamps ${width} ${theme}: hover ${noHover?'none':'available'}`);
            await ev(`document.querySelector('#live-history-messages').scrollTop=0;true`);
            if(!before){
                assert.equal(await ev(`(()=>{const b=document.querySelector('#live-history-messages .chat-bubble.assistant');return getComputedStyle(b).marginBottom})()`),width===390?'46px':'22px','existing prose action row reserves the same spacing');
                await client.Input.dispatchMouseEvent({type:'mouseMoved',x:0,y:0});
                await ev(`document.activeElement?.blur();true`);
                await waitFor(`getComputedStyle(document.querySelector('#live-history-messages .chat-bubble.assistant .message-time')).opacity === '${noHover?'1':'0'}'`,'time follows the current hover capability after leaving the message');
                assert.equal(await ev(`(()=>{const a=document.querySelector('#live-history-messages .chat-bubble.human .chat-save-btn').getBoundingClientRect();const b=document.querySelector('#live-history-messages .chat-bubble.assistant').getBoundingClientRect();return a.bottom<=b.top+1})()`),true,'footer never covers the next message');
            }
            await hover('#live-history-messages .chat-bubble.assistant');
            if(!before){
                const times=await ev(`[...document.querySelectorAll('#live-history-messages .message-time')].map(t=>({text:t.textContent,title:t.title,datetime:t.dateTime,opacity:getComputedStyle(t).opacity}))`);
                assert.equal(times.length,longChat?14:2);assert.equal(times[0].text,'17:30');assert.match(times[1].text,/17:30/);assert.notEqual(times[1].text,'17:30');
                assert.equal(times[1].datetime,(await ev(`window.__sent[1]`)));assert.equal(times[1].title,await ev(`new Intl.DateTimeFormat(undefined,{dateStyle:'full',timeStyle:'long'}).format(new Date(window.__sent[1]))`));
                assert.equal(times[1].opacity,'1');
                assert.equal(await ev(`(()=>{const b=document.querySelector('#live-history-messages .chat-bubble.assistant');const t=b.querySelector('.message-time').getBoundingClientRect(),s=b.querySelector('.chat-save-btn').getBoundingClientRect(),c=b.querySelector('.chat-copy-btn').getBoundingClientRect();return t.right<=s.left+1&&s.right<=c.left+1})()`),true,'timestamp and actions do not overlap');
                await ev(`document.querySelector('#live-history-messages .chat-bubble.assistant .chat-copy-btn').click();true`);
                await waitFor(`window.__copiedTimeText.length===1`,'copy original message');
                assert.equal(await ev(`window.__copiedTimeText[0]`),'The release notes are ready. The build and the focused checks passed.','Copy excludes timestamp text');
                await ev(`document.querySelector('#live-history-messages .chat-bubble.assistant .chat-copy-btn').focus();true`);
                await client.Input.dispatchMouseEvent({type:'mouseMoved',x:0,y:0});
                assert.equal(await ev(`getComputedStyle(document.querySelector('#live-history-messages .chat-bubble.assistant .message-time')).opacity`),'1','keyboard focus shows chat time');
                await waitFor(`getComputedStyle(document.querySelector('#live-history-messages .chat-bubble.assistant .chat-save-btn')).opacity==='1'`,'focus reveals the whole action row');
            }
            await shot('chat-'+width+'-'+theme);
            await ev(`(async()=>{const u=window.__coralStatic||'/static/';(await import(u+'utils.js')).showView('messageboard-view');(await import(u+'message_board.js')).selectBoardProject('b-hover-time');})()`);
            await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length===2`,'board fixture');
            await hover('#mb-messages .mb-message[data-message-id="2"]');
            if(!before){
                assert.equal(await ev(`document.querySelector('#mb-messages [data-message-id="1"] .message-time').textContent`),'17:30');
                assert.equal(await ev(`getComputedStyle(document.querySelector('#mb-messages [data-message-id="2"] .message-time')).opacity`),'1');
                await ev(`document.querySelector('#mb-messages [data-message-id="2"] .mb-save-btn').focus();true`);
                await client.Input.dispatchMouseEvent({type:'mouseMoved',x:0,y:0});
                assert.equal(await ev(`getComputedStyle(document.querySelector('#mb-messages [data-message-id="2"] .message-time')).opacity`),'1','keyboard focus shows board time');
                assert.equal(await ev(`document.documentElement.scrollWidth>innerWidth`),false,'no page overflow');
            }
            await shot('board-'+width+'-'+theme);
        }
        console.log('PASS message timestamps: chat/board, mouse/focus/touch, local today/older, both widths/themes');
    } finally {await client.close();}
})().catch(e=>{console.error(e);process.exit(1)});
