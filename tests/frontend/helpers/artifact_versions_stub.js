// A stubbed server for the artifact viewer's versions (#6480): three versions of release-plan.md, the version list, and the comments of the newest with a carried, an ambiguous and a gone one.
// Shared by artifact_versions_browser and the censuses' version scenes. STUB is page code to run with ev(): it replaces window.fetch.
const H1 = '1'.repeat(64), H2 = '2'.repeat(64), H3 = '3'.repeat(64);
const T = {
    [H1]: ['# Release plan', '', 'Ship on Thursday.', 'The first step is the folder.', 'Terms stay as they are.'].join('\n'),
    [H2]: ['# Release plan', '', 'Ship the Servers page on Friday, with the team behind it.', 'The first step is the folder, then the details.', 'Revoke sits beside Rename on every row.', 'Terms stay as they are.'].join('\n'),
    [H3]: ['# Release plan', '', 'Ship the Servers page on Friday, with the team behind it.', 'The first step is the folder, then the details.', 'Revoke moves into a More menu on every row.', 'Terms stay as they are.'].join('\n'),
};

const STUB = `(() => {
    const real = window.fetch; window.__posts = []; const T = ${JSON.stringify(T)};
    const at = n => new Date(Date.now() - n * 3600000).toISOString();
    const rows = [['${H1}', 1, 30], ['${H2}', 2, 3], ['${H3}', 3, 1]].map(([d, v, h]) => ({ digest: d, version: v, versions: 3, is_latest: v === 3, name: 'release-plan.md', size: 9000 + v * 1500, media_type: 'text/markdown', at: at(h), uploader: 'frontend-dev', role: 'Frontend Dev' }));
    const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
    const who = n => (n === 'Operator' ? { kind: 'owner', issuer: 'local', subject: n, name: n } : { kind: 'agent', issuer: 'local', subject: n, name: n });
    const c = (id, art, ver, from, carried, quote, body, extra) => ({ id, artifact_id: art, parent_id: null, anchor: { quote }, body, author: who(extra && extra.by || 'Operator'), created_at: at(2), mine: true, can: { reply: true, edit: true, delete: true, resolve: true }, version: ver, ...(from ? { from_version: from, carried } : {}), ...(extra || {}) });
    window.fetch = (u, o) => {
        const url = String(u), method = (o && o.method) || 'GET';
        let m = /\\/api\\/artifacts\\/([0-9a-f]{64})\\/versions/.exec(url);
        if (m) return json(rows);
        m = /\\/api\\/artifacts\\/([0-9a-f]{64})\\/comments(\\/\\d+\\/\\w+)?(\\?.*)?$/.exec(url);
        if (m) {
            if (method === 'POST') { window.__posts.push(url); return json({ ...c(1, '${H2}', 2, 2, 'found', 'folder', 'Say what the folder is for.'), resolved_at: at(0) }); }
            const own = m[1];
            const list = own === '${H3}' && /series=1/.test(url) ? [
                c(10, '${H3}', 3, 0, '', 'Revoke moves into a More menu', 'Good, ship it.'),
                c(1, '${H2}', 2, 2, 'found', 'The first step is the folder', 'Say what the folder is for.', { by: 'Security Reviewer' }),
                c(2, '${H2}', 2, 2, 'ambiguous', 'the team behind it', 'Name who is on the team.', { by: 'QA Engineer' }),
                c(3, '${H1}', 1, 1, 'gone', 'Ship on Thursday.', 'Why Thursday?', { by: 'Frontend Dev' })] : [];
            return json({ comments: list, locked: false, you: { kind: 'owner', can_comment: true, can_lock: true }, limits: {} });
        }
        m = /\\/api\\/artifacts\\/([0-9a-f]{64})(\\?v=\\d+)?$/.exec(url);
        if (m && T[m[1]]) return Promise.resolve(new Response(T[m[1]], { status: 200, headers: { 'Content-Type': 'text/markdown' } }));
        return real(u, o);
    };
    return true; })()`;

module.exports = { H1, H2, H3, T, STUB };
