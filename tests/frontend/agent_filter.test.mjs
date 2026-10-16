// The sidebar search text and group collapse (#3836): group_collapse.js asks agent_filter.js whether a search is on instead of
// reading render.js's filter. A collapsed group reports collapsed; while a search has text it reports open (so matches inside it
// stay visible); toggling flips it and asks for a redraw; Enter or Space on the header toggles it, other keys and keys meant for
// a button inside the header do nothing.
//   node agent_filter.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const filter = await importUi('agent_filter');
const hooks = await importUi('list_hooks');
const collapse = await importUi('group_collapse');

assert.equal(filter.getAgentFilter(), '');
assert.equal(filter.isAgentFiltering(), false);
filter.setAgentFilter('abc');
assert.equal(filter.getAgentFilter(), 'abc');
assert.equal(filter.isAgentFiltering(), true);
filter.setAgentFilter('');

let redraws = 0;
hooks.registerLiveListRenderer(() => { redraws++; });
assert.equal(collapse._isGroupCollapsed('team-a'), false, 'nothing collapsed to begin with');
collapse.toggleGroupCollapse('team-a');
assert.equal(redraws, 1, 'a toggle redraws the list');
assert.equal(collapse._isGroupCollapsed('team-a'), true);
assert.equal(collapse._isGroupCollapsed('team-b'), false, 'other groups are unaffected');
filter.setAgentFilter('x');
assert.equal(collapse._isGroupCollapsed('team-a'), false, 'while a search has text, a collapsed group shows its matches');
filter.setAgentFilter('');
assert.equal(collapse._isGroupCollapsed('team-a'), true);
collapse.toggleGroupCollapse('team-a');
assert.equal(collapse._isGroupCollapsed('team-a'), false, 'a second toggle opens it');

const header = { dataset: { groupName: 'team-c' } };
const key = (k, target) => ({ key: k, target, preventDefault() { this.prevented = true; } });
let ev = key('Enter', header);
collapse.groupHeaderKey(ev, header);
assert.equal(collapse._isGroupCollapsed('team-c'), true, 'Enter on the header toggles it');
assert.equal(ev.prevented, true);
collapse.groupHeaderKey(key(' ', header), header);
assert.equal(collapse._isGroupCollapsed('team-c'), false, 'Space toggles it back');
collapse.groupHeaderKey(key('a', header), header);
collapse.groupHeaderKey(key('Enter', {}), header);
assert.equal(collapse._isGroupCollapsed('team-c'), false, 'another key, or a key meant for a button inside, does nothing');
const attrs = collapse._collapseAttrs(true);
assert.ok(attrs.includes('aria-expanded="false"') && attrs.includes('data-on-keydown="group-header-key"'));
console.log('agent_filter: ok');
