// #6093: empty comments never reserve document width; the same accessible toggle
// works docked, expanded and after reload, with open-thread counts and text selection.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { STUB, HEX } = require('./helpers/comments_scale');
const { setTheme } = require('./helpers/contrast');
const { framesJs } = require('./helpers/wait');
const BASE = require('./test_base').BASE;
const CONTENT = '# Release notes\n\nThe document should have room to read.\n\nSelect this passage to add a comment.\n';
const COMMENTS = [
    {id:1,parent_id:null,anchor:{whole:true},body:'Please check this.',author:{kind:'owner',issuer:'local',subject:'Operator'},created_at:'2026-10-22T00:00:00Z'},
    {id:2,parent_id:1,body:'A reply is not another thread.',author:{kind:'owner',issuer:'local',subject:'Operator'},created_at:'2026-10-22T00:01:00Z'},
    {id:3,parent_id:null,anchor:{whole:true},body:'Already resolved.',author:{kind:'owner',issuer:'local',subject:'Operator'},created_at:'2026-10-22T00:02:00Z',resolved_at:'2026-10-22T00:03:00Z'},
];
(async () => {
    const {client,Page,Emulation,ev,waitFor,PRELOAD,install} = await connect();
    const before = process.env.COMMENTS_CAPTURE_BEFORE === '1';
    const S = `(window.__coralStatic || '/static/')`;
    const ready = `(() => {const t=document.querySelector('#preview-toolbar .ac-toggle');return !!t&&!t.hidden;})()`;
    const state = () => ev(`(() => {const m=document.getElementById('preview-main'),b=document.getElementById('preview-body'),p=m.querySelector('.ac-panel'),t=document.querySelector('#preview-toolbar .ac-toggle');return {text:t.textContent,expanded:t.getAttribute('aria-expanded'),buttonShown:!!t.getClientRects().length&&!t.hidden,controls:t.getAttribute('aria-controls')===p.id,open:p.classList.contains('ac-sheet-open'),inert:p.inert,main:m.clientWidth,body:b.getBoundingClientRect().width,buttonHeight:t.getBoundingClientRect().height};})()`);
    const open = async () => {
        await ev(`window.PP.openArtifactTab('coral://artifacts/${HEX}', {name:'release-notes.md',mediaType:'text/markdown'});true`);
        await waitFor(ready,'comments loaded');
    };
    const load = async (width,theme,comments=[],clear=true) => {
        await Emulation.setDeviceMetricsOverride({width,height:900,deviceScaleFactor:1,mobile:width===390});
        await Emulation.setTouchEmulationEnabled({enabled:width===390});
        await Page.navigate({url:BASE});await Page.loadEventFired();await install();
        await ev(`window.__coralReady.then(()=>true)`);await setTheme(ev,theme);
        await ev(`(async()=>{window.BL=(await import(${S}+'safe_storage.js')).browserLocal;${clear?"window.BL.remove('coral-artifact-comments-open');":''}window.PP=await import(${S}+'preview_pane.js');const st=(await import(${S}+'state.js')).state;st.currentSession={type:'live',name:'fixture',session_id:'comments-fixture',agent_type:'claude'};(await import(${S}+'utils.js')).showView('live-session-view');window.PP.setPreviewSession('comments-fixture');})()`);
        await ev(STUB({filename:'release-notes.md',content:CONTENT,mediaType:'text/markdown',comments}));
        await open();
        if(width!==390) await ev(`if(!document.getElementById('preview-pane').classList.contains('docked-column'))document.getElementById('preview-dock-btn').click();true`);
        await ev(framesJs(2));
    };
    const shot = async name => {
        if(!process.env.CORAL_SCREENSHOT_DIR)return;
        await ev(framesJs(2));fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});
        fs.writeFileSync(path.join(process.env.CORAL_SCREENSHOT_DIR,name+'.png'),Buffer.from((await Page.captureScreenshot({format:'png'})).data,'base64'));
    };
    try {
        await Page.enable();await Page.addScriptToEvaluateOnNewDocument({source:PRELOAD});
        for(const width of [1920,1280,390]) for(const theme of ['dark','light']) {
            await load(width,theme);
            for(const mode of ['docked','expanded']) {
                if(mode==='expanded'){await ev(`document.getElementById('preview-max-btn').click();true`);await ev(framesJs(2));}
                await shot(`${before?'before':'after'}-${mode}-${width}-${theme}`);
                if(before)continue;
                let s=await state();
                assert.equal(s.text,'Comments');assert.equal(s.expanded,'false');assert.ok(s.buttonShown&&s.controls&&s.inert&&!s.open,JSON.stringify(s));
                assert.ok(s.body>=s.main-2,'hidden panel leaves the full document width: '+JSON.stringify(s));
                if(width===390)assert.ok(s.buttonHeight>=44,'phone target');
                await ev(`document.querySelector('#preview-toolbar .ac-toggle').focus();true`);
                await client.Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
                await client.Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
                await waitFor(`document.querySelector('#preview-toolbar .ac-toggle').getAttribute('aria-expanded')==='true'`,'keyboard opens comments');
                s=await state();assert.ok(s.open&&!s.inert);
                await ev(`document.querySelector('#preview-toolbar .ac-toggle').click();true`);
                s=await state();assert.equal(s.expanded,'false');assert.ok(s.inert);
            }
            if(before)continue;
            // An explicit closed choice survives a full browser-page reload, even
            // with open comments; removing the preference restores count defaults.
            await load(width,theme,COMMENTS,false);
            let s=await state();assert.equal(s.text,'Comments (1)');assert.equal(s.expanded,'false');
            await ev(`window.BL.remove('coral-artifact-comments-open');window.PP.closeAllTabs();true`);await open();
            await waitFor(`document.querySelector('#preview-toolbar .ac-toggle').getAttribute('aria-expanded')==='true'`,'open threads start shown');
            s=await state();assert.equal(s.text,'Comments (1)');
            // The saved open choice also survives reload on a phone.
            await ev(`document.querySelector('#preview-toolbar .ac-toggle').click();document.querySelector('#preview-toolbar .ac-toggle').click();true`);
            await load(width,theme,COMMENTS,false);s=await state();assert.equal(s.expanded,'true');
            await ev(`window.BL.remove('coral-artifact-comments-open');window.PP.closeAllTabs();window.__cs=window.__cs.filter(c=>c.resolved_at);true`);await open();
            s=await state();assert.equal(s.text,'Comments');assert.equal(s.expanded,'false','resolved-only starts hidden');
            // Text selection still offers an inline action while the panel is hidden.
            await ev(`(()=>{const root=document.getElementById('preview-body'),w=document.createTreeWalker(root,NodeFilter.SHOW_TEXT);for(let n=w.nextNode();n;n=w.nextNode()){const i=n.nodeValue.indexOf('Select this passage');if(i<0)continue;const r=document.createRange();r.setStart(n,i);r.setEnd(n,i+19);getSelection().removeAllRanges();getSelection().addRange(r);return true;}return false;})()`);
            await waitFor(`(()=>{const b=document.querySelector('#preview-pane .ac-selbtn');return b&&!b.hidden;})()`,'inline Add comment');
            assert.equal(await ev(`document.querySelector('#preview-pane .ac-selbtn').textContent`),'Add comment');
            assert.equal(await ev(`document.querySelector('#preview-pane .ac-selbtn').getAttribute('aria-label').startsWith('Add comment')`),true,'visible action is in its accessible name');
            await ev(`document.querySelector('#preview-pane .ac-selbtn').click();true`);
            await waitFor(`!!document.querySelector('#preview-main .ac-input')`,'selection opens composer');
            s=await state();assert.equal(s.expanded,'true');
            assert.equal(await ev(`document.activeElement===document.querySelector('#preview-main .ac-input')`),true);
            assert.equal(await ev(`document.documentElement.scrollWidth<=innerWidth`),true,'no page overflow');
        }
        console.log(before?'Captured before states':'PASS comments visibility: empty/count defaults, hide/show, full width, keyboard, remembered choices/reload, resolved/replies and inline selection; docked/expanded, 1920/1280/390, both themes');
    } finally {await client.close();}
})().catch(e=>{console.error(e);process.exit(1);});
