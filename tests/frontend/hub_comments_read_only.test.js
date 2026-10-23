// Through Coral Hub an artifact's comments can be read and not written (#6245): the tunnel lists the comments GET and never a post, reply, resolve or lock (pkg/tunnel/routes.go). So the list the panel
// draws from the hub has no composer, no reply, edit, delete or resolve and no lock, and says where to write; the same server's answer is untouched on a local Coral (the browser suites cover that).
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.location = { protocol: 'https:', host: 'hub.example' };
    globalThis.document = { addEventListener() {}, getElementById: id => (id === 'coral-boot' ? { textContent: '{"mode":"cloud"}' } : null) };
    const urls = [];
    globalThis.fetch = async (u) => {
        urls.push(String(u));
        const body = { comments: [{ id: 1, parent_id: null, anchor: { whole: true }, body: 'Looks right.', author: { kind: 'owner', subject: 'Operator' }, mine: true, can: { reply: true, edit: true, delete: true, resolve: true } }],
            locked: false, you: { kind: 'owner', can_comment: true, can_lock: true, reason: '' }, limits: {} };
        return { ok: true, status: 200, text: async () => JSON.stringify(body) };
    };
    const api = await import(pathToFileURL(path.join(dir, 'artifact_comments_api.js')).href);
    const HEX = 'e'.repeat(64);
    const d = await api.listComments(HEX, 'ws1', undefined);
    assert.strictEqual(urls.length, 1);
    assert.ok(urls[0].startsWith(`/api/remote/ws1/api/artifacts/${HEX}/comments`), urls[0]);
    assert.strictEqual(d.comments.length, 1, 'the comments are still read');
    assert.strictEqual(d.comments[0].body, 'Looks right.');
    assert.strictEqual(d.canComment, false, 'no composer through the hub');
    assert.strictEqual(d.canLock, false, 'no lock through the hub');
    assert.strictEqual(d.reason, 'Comments can be added on the computer itself.');
    assert.deepStrictEqual(d.comments[0].can, { reply: false, edit: false, delete: false, resolve: false }, 'no reply, edit, delete or resolve through the hub');
    console.log('PASS hub_comments_read_only: the hub list is read-only and says where to write');
})().catch(e => { console.error(e); process.exit(1); });
