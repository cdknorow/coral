// #6610: a chat message held behind the agent's question. The server answers the composer's frame "terminal_input_held": the send then waits as long as the server holds it (not the usual
// 20 seconds), is delivered by its later ack, and is refused by a later failure; a held frame for another message changes nothing.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    const out = await ev(`(async () => {
      const MOD = (window.__coralStatic || '/static/');
      const g = await import(MOD + 'terminal_gone.js');
      const delays = [];
      const realSet = window.setTimeout;
      window.setTimeout = (fn, ms, ...a) => { delays.push(ms); return realSet(fn, 1e9, ...a); }; // never fires: only the delays are looked at
      const sess = { session_id: 'sid-held' };
      const emit = (type, detail) => document.dispatchEvent(new CustomEvent(type, { detail }));
      const result = {};

      const p1 = g.awaitSocketAccept(sess, [101]);
      const before = delays.length;
      emit('coral:terminal-input-held', { sessionId: 'sid-held', id: 999 });      // another message's
      emit('coral:terminal-input-held', { sessionId: 'sid-held', id: 101, note: '' }); // this one's
      result.heldDelay = delays.slice(before).filter(d => d > 60000)[0] || 0;
      emit('coral:terminal-input-ack', { sessionId: 'sid-held', id: 101 });
      result.ackSettled = await Promise.race([p1, new Promise(r => realSet(() => r('pending'), 100))]);

      const p2 = g.awaitSocketAccept(sess, [202]);
      emit('coral:terminal-input-held', { sessionId: 'sid-held', id: 202 });
      emit('coral:terminal-input-refused', { sessionId: 'sid-held', id: 202, error: 'The agent was closed' });
      result.refusedSettled = await Promise.race([p2, new Promise(r => realSet(() => r('pending'), 100))]);

      const p3 = g.awaitSocketAccept(sess, [303]);
      emit('coral:terminal-input-held', { sessionId: 'other-session', id: 303 }); // another agent's
      emit('coral:terminal-input-ack', { sessionId: 'sid-held', id: 303 });
      result.otherSessionNoted = await Promise.race([p3, new Promise(r => realSet(() => r('pending'), 100))]);
      window.setTimeout = realSet;
      return result;
    })()`);
    assert.equal(out.heldDelay, 31 * 60 * 1000, 'a held send waits as long as the server holds it');
    assert.equal(out.ackSettled, true, 'the later ack delivers it');
    assert.equal(out.refusedSettled, false, 'a later refusal refuses it');
    assert.equal(out.otherSessionNoted, true, 'a held frame for another session does not touch this one');
    console.log('PASS a held chat message waits for its ack or refusal');
  } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
