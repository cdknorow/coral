// Condition waits for CDP suites (#3118). A suite waits for what it needs to be true, not for a guessed
// number of milliseconds: a fixed sleep that is long enough on a quiet machine is a coin flip at --shards 3
// or under a CPU throttle. `ev` is the suite's own evaluate (Runtime.evaluate with returnByValue and
// awaitPromise, throwing on an exception).
//
//   const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
//   await coralReady();                                    // first live list rendered, feed up, settings in
//   await coralReady('startup', 'liveList', 'liveListLoaded', 'history', 'settings');  // the same, for a suite that stubs /ws/coral
//   'liveList' is the first render of the agent list (the feed's first update can cause it); 'liveListLoaded' is the
//   page start's HTTP list answers applied. Wait for 'liveListLoaded' before injecting state (a hub, servers, fake
//   agents), or a late list answer lands on top of it. coralReady() with no arguments includes it.
//   await coralReady('agentSelected', sessionId);          // that agent's chat page rendered or terminal attached
//   await waitFor(`document.querySelectorAll('.row').length === 2`, 'both rows');
//   await waitStable(`document.querySelectorAll('.row').length`, 'the row count', 300);

const POLL_MS = 20;
const tick = () => new Promise(r => setTimeout(r, POLL_MS));

// Polls `expression` in the page until it is truthy and returns that value. On timeout, throws naming
// `what`, the last value seen, and `diag` (another expression) if given. With `soft: true` it returns the
// last value instead of throwing, for suites that report through their own check() so the failing
// check still prints its own message. QA_WAIT_DEBUG=<file> appends every soft timeout to that file (a soft
// wait that times out on a passing run is waiting for the wrong thing and costs its whole timeout).
async function waitFor(ev, expression, what, { timeout = 5000, diag = null, soft = false } = {}) {
    const until = Date.now() + timeout;
    let last;
    for (;;) {
        last = await ev(expression);
        if (last) return last;
        if (Date.now() > until) {
            if (soft) { if (process.env.QA_WAIT_DEBUG) require('node:fs').appendFileSync(process.env.QA_WAIT_DEBUG, `${require('node:path').basename(process.argv[1])}: soft timeout: ${what}\n`); return last; }
            const extra = diag ? ' ' + JSON.stringify(await ev(diag).catch(e => String(e))) : '';
            throw new Error(`timed out after ${timeout}ms waiting for ${what} (last: ${JSON.stringify(last)})${extra}`);
        }
        await tick();
    }
}

// Waits until `expression` gives the same value (compared as JSON) for `quietMs` in a row, and returns it.
// For things with no single "done" signal, e.g. a list the app may re-render from a late startup load.
async function waitStable(ev, expression, what, quietMs = 300, { timeout = 5000, accept = () => true } = {}) {
    const until = Date.now() + timeout;
    let prev, since = 0;
    for (;;) {
        const v = JSON.stringify(await ev(expression));
        if (v === prev && accept(JSON.parse(v ?? 'null'))) { if (Date.now() - since >= quietMs) return JSON.parse(v ?? 'null'); }
        else { prev = v; since = Date.now(); }
        if (Date.now() > until) {
            if (process.env.QA_WAIT_DEBUG) require('node:fs').appendFileSync(process.env.QA_WAIT_DEBUG, `${require('node:path').basename(process.argv[1])}: unsettled: ${what}\n`);
            throw new Error(`timed out after ${timeout}ms waiting for ${what} to settle (last: ${v})`);
        }
        await tick();
    }
}

// Resolves when the app says it is ready (window.__coralReady, the app hook from #3118): the first live-list
// render, the feed connected, the first history list and settings loaded. `step` names a per-view hook on
// the same object (e.g. 'agentSelected'); its arguments follow. A build without the hook fails loudly
// rather than falling back to a sleep.
async function coralReady(ev, step = null, ...args) {
    const timeout = 10000;
    await waitFor(ev, `!!window.__coralReady`, 'window.__coralReady (the app readiness hook)', { timeout });
    const call = step ? `window.__coralReady.${step}(...${JSON.stringify(args)})` : `window.__coralReady`;
    // Never hang a suite: a hook that does not resolve fails with what it was waiting for.
    const ok = await ev(`Promise.race([Promise.resolve(${call}).then(() => true), new Promise(r => setTimeout(() => r(false), ${timeout}))])`);
    if (!ok) throw new Error(`timed out after ${timeout}ms waiting for window.__coralReady${step ? '.' + step + '(' + args.map(a => JSON.stringify(a)).join(', ') + ')' : ''}` +
        ` (parts: ${JSON.stringify(await ev(`window.__coralReady.parts || null`).catch(() => null))})`);
}

// Animation frames, bounded. A page that is not visible (document.visibilityState 'hidden') runs no
// requestAnimationFrame callbacks: on the Mac, headless Chrome hides the page after a key event sent through
// Input.dispatchKeyEvent, and a wait made of frames then never finishes (#3703, #3705). run_cdp_suites.mjs now
// keeps every suite's pages visible (helpers/keep_page_visible.js, #3707); as a second line of defence a frame
// wait here gives up after maxMs and resolves false instead of hanging, and true when the frames ran.
// framesJs(n, maxMs) is the in-page expression (a Promise), for use inside a suite's own page script:
//   await ${framesJs(2)};          // inside an ev(`...`) template
// n may be a number or the name of an in-page variable. Test files never call requestAnimationFrame
// themselves: raf_hygiene.test.js refuses it, and a wait on a condition is waitFor.
const framesJs = (n = 2, maxMs = 250) => `new Promise(r => { let k = ${n}; const step = () => (--k > 0 ? requestAnimationFrame(step) : r(true)); requestAnimationFrame(step); setTimeout(() => r(false), ${maxMs}); })`;

// Two animation frames: enough for anything the app applied synchronously (live-list diffs, fixtures) or
// deferred to requestAnimationFrame to be on screen and laid out. Bounded, see above.
const nextFrame = ev => ev(framesJs(2));

module.exports = {
    waitFor, waitStable, coralReady, nextFrame, framesJs,
    bind: ev => ({
        nextFrame: () => nextFrame(ev),
        waitFor: (expression, what, opts) => waitFor(ev, expression, what, opts),
        waitStable: (expression, what, quietMs, opts) => waitStable(ev, expression, what, quietMs, opts),
        coralReady: (step, ...args) => coralReady(ev, step, ...args),
    }),
};
