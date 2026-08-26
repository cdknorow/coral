/*
 * Behavioural tests for CoralLSPClient.
 *
 * lsp_client.test.mjs covers the pure helpers (position conversion, URIs,
 * grouping). Nothing exercised the client class itself, so the spec's
 * "Frontend Tests" items — cancellation, stale-hover suppression, version
 * ordering, missing-server and reconnect states — were unverified.
 *
 * The client talks to three globals (fetch, WebSocket, location); each test
 * installs fakes for them before importing the module under test.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const source = await readFile(new URL('./lsp_client.js', import.meta.url), 'utf8');
const moduleURL = `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`;

globalThis.location = { protocol: 'http:', host: 'localhost:8420' };

// FakeSocket records everything the client sends and lets a test drive the
// server side of the conversation.
class FakeSocket {
    static OPEN = 1;
    static CLOSED = 3;

    constructor(url) {
        this.url = url;
        this.readyState = FakeSocket.OPEN;
        this.sent = [];
        this.closeCalls = [];
        FakeSocket.last = this;
        // onopen is assigned by the caller immediately after construction.
        setTimeout(() => this.onopen?.(), 0);
    }

    send(payload) { this.sent.push(JSON.parse(payload)); }

    close(code, reason) {
        this.closeCalls.push({ code, reason });
        this.readyState = FakeSocket.CLOSED;
    }

    // Test-side helpers.
    deliver(message) { this.onmessage?.({ data: JSON.stringify(message) }); }
    drop() { this.readyState = FakeSocket.CLOSED; this.onclose?.(); }
    envelopes(method) { return this.sent.filter(m => m.method === method); }
}

globalThis.WebSocket = FakeSocket;

const { CoralLSPClient } = await import(moduleURL);

function stubCapabilities(body) {
    globalThis.fetch = async () => ({ json: async () => body });
}

const READY_CAPABILITIES = {
    status: 'ready',
    capabilities: { hover: true, definition: true, references: true },
};

// newClient connects a client whose editor buffer is `text`, and marks it ready
// by delivering the broker's status envelope.
async function newClient(text = 'package main\n', capabilities = READY_CAPABILITIES) {
    stubCapabilities(capabilities);
    const statuses = [];
    const client = new CoralLSPClient({
        baseURL: '/api/sessions/live/agent',
        query: 'session_id=s1',
        worktree: '/work',
        filepath: 'main.go',
        language: 'go',
        getText: () => client.text,
        onStatus: state => statuses.push(state),
    });
    client.text = text;
    const connected = await client.connect();
    const socket = FakeSocket.last;
    if (connected) {
        socket.deliver({ type: 'status', status: 'ready', capabilities: capabilities.capabilities });
    }
    return { client, socket, statuses, connected };
}

test('didOpen carries version 1 and the current buffer', async () => {
    const { client, socket } = await newClient('package main\n');
    const [open] = socket.envelopes('textDocument/didOpen');
    assert.ok(open, 'client did not send didOpen');
    assert.equal(open.params.textDocument.version, 1);
    assert.equal(open.params.textDocument.text, 'package main\n');
    assert.equal(open.params.textDocument.uri, 'file:///work/main.go');
    assert.equal(open.params.textDocument.languageId, 'go');
    client.close();
});

test('document versions increase monotonically across edits', async () => {
    const { client, socket } = await newClient();
    for (const text of ['a\n', 'ab\n', 'abc\n']) {
        client.text = text;
        client.flushChanges();
    }
    const versions = socket.envelopes('textDocument/didChange')
        .map(m => m.params.textDocument.version);
    assert.deepEqual(versions, [2, 3, 4], 'versions must be strictly increasing after didOpen v1');

    const texts = socket.envelopes('textDocument/didChange')
        .map(m => m.params.contentChanges[0].text);
    assert.deepEqual(texts, ['a\n', 'ab\n', 'abc\n'], 'each didChange must carry the buffer at that version');
    client.close();
});

test('unsaved edits are flushed before a semantic request is issued', async () => {
    const { client, socket } = await newClient('old\n');
    client.text = 'new unsaved text\n';
    client.documentChanged(); // debounced; must not be lost

    const pending = client.hover(0);
    const order = socket.sent.map(m => m.method);
    const changeIndex = order.indexOf('textDocument/didChange');
    const hoverIndex = order.indexOf('textDocument/hover');
    assert.ok(changeIndex !== -1, 'pending edit was never synchronized');
    assert.ok(changeIndex < hoverIndex, 'hover was sent before the unsaved edit reached the server');
    assert.equal(
        socket.envelopes('textDocument/didChange')[0].params.contentChanges[0].text,
        'new unsaved text\n');

    socket.deliver({ type: 'response', id: socket.envelopes('textDocument/hover')[0].id, result: null });
    await pending;
    client.close();
});

test('a timed-out request cancels itself with the matching id and rejects', async () => {
    const { client, socket } = await newClient();
    const pending = client.request('textDocument/hover', {}, 20);
    const [hover] = socket.envelopes('textDocument/hover');

    await assert.rejects(pending, /timed out/);

    const [cancel] = socket.envelopes('$/cancelRequest');
    assert.ok(cancel, 'timeout did not cancel the outstanding request');
    assert.equal(cancel.params.id, hover.id,
        'cancellation must reference the id the broker knows this request by');

    // A late reply for a cancelled id must not throw or resolve anything.
    socket.deliver({ type: 'response', id: hover.id, result: { late: true } });
    client.close();
});

test('a superseded hover resolves to null so stale cards cannot render', async () => {
    const { client, socket } = await newClient();
    const first = client.hover(0);
    const second = client.hover(1);
    const hovers = socket.envelopes('textDocument/hover');
    assert.equal(hovers.length, 2);

    // Answer the newer request first, then the older one.
    socket.deliver({ type: 'response', id: hovers[1].id, result: { contents: 'newer' } });
    socket.deliver({ type: 'response', id: hovers[0].id, result: { contents: 'older' } });

    assert.deepEqual(await second, { contents: 'newer' });
    assert.equal(await first, null, 'a superseded hover must not deliver its result');
    client.close();
});

test('cancelHover suppresses an in-flight hover result', async () => {
    const { client, socket } = await newClient();
    const pending = client.hover(0);
    client.cancelHover();
    const [hover] = socket.envelopes('textDocument/hover');
    socket.deliver({ type: 'response', id: hover.id, result: { contents: 'ignored' } });
    assert.equal(await pending, null);
    client.close();
});

test('a missing language server reports unavailable and opens no socket', async () => {
    const before = FakeSocket.last;
    const { connected, statuses } = await newClient('x\n', {
        status: 'unavailable',
        capabilities: {},
        message: 'gopls is not installed',
    });

    assert.equal(connected, false, 'client must not report a usable connection');
    const final = statuses[statuses.length - 1];
    assert.equal(final.status, 'unavailable');
    assert.equal(final.message, 'gopls is not installed');
    assert.equal(FakeSocket.last, before, 'no WebSocket should be opened when the server is unavailable');
});

test('requests are refused while the client is not ready', async () => {
    const { client } = await newClient('x\n', { status: 'unavailable', capabilities: {} });
    await assert.rejects(client.request('textDocument/hover', {}), /not ready/);
});

test('an unexpected disconnect rejects pending requests and surfaces a failed state', async () => {
    const { client, socket, statuses } = await newClient();
    const pending = client.request('textDocument/definition', {}, 5000);

    socket.drop();

    await assert.rejects(pending, /disconnected/);
    const final = statuses[statuses.length - 1];
    assert.equal(final.status, 'failed');
    assert.match(final.message, /disconnected/);
    assert.equal(client.opened, false, 'a dropped socket must clear the open-document flag');
});

test('closing the editor sends didClose, rejects pending work, and closes cleanly', async () => {
    const { client, socket, statuses } = await newClient();
    const pending = client.request('textDocument/references', {}, 5000);
    const statusCount = statuses.length;

    client.close();

    await assert.rejects(pending, /Editor closed/);
    assert.equal(socket.envelopes('textDocument/didClose').length, 1,
        'the document lease must be released on close');
    assert.equal(socket.closeCalls.length, 1);
    assert.equal(socket.closeCalls[0].code, 1000);
    assert.equal(statuses.length, statusCount,
        'an intentional close must not raise a failure state');
});

test('requests issued after a disconnect reject instead of hanging', async () => {
    const { client, socket } = await newClient();
    socket.drop();
    // status is 'failed' after a drop, so the guard rejects before send.
    await assert.rejects(client.request('textDocument/hover', {}), /not ready/);
});

test('a broker error envelope rejects only its own request and carries the code', async () => {
    const { client, socket } = await newClient();
    const failing = client.request('textDocument/definition', {}, 5000);
    const surviving = client.request('textDocument/references', {}, 5000);
    const [definition] = socket.envelopes('textDocument/definition');

    socket.deliver({
        type: 'error', id: definition.id,
        error: { code: 'outside_workspace', message: 'location outside workspace' },
    });

    await assert.rejects(failing, error => {
        assert.equal(error.code, 'outside_workspace');
        assert.match(error.message, /outside workspace/);
        return true;
    });

    const [references] = socket.envelopes('textDocument/references');
    socket.deliver({ type: 'response', id: references.id, result: [] });
    assert.deepEqual(await surviving, [], 'an unrelated request must be unaffected');
    client.close();
});

test('an id-less error envelope is treated as a connection-level failure', async () => {
    const { client, socket, statuses } = await newClient();
    socket.deliver({ type: 'error', error: { code: 'server_error', message: 'gopls crashed' } });
    const final = statuses[statuses.length - 1];
    assert.equal(final.status, 'failed');
    assert.equal(final.message, 'gopls crashed');
    client.close();
});
