// Pure-node unit test for the hub sidebar/server-status helpers (no browser needed):
//   node server_status.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.location = { protocol: 'http:', host: 'hub.example' };
    const m = await import(pathToFileURL(path.join(dir, 'server_status.js')).href);
    const { state } = await import(pathToFileURL(path.join(dir, 'state.js')).href);

    // Hub off: nothing is rendered, whatever state.servers holds.
    state.hub = false;
    state.servers = [{ id: 'ws1', label: 'WS', status: 'unreachable' }];
    assert.strictEqual(m.showServerLabels(), false);
    assert.strictEqual(m.serverBadgeHtml('ws1'), '');
    assert.strictEqual(m.serverStripHtml(), '');
    const ents = [['b', 1], ['@ws1/a', 2]];
    assert.strictEqual(m.localFirst(ents), ents);

    // Hub on, no remotes: still identical to a standalone server.
    state.hub = true;
    state.servers = [];
    assert.strictEqual(m.showServerLabels(), false);
    assert.strictEqual(m.serverBadgeHtml('local'), '');
    assert.strictEqual(m.serverStripHtml(), '');
    state.servers = [{ id: 'local', label: 'Local', status: 'online' }];
    assert.strictEqual(m.showServerLabels(), false);

    // Hub on with remotes.
    state.servers = [{ id: 'ws1', label: 'Work <b>', status: 'online' }, { id: 'ws2', label: 'Cloud', status: 'unreachable' }, { id: 'ws3', label: 'Old', status: 'unauthorized' }];
    assert.strictEqual(m.showServerLabels(), true);
    assert.strictEqual(m.serverStatus('ws2'), 'unreachable');
    assert.strictEqual(m.serverStatus('local'), 'online');
    assert.strictEqual(m.serverStatus('nope'), 'unknown');
    const badge = m.serverBadgeHtml('ws1');
    assert.ok(badge.includes('server-online') && badge.includes('Work &lt;b&gt;') && !badge.includes('<b>'));
    assert.ok(m.serverBadgeHtml('local').includes('Local'));
    assert.notStrictEqual(m.serverBadgeHtml('ws1'), m.serverBadgeHtml('ws2'));
    const strip = m.serverStripHtml();
    assert.ok(strip.indexOf('Local') < strip.indexOf('Cloud'));
    assert.ok(strip.includes('Unreachable') && strip.includes('Key rejected'));
    assert.deepStrictEqual(m.localFirst([['@ws1/a', 1], ['b', 2], ['@ws2/c', 3], ['d', 4]]).map(e => e[0]), ['b', 'd', '@ws1/a', '@ws2/c']);

    // Hover text: status, address, agent count, last contact, error when down.
    const down = m.serverHoverText('ws2');
    assert.ok(/^Cloud: Unreachable/.test(down) && /agents?/.test(down) && /Last contact:/.test(down), 'one wording, Last contact, online or not (#3152): ' + down);
    assert.ok(!/Last seen/.test(down));
    assert.ok(/This server \(the hub\)/.test(m.serverHoverText('local')));

    // Cloud Hub update notice (#3059): only available/required show anything, the
    // link is the constant releases page, and server-sent text is escaped.
    for (const update of [undefined, 'current', 'unknown', 'bogus']) {
        assert.strictEqual(m.updateNotice({ update, latest_coral: '9.9.9' }), null, String(update));
        assert.strictEqual(m.updateBadgeHtml({ update }), '');
    }
    assert.strictEqual(m.updateNotice({ update: 'available', latest_coral: '1.4.4' }).text, 'Update available: 1.4.4');
    assert.strictEqual(m.updateNotice({ update: 'required', coral_version: '1.3.40', latest_coral: '1.4.4' }).text,
        'Update required: this Coral (1.3.40) is too old for some Coral Hub features. Update to 1.4.4.');
    assert.ok(/Update to the latest release\.$/.test(m.updateNotice({ update: 'required', coral_version: '1.3.40' }).text));
    const hostile = { update: 'required', coral_version: '<img src=x onerror=1>', latest_coral: '"><script>x</script>' };
    const ub = m.updateBadgeHtml(hostile);
    assert.ok(!ub.includes('<img') && !ub.includes('<script') && !ub.includes('&lt;'), 'a field that is not a version is not shown at all (#4133): ' + ub);
    assert.strictEqual(m.updateNotice(hostile).text, 'Update required: this Coral is too old for some Coral Hub features. Update to the latest release.');
    // Not a version, whatever it says or however long: dropped, never shown (the notice falls back to the generic text).
    for (const bad of ['Update required: call 555-0100', 'v1.4', '1.4.4 now', '1.4.4-' + 'a'.repeat(41), '1234567890.1.1', '1.4.4\nline two', 42, null, {}, ['1.4.4']]) {
        assert.strictEqual(m.updateNotice({ update: 'available', latest_coral: bad }).text, 'Update available', JSON.stringify(bad));
        assert.ok(!/\d{1,3}\.\d/.test(m.updateNotice({ update: 'required', coral_version: bad }).text), JSON.stringify(bad));
    }
    // A version, with or without v, with a prerelease: shown.
    for (const good of ['1.4.70', 'v1.4.70', '1.4.71-rc1', '2.0.0-beta.2']) assert.strictEqual(m.updateNotice({ update: 'available', latest_coral: good }).text, 'Update available: ' + good, good);
    assert.ok(ub.includes('href="https://coralhub.dev/coral.html#download"') && ub.includes('rel="noopener noreferrer"') && ub.includes('target="_blank"'));
    assert.strictEqual((ub.match(/href=/g) || []).length, 1, 'the only link is the constant one');
    const prevServers = state.servers;
    state.servers = [{ id: 'ws1', label: 'Work', status: 'online', update: 'available', latest_coral: '1.4.4' }, { id: 'ws2', label: 'Cloud', status: 'online', update: 'current' }];
    assert.ok(/Update available: 1\.4\.4/.test(m.serverHoverText('ws1')));
    assert.ok(!/Update/.test(m.serverHoverText('ws2')));
    const us = m.serverStripHtml();
    assert.strictEqual((us.match(/server-update-badge/g) || []).length, 1, 'one badge, on ws1 only');
    state.servers = prevServers;

    // Stale sessions.
    assert.strictEqual(m.isStaleSession({ stale: true }), true);
    assert.strictEqual(m.isStaleSession({}), false);
    assert.ok(/Cloud is unreachable/.test(m.staleReason({ server: 'ws2' })));

    // Error messages.
    assert.ok(/cannot be decrypted/.test(m.describeServerError(409, { error: 'x', status: 'key_unreadable' })));
    assert.ok(/already exists/.test(m.describeServerError(409, { error: 'server id already exists' })));
    assert.ok(/rejected the API key/.test(m.describeServerError(502, { status: 'unauthorized', error: 'remote rejected API key' })));
    assert.ok(/Could not reach/.test(m.describeServerError(502, { status: 'unreachable', error: 'remote unreachable' })));
    assert.ok(/boom/.test(m.describeServerError(400, { error: 'boom' })));

    // Misc.
    assert.strictEqual(m.formatLastSeen(null), 'never');
    assert.strictEqual(m.formatLastSeen('2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:05:00Z')), '5m ago');
    assert.ok(m.isValidServerId('work-1') && !m.isValidServerId('local') && !m.isValidServerId('A') && !m.isValidServerId(''));

    // The machine menu's details (#5487): the version is cut before "+", its full string is the tooltip and the accessible name; one Update control, "Update to <version>"; no Collapse button here.
    assert.strictEqual(m.shortVersion('1.4.129-dev+f9fef71f.dirty'), '1.4.129-dev');
    assert.strictEqual(m.shortVersion('1.4.134'), '1.4.134');
    assert.strictEqual(m.shortVersion('+odd'), '+odd', 'a leading plus is not a build suffix');
    assert.strictEqual(m.shortVersion(''), '');
    {
        state.hub = true; state.servers = [{ id: 'm1', label: 'pro', status: 'online', version: '1.4.129-dev+f9fef71f.dirty', url: 'http://x', last_seen: '2026-10-20T00:00:00Z', update: 'available', latest_coral: '1.4.134' }];
        const html = m.serverDetailsHtml(state.servers[0]);
        assert.ok(/<dd class="server-version"[^>]*title="1\.4\.129-dev\+f9fef71f\.dirty"[^>]*aria-label="Version 1\.4\.129-dev, full build 1\.4\.129-dev\+f9fef71f\.dirty"[^>]*>1\.4\.129-dev<\/dd>/.test(html), 'short version with the full build as title and name: ' + html);
        assert.ok(/<span>Update to <strong>1\.4\.134<\/strong><\/span>/.test(html), 'the update row says what it updates to');
        assert.strictEqual((html.match(/server-update-badge/g) || []).length, 1, 'one Update control in the details');
        const header = m.serverGroupHeaderHtml(state.servers[0], 3, false, true);
        assert.ok(!/>(Collapse|Expand)</.test(header), 'no Collapse or Expand button in the menu');
        assert.ok(/aria-expanded="true"/.test(header), 'the header toggles the group and says so');
        assert.strictEqual((header.match(/server-open-btn/g) || []).length, 1, 'Open in Servers once, in the launch row');
        assert.ok(!/data-server-launch="folder"/.test(header) && /To use a folder, choose it when you launch an agent\./.test(header), 'a remote has no Open folder, only the hint');
        const rel = m.serverDetailsHtml({ id: 'm2', label: 'rel', status: 'online', version: '1.4.134', last_seen: '2026-10-20T00:00:00Z' });
        assert.ok(/<dd>1\.4\.134<\/dd>/.test(rel) && !/server-version/.test(rel), 'a release version has no build suffix and no tooltip');
        state.servers = []; state.hub = false;
    }

    console.log('server_status.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
