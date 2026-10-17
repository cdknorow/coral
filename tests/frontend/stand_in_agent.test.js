'use strict';
// The stand-in helper's cleanup must undo only its OWN change to cli_path_claude and never write an
// empty value over a path someone else set (#4354: a stand-in that read an empty cli_path before the
// runner seeded it then "restored" empty, wiping the mock for the rest of the shard). No server: a fake
// api backed by a settings map.
const assert = require('node:assert/strict');
const { launchStandIn } = require('./helpers/stand_in_agent');

// api(method, path, body) over a mutable settings map; records every PUT body.
function makeApi(initialCliPath) {
    const settings = {};
    if (initialCliPath) settings.cli_path_claude = initialCliPath;
    const puts = [];
    const api = async (method, p, body) => {
        if (p === '/api/settings' && method === 'GET') return { status: 200, data: { settings: { ...settings } } };
        if (p === '/api/settings' && method === 'PUT') { puts.push({ ...body }); Object.assign(settings, body); return { status: 200, data: {} }; }
        if (method === 'POST' && p === '/api/sessions/launch') return { status: 200, data: { session_id: 's1', session_name: 'claude-s1' } };
        return { status: 200, data: {} }; // kill, and diagnose's reads
    };
    return { api, settings, puts };
}

(async () => {
    // 1. A non-empty previous path is restored on stop.
    {
        const { api, settings } = makeApi('/custom/claude');
        const h = await launchStandIn(api, { deadlineMs: 50 });
        assert.notEqual(settings.cli_path_claude, '/custom/claude', 'the stand-in replaced the path during the run');
        await h.stop();
        assert.equal(settings.cli_path_claude, '/custom/claude', 'stop restores the previous non-empty path');
    }
    // 2. An empty previous path is NEVER written back (the #4354 bug: no clobber to empty).
    {
        const { api, settings, puts } = makeApi('');
        const h = await launchStandIn(api, { deadlineMs: 50 });
        await h.stop();
        assert.ok(!puts.some(p => p.cli_path_claude === ''), 'stop never PUTs an empty cli_path_claude');
        assert.notEqual(settings.cli_path_claude, '', 'cli_path_claude is not cleared to empty');
    }
    // 3. A value another client set after setup is left untouched (stop undoes only its own change).
    {
        const { api, settings } = makeApi('/custom/claude');
        const h = await launchStandIn(api, { deadlineMs: 50 });
        settings.cli_path_claude = '/someone/else'; // changed since the stand-in's own PUT
        await h.stop();
        assert.equal(settings.cli_path_claude, '/someone/else', 'stop does not overwrite a value changed since its own PUT');
    }
    console.log('stand_in_agent restore: ok');
})().catch(e => { console.error(e); process.exit(1); });
