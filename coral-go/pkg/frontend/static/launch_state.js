/* The launch flow's shared state (#3836): one object instead of module-level lets, so the agent config form, the team form, the
 * model lists and the other launch pieces can live in modules of their own and read and set the same values. */
import { LOCAL_SERVER } from './server_base.js';

export const launch = {
    // Which launch mode is active: null (the chooser), 'agent' or 'terminal'.
    mode: null,
    existingTeamBoardNames: new Set(),
    teamAgentModalRow: null,
    // A launch always runs on exactly one server (multi-server hub, spec section 5). `server` is the Server dropdown of the
    // launch modal and `addAgentServer` the server inherited by "add agent to board" / folder-group launches.
    // With hub mode off both stay "local" and nothing touches /api/remote/*.
    server: LOCAL_SERVER,
    serverLocked: false,
    addAgentServer: LOCAL_SERVER,
    teamAgentCounter: 0,
    skipDefaultTeamAgents: false,
};
