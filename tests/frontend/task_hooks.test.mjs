// The task view hook (#3837): the subagent dialog redraws the task list and closes the shared task dialog through task_hooks.js,
// which imports nothing of tasks.js. Before tasks.js registers, both do nothing and do not throw; after, they call what it
// registered; registering again replaces it.
//   node task_hooks.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const hooks = await importUi('task_hooks');
assert.doesNotThrow(() => { hooks.redrawBoardTaskList(); hooks.closeTaskDetailModal(); }, 'before registration both do nothing');
const calls = [];
hooks.registerTaskView({ renderBoardTaskList: () => calls.push('render'), hideTaskDetailModal: () => calls.push('hide') });
hooks.redrawBoardTaskList(); hooks.closeTaskDetailModal();
assert.deepEqual(calls, ['render', 'hide'], 'they call what tasks.js registered');
const later = [];
hooks.registerTaskView({ renderBoardTaskList: () => later.push('render2'), hideTaskDetailModal: () => later.push('hide2') });
hooks.redrawBoardTaskList(); hooks.closeTaskDetailModal();
assert.deepEqual(later, ['render2', 'hide2'], 'a new registration replaces the old');
assert.equal(calls.length, 2, 'and the old one is not called again');
console.log('task_hooks: ok');
