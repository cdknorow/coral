/* Language-neutral browser client for Coral's allowlisted LSP broker. */

export function offsetToLspPosition(text, offset) {
    const safe = Math.max(0, Math.min(offset, text.length));
    const before = text.slice(0, safe);
    const line = (before.match(/\n/g) || []).length;
    const lineStart = before.lastIndexOf('\n') + 1;
    return { line, character: before.slice(lineStart).length };
}

export function lspPositionToOffset(text, position) {
    const targetLine = Math.max(0, position?.line || 0);
    let offset = 0;
    for (let line = 0; line < targetLine && offset < text.length; line++) {
        const newline = text.indexOf('\n', offset);
        offset = newline < 0 ? text.length : newline + 1;
    }
    return Math.min(text.length, offset + Math.max(0, position?.character || 0));
}

export function pathToFileURI(worktree, filepath) {
    const windows = /^[A-Za-z]:[\\/]/.test(worktree);
    const separator = windows ? '\\' : '/';
    const joined = `${worktree.replace(/[\\/]$/, '')}${separator}${filepath.replace(/^[\\/]/, '')}`;
    const slashPath = joined.replace(/\\/g, '/');
    return `file://${windows ? '/' : ''}${slashPath.split('/').map((part, index) =>
        index === 0 ? part : encodeURIComponent(part)).join('/')}`;
}

export function fileURIToRelativePath(uri, worktree) {
    const parsed = new URL(uri);
    if (parsed.protocol !== 'file:') throw new Error('Unsupported navigation URI');
    let path = decodeURIComponent(parsed.pathname);
    let root = worktree.replace(/\\/g, '/').replace(/\/$/, '');
    if (/^[A-Za-z]:\//.test(root) && path.startsWith('/')) path = path.slice(1);
    if (path === root) return '';
    if (!path.startsWith(`${root}/`)) throw new Error('Location is outside the workspace');
    return path.slice(root.length + 1);
}

export function groupLocations(locations, worktree) {
    const groups = new Map();
    for (const location of locations || []) {
        const uri = location.uri || location.targetUri;
        const range = location.range || location.targetSelectionRange || location.targetRange;
        if (!uri || !range) continue;
        const filepath = fileURIToRelativePath(uri, worktree);
        if (!groups.has(filepath)) groups.set(filepath, []);
        groups.get(filepath).push({
            filepath, range,
            line: range.start.line + 1,
            column: range.start.character + 1,
        });
    }
    return groups;
}

function websocketURL(path) {
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${scheme}//${location.host}${path}`;
}

export class CoralLSPClient {
    constructor(options) {
        this.options = options;
        this.socket = null;
        this.status = 'starting';
        this.capabilities = {};
        this.version = 0;
        this.nextID = 1;
        this.pending = new Map();
        this.changeTimer = null;
        this.hoverGeneration = 0;
        this.closed = false;
        this.opened = false;
        this.uri = pathToFileURI(options.worktree, options.filepath);
        this.workspace = options.worktree;
    }

    async connect() {
        this.closed = false;
        this._setStatus('starting');
        const response = await fetch(`${this.options.baseURL}/language-capabilities?${this.options.query}`);
        const capability = await response.json();
        this.capabilities = capability.capabilities || {};
        this.workspace = capability.workspace || this.workspace;
        this._setStatus(capability.status || 'failed', capability.message);
        if (capability.status !== 'ready' && capability.status !== 'starting') return false;
        this._setStatus('starting');
        return new Promise(resolve => {
            const socket = new WebSocket(websocketURL(`${this.options.baseURL}/lsp?${this.options.query}`));
            this.socket = socket;
            socket.onopen = () => {
                this.version = 1;
                this._notify('textDocument/didOpen', {
                    textDocument: {
                        uri: this.uri,
                        languageId: this.options.language || '',
                        version: this.version,
                        text: this.options.getText(),
                    },
                });
                this.opened = true;
                resolve(true);
            };
            socket.onmessage = event => this._onMessage(event);
            socket.onerror = () => this._setStatus('failed', 'Language server connection failed');
            socket.onclose = () => {
                this.socket = null;
                this.opened = false;
                this._rejectPending(new Error('Language server disconnected'));
                if (!this.closed) this._setStatus('failed', 'Language server disconnected');
            };
        });
    }

    _setStatus(status, message = '') {
        this.status = status;
        this.options.onStatus?.({ status, message, capabilities: this.capabilities });
    }

    _onMessage(event) {
        let message;
        try { message = JSON.parse(event.data); } catch { return; }
        if (message.type === 'status') {
            this.capabilities = message.capabilities || {};
            this.workspace = message.workspace || this.workspace;
            this._setStatus(message.status || 'ready');
            return;
        }
        if (message.type === 'error' && (message.id === undefined || message.id === null)) {
            this._setStatus('failed', message.error?.message || 'Language server error');
            return;
        }
        const pending = this.pending.get(String(message.id));
        if (!pending) return;
        this.pending.delete(String(message.id));
        clearTimeout(pending.timer);
        if (message.type === 'error') {
            const error = new Error(message.error?.message || 'Language server error');
            error.code = message.error?.code;
            pending.reject(error);
        } else {
            pending.resolve(message.result);
        }
    }

    _send(envelope) {
        if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return false;
        this.socket.send(JSON.stringify(envelope));
        return true;
    }

    _notify(method, params) {
        return this._send({ type: 'request', method, params });
    }

    request(method, params, timeout = 10000) {
        if (this.status !== 'ready') return Promise.reject(new Error('Language intelligence is not ready'));
        this.flushChanges();
        const id = this.nextID++;
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                this.pending.delete(String(id));
                this._notify('$/cancelRequest', { id });
                reject(new Error('Language server request timed out'));
            }, timeout);
            this.pending.set(String(id), { resolve, reject, timer });
            if (!this._send({ type: 'request', id, method, params })) {
                clearTimeout(timer);
                this.pending.delete(String(id));
                reject(new Error('Language server disconnected'));
            }
        });
    }

    documentChanged() {
        clearTimeout(this.changeTimer);
        this.changeTimer = setTimeout(() => this.flushChanges(), 120);
    }

    flushChanges() {
        clearTimeout(this.changeTimer);
        this.changeTimer = null;
        if (!this.opened) return;
        this.version++;
        this._notify('textDocument/didChange', {
            textDocument: { uri: this.uri, version: this.version },
            contentChanges: [{ text: this.options.getText() }],
        });
    }

    didSave() {
        this.flushChanges();
        if (this.opened) this._notify('textDocument/didSave', { textDocument: { uri: this.uri } });
    }

    textDocumentPosition(offset) {
        return {
            textDocument: { uri: this.uri },
            position: offsetToLspPosition(this.options.getText(), offset),
        };
    }

    async hover(offset) {
        const generation = ++this.hoverGeneration;
        const result = await this.request('textDocument/hover', this.textDocumentPosition(offset));
        return generation === this.hoverGeneration ? result : null;
    }

    async definition(offset) {
        return this.request('textDocument/definition', this.textDocumentPosition(offset));
    }

    async references(offset) {
        return this.request('textDocument/references', {
            ...this.textDocumentPosition(offset),
            context: { includeDeclaration: true },
        });
    }

    cancelHover() { this.hoverGeneration++; }

    close() {
        this.closed = true;
        clearTimeout(this.changeTimer);
        this.cancelHover();
        if (this.opened) this._notify('textDocument/didClose', { textDocument: { uri: this.uri } });
        this.opened = false;
        this._rejectPending(new Error('Editor closed'));
        this.socket?.close(1000, 'editor closed');
        this.socket = null;
    }

    _rejectPending(error) {
        for (const pending of this.pending.values()) {
            clearTimeout(pending.timer);
            pending.reject(error);
        }
        this.pending.clear();
    }
}

export function hoverMarkup(contents) {
    if (!contents) return '';
    if (typeof contents === 'string') return contents;
    if (Array.isArray(contents)) return contents.map(item =>
        typeof item === 'string' ? item : (item.value || '')).join('\n\n');
    return contents.value || '';
}
