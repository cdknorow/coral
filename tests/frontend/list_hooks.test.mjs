// The redraw hook (#3835): other modules ask render.js for a redraw of the agent list through list_hooks.js, which imports nothing of
// render.js. Before render.js registers its renderer a redraw does nothing and does not throw; after, it draws from the current
// sessions (an empty list when there are none); registering again replaces the renderer.
//   node list_hooks.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { state } = await importUi('state');
const hooks = await importUi('list_hooks');

assert.doesNotThrow(() => hooks.redrawLiveList(), 'a redraw before registration does nothing');
const seen = [];
hooks.registerLiveListRenderer(sessions => seen.push(sessions));
state.liveSessions = [{ name: 'a' }, { name: 'b' }];
hooks.redrawLiveList();
assert.deepEqual(seen, [[{ name: 'a' }, { name: 'b' }]], 'it draws from the current sessions');
state.liveSessions = undefined;
hooks.redrawLiveList();
assert.deepEqual(seen[1], [], 'with no sessions it draws an empty list');
const later = [];
hooks.registerLiveListRenderer(sessions => later.push(sessions.length));
state.liveSessions = [{ name: 'c' }];
hooks.redrawLiveList();
assert.deepEqual(later, [1], 'a new registration replaces the renderer');
assert.equal(seen.length, 2, 'and the old one is not called again');
console.log('list_hooks: ok');
