// The launch flow's shared state (#3836): one object that modals.js and the modules split out of it read and set. It starts as the
// chooser with every target server local, and each import sees the same object.
//   node launch_state.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { launch } = await importUi('launch_state');
const { LOCAL_SERVER } = await importUi('server_base');
assert.deepEqual({ ...launch, existingTeamBoardNames: [...launch.existingTeamBoardNames] }, {
    mode: null, existingTeamBoardNames: [], teamAgentModalRow: null,
    server: LOCAL_SERVER, serverLocked: false, addAgentServer: LOCAL_SERVER,
    teamAgentCounter: 0, skipDefaultTeamAgents: false,
}, 'the chooser, every launch target local, nothing pending');
launch.mode = 'agent';
assert.equal((await importUi('launch_state')).launch.mode, 'agent', 'every importer sees the same object');
console.log('launch_state: ok');
