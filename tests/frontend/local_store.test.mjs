// What the dashboard keeps in the browser of what people typed and where they worked (#3681,
// storage audit #3680 findings 2 and 3; coral-hub Security's per-account keying):
//   - on Coral Hub each kind is kept under the signed-in account (a hash, never the email), and a
//     change of account removes the previous account's; data from before the rule is removed at
//     the first Hub sign-in; a local dashboard keeps its keys as they were;
//   - sign-out removes drafts, pending messages, recent folders and stars of every account, and
//     nothing else;
//   - an old draft ages out, the draft of an ended session goes once untouched for a day (a fresh
//     one stays), the drafts are capped by size, an untouched star list ages out;
//   - drafts and stars stored in the old formats still load.
//   node local_store.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'cloud' });
const store = await importUi('local_store');
const { state, loadSessionDrafts, saveSessionDraft } = await importUi('state');
const { recentDirsKey } = await importUi('launch_server');
const LS = globalThis.localStorage;
const keys = () => Object.keys(LS).sort();
const DAY = 24 * 3600 * 1000;

// ── the account tag ────────────────────────────────────────────────────
const a = await store.accountTag('Ana@Example.com');
const b = await store.accountTag('bo@example.com');
assert.match(a, /^[0-9a-f]{16}$/);
assert.equal(a, await store.accountTag(' ana@example.com '), 'case and spaces do not change the tag');
assert.notEqual(a, b);
assert.equal(await store.accountTag(''), '');

// ── a local dashboard keeps its keys ───────────────────────────────────
LS.clear();
LS.setItem('coral-input-drafts', JSON.stringify({ 'live:s1': 'old format draft' }));
LS.setItem('coral-recent-dirs', JSON.stringify(['/r/proj']));
LS.setItem('coral-sidebar-collapsed', '1');
store.setStoreOwner('');
assert.equal(store.storeKey('coral-input-drafts'), 'coral-input-drafts');
assert.equal(recentDirsKey('local'), 'coral-recent-dirs');
loadSessionDrafts();
assert.equal(state.sessionInputText['live:s1'], 'old format draft', 'an old-format draft still loads');
assert.ok(keys().includes('coral-recent-dirs'), 'a local dashboard keeps its recent folders');

// ── on Coral Hub: per account ──────────────────────────────────────────
store.setStoreOwner(a);
assert.ok(!keys().includes('coral-input-drafts') && !keys().includes('coral-recent-dirs'), 'data from before the rule is removed at the first Hub sign-in');
assert.ok(keys().includes('coral-sidebar-collapsed'), 'other settings stay');
assert.equal(store.storeKey('coral-input-drafts'), `coral-input-drafts@${a}`);
assert.equal(recentDirsKey('srv1'), `coral-recent-dirs:srv1@${a}`);
loadSessionDrafts();
saveSessionDraft('live:s2', 'ana typed this');
LS.setItem(recentDirsKey('local'), JSON.stringify(['/r/ana']));
LS.setItem(store.storeKey('coral-pending-messages'), JSON.stringify({ s2: [{ id: 1, text: 'x', at: Date.now() }] }));
store.writeStars(store.storeKey('coral-starred-files-s2'), ['a.go']);
assert.ok(keys().every(k => !k.includes('example.com')), 'the email is never part of a key');

store.setStoreOwner(b);
loadSessionDrafts();
assert.deepEqual(state.sessionInputText, {}, 'another account reads none of the first account’s drafts');
assert.deepEqual(keys().filter(k => k.endsWith('@' + a)), [], 'and the first account’s data is removed');

// ── sign-out ───────────────────────────────────────────────────────────
saveSessionDraft('live:s3', 'bo typed this');
LS.setItem(recentDirsKey('srv2'), JSON.stringify(['/r/bo']));
LS.setItem(store.storeKey('coral-pending-messages'), JSON.stringify({ s3: [] }));
store.writeStars(store.storeKey('coral-starred-files-s3'), ['b.go']);
LS.setItem(`coral-input-drafts@${a}`, '{}'); // a stray of another account
const SS = globalThis.sessionStorage; // the agent UI's panel-request draft (#4116) goes too; other session keys stay
SS.setItem('coral-panel-request:s3', JSON.stringify({ draft: 'a panel request bo typed' }));
SS.setItem('coral-panel-request:s4', '{}');
SS.setItem('coral-update-reloaded', 'v1');
const removed = store.purgeStored();
assert.deepEqual(Object.keys(SS).sort(), ['coral-update-reloaded'], 'sign-out removes the panel-request drafts in sessionStorage, and nothing else there');
assert.ok(removed >= 5, `sign-out removed ${removed}`);
assert.deepEqual(keys(), ['coral-sidebar-collapsed', 'coral-store-owner'], 'sign-out removes every kind for every account, and nothing else');

// ── age-out ────────────────────────────────────────────────────────────
const now = Date.now();
store.writeDrafts({
    'live:old': { t: 'four days old', at: now - 4 * DAY },
    'live:ended-stale': { t: 'ended, two days', at: now - 2 * DAY },
    'live:ended-fresh': { t: 'ended, an hour', at: now - 3600 * 1000 },
    'live:running': { t: 'running, two days', at: now - 2 * DAY },
    'history:h1': { t: 'a history draft', at: now - 2 * DAY },
});
store._resetLivePrune();
let kept = store.ageOutStored({ liveIds: new Set(['running']), now });
assert.deepEqual(Object.keys(kept).sort(), ['history:h1', 'live:ended-fresh', 'live:running'],
    'an old draft ages out; an ended session’s draft goes once left a day; a fresh one and a running one stay');
assert.equal(store.ageOutStored({ liveIds: new Set(), now: now + 1000 }), null, 'the ended-session check runs at most hourly');
kept = store.ageOutStored({ now });
assert.equal(Object.keys(kept).length, 3, 'without a live list only age and size apply');

const big = {};
for (let i = 0; i < 400; i++) big[`live:s${i}`] = { t: 'x'.repeat(1000), at: now - i * 1000 };
const capped = store.ageDrafts(big, { now });
const n = Object.keys(capped).length;
assert.ok(n > 100 && n < 400 && JSON.stringify(capped).length <= store.DRAFTS_MAX_BYTES, `drafts capped by size (${n} kept)`);
assert.ok(capped['live:s0'] && !capped['live:s399'], 'the newest are kept, the oldest go');

LS.setItem(store.storeKey('coral-starred-files-old'), JSON.stringify({ files: ['x'], at: now - 31 * DAY }));
LS.setItem(store.storeKey('coral-starred-files-new'), JSON.stringify({ files: ['y'], at: now - DAY }));
LS.setItem(store.storeKey('coral-starred-files-legacy'), JSON.stringify(['z']));
store.ageOutStored({ now });
assert.ok(!keys().includes(store.storeKey('coral-starred-files-old')), 'an untouched star list ages out');
assert.ok(keys().includes(store.storeKey('coral-starred-files-new')));
assert.deepEqual(store.readStars(store.storeKey('coral-starred-files-legacy')).files, ['z'], 'an old-format star list still loads');

console.log('local_store.test.mjs: ok');
