// Focused QR entry regression. Run with ONLY=mobile_connect_browser.test.js
// against an isolated dev server + Chrome; no production settings are mutated.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');

(async () => {
    const client = await CDP({port: Number(process.env.CDP_PORT || 9222)});
    const {Page, Runtime, Input} = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({expression, returnByValue:true, awaitPromise:true});
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const tick = () => ev('new Promise(r=>setTimeout(r,60))');
    const status = () => ev(`document.getElementById('mobile-connect-status').textContent`);
    const action = label => ev(`[...document.querySelectorAll('#mobile-connect-actions button')].find(b=>b.textContent===${JSON.stringify(label)}).click()`);
    const noQR = async () => {
        assert.equal(await ev(`document.querySelectorAll('#mobile-connect-qr img').length`),0);
        assert.equal(await ev(`document.getElementById('mobile-connect-details').hidden`),true);
        assert.equal(await ev(`document.getElementById('mobile-connect-api-key').textContent`),'');
    };
    const shot = async name => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        const box = await ev(`document.querySelector('#mobile-connect-modal .modal-content').getBoundingClientRect().toJSON()`);
        const {data} = await Page.captureScreenshot({format:'png',clip:{x:box.x,y:box.y,width:box.width,height:box.height,scale:1}});
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});
        fs.writeFileSync(path.join(process.env.CORAL_SCREENSHOT_DIR,name+'.png'),Buffer.from(data,'base64'));
    };
    try {
        await Page.enable();
        await client.Emulation.setDeviceMetricsOverride({width:1200,height:900,deviceScaleFactor:1,mobile:false});
        await Page.navigate({url:BASE}); await Page.loadEventFired();
        await ev(`(async()=>{
            await import('/static/modals.js');
            const {state}=await import('/static/state.js');window.__state=state;
            window.__calls=[];window.__saved=false;window.__effective=false;window.__mode='normal';window.__putStatus=200;
            const real=window.fetch;
            window.fetch=async(url,opts={})=>{
                const u=String(url);
                if(u==='/api/system/privacy'){
                    window.__calls.push({url:u});
                    const response=()=>new Response(JSON.stringify({remote_access_enabled:window.__saved,remote_access_effective:window.__effective,remote_access_restart_required:window.__saved!==window.__effective}),{headers:{'Content-Type':'application/json'}});
                    if(window.__mode==='delay')return new Promise(resolve=>window.__resolvePrivacy=()=>resolve(response()));
                    if(window.__mode==='error')return new Response('{}',{status:503});
                    if(window.__mode==='invalid')return new Response('{}');
                    return response();
                }
                if(u==='/api/settings' && opts.method==='PUT'){
                    window.__calls.push({url:u,body:JSON.parse(opts.body)});
                    if(window.__putStatus===200)window.__saved=true;
                    return new Response('{}',{status:window.__putStatus});
                }
                if(u==='/api/system/network-info'){
                    window.__calls.push({url:u});
                    if(window.__mode==='network-error')return new Response('{}',{status:503});
                    return new Response(JSON.stringify({primary:'192.168.1.50',port:8468}));
                }
                if(u==='/api/system/api-key'){window.__calls.push({url:u});return new Response(JSON.stringify({key:'test-key-not-a-credential'}));}
                return real(url,opts);
            };
            const opener=document.createElement('button');opener.id='qr-test-opener';opener.textContent='Mobile QR';document.body.append(opener);opener.focus();
        })()`);
        await ev('window._showMobileConnectModal()');
        assert.equal(await status(),'Mobile access is disabled. Enable it?');
        assert.deepEqual(await ev('window.__calls.map(c=>c.url)'),['/api/system/privacy']);
        await noQR(); await shot('mobile-qr-disabled');
        // Focus trap excludes controls hidden in the enabled-only details.
        await Input.dispatchKeyEvent({type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9,modifiers:8});
        await Input.dispatchKeyEvent({type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
        assert.equal(await ev('document.activeElement.textContent'),'Cancel');
        await action('Cancel');
        assert.equal(await ev(`document.getElementById('mobile-connect-modal').style.display`),'none');
        assert.equal(await ev('document.activeElement.id'),'qr-test-opener');
        assert.equal(await ev(`window.__calls.filter(c=>c.body).length`),0);
        // Explicit Enable is the sole write; duplicate clicks do not resubmit.
        await ev('window._showMobileConnectModal()');
        await ev(`const b=document.querySelector('#mobile-connect-actions button');b.click();b.click()`);await tick();
        assert.deepEqual(await ev(`window.__calls.filter(c=>c.body).map(c=>c.body)`),[{remote_access_enabled:true}]);
        assert.match(await status(),/Restart Coral/);await noQR();await shot('mobile-qr-restart-required');
        assert.equal(await ev(`window.__calls.some(c=>c.url==='/api/system/api-key')`),false);
        await action('Check again');await tick();await noQR();
        assert.equal(await ev(`window.__calls.filter(c=>c.body).length`),1);
        // Only a fresh effective=true response unlocks network/key/QR details.
        await ev('window.__effective=true');await action('Check again');await tick();
        assert.equal(await ev(`document.getElementById('mobile-connect-details').hidden`),false);
        assert.match(await ev(`document.getElementById('mobile-connect-url').textContent`),/192.168.1.50:8468/);
        assert.equal(await ev(`document.querySelectorAll('#mobile-connect-qr img').length`),1);
        await shot('mobile-qr-enabled');
        // Pending disable immediately withholds old QR and API key.
        await ev('window.__saved=false;window.__calls=[];window._showMobileConnectModal()');
        assert.match(await status(),/finish disabling/);await noQR();
        assert.deepEqual(await ev('window.__calls.map(c=>c.url)'),['/api/system/privacy']);
        // Failures leave access closed; retry reads status, never writes settings.
        await ev('window.__mode="error";window._showMobileConnectModal()');
        assert.match(await status(),/Could not check mobile access/);await noQR();
        await ev('window.__mode="invalid"');await action('Retry');await tick();
        assert.match(await status(),/Could not verify/);await noQR();
        await ev('window.__mode="normal";window.__effective=false');await action('Retry');await tick();
        await ev('window.__putStatus=403');await action('Enable');await tick();
        assert.match(await status(),/Could not enable mobile access \(403\)/);await noQR();
        await ev('window.__saved=true;window.__effective=true;window.__mode="network-error";window._showMobileConnectModal()');
        assert.match(await status(),/Could not load the mobile connection details/);await noQR();
        // Closing during a delayed status check cannot re-open/populate the modal.
        await ev('window.__mode="delay";window._showMobileConnectModal();undefined');
        assert.match(await status(),/Checking mobile access/);
        await Input.dispatchKeyEvent({type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
        await ev('window.__resolvePrivacy()');await tick();
        assert.equal(await ev(`document.getElementById('mobile-connect-modal').style.display`),'none');await noQR();
        // Missing settings default off, including the settings status label.
        await ev(`window.__mode='normal';window.__saved=false;window.__effective=false;window.__state.settings={};window.__state.privacyStatus=null;window.fetch=async u=>String(u).includes('/api/system/privacy')?new Response('{}',{status:503}):new Response('{}');import('/static/modals.js').then(m=>m.showSettingsModal())`);
        assert.equal(await ev(`document.getElementById('settings-remote-access-enabled').checked`),false);
        console.log('PASS mobile QR: default off, no-write open/cancel, explicit single enable, pending enable/disable, effective-only QR, errors/retry, stale close, keyboard/focus and settings fallback');
    } finally {await client.close();}
})().catch(error=>{console.error(error);process.exitCode=1});
