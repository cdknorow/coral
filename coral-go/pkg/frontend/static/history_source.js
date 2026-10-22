/* A linked computer's read-only history on Coral Hub. Local dashboards keep
 * their own history; a remote selection is explicit in the picker and URL. */
import { state } from './state.js';
import { isCloud, serverFetch, serverUrl, currentServer } from './server_base.js';

export function historyServer() {
    if (!isCloud()) return 'local';
    if (state.historySourceServer) return state.historySourceServer;
    const online = (state.servers || []).filter(s => s.id !== 'local' && s.status === 'online');
    const preferred = online.find(s => s.id === currentServer()) || online[0];
    if (preferred) state.historySourceServer = preferred.id;
    return preferred?.id || null;
}

export function setHistorySource(server) {
    if (!isCloud()) return !server || server === 'local';
    if (server && !(state.servers || []).some(s => s.id !== 'local' && s.id === server)) return false;
    state.historySourceServer = server || null;
    return true;
}

export function historyRoute(id, query = '') {
    const route = id ? { view: 'chats', id } : { view: 'chats', query };
    const server = historyServer();
    if (isCloud() && server) route.server = server;
    return route;
}

export function historyUrl(url) {
    const server = historyServer();
    if (!server) throw new Error('No computers online.');
    return serverUrl(url, server);
}

// Historical edits and summary generation are tunnel Never. Even a programmatic
// invocation cannot send them through this read-only source.
export async function historyFetch(url, options) {
    const server = historyServer();
    if (!server) throw new Error('No computers online.');
    if (isCloud() && (options?.method || 'GET').toUpperCase() !== 'GET') throw new Error('History is read-only through Coral Hub.');
    const response = await serverFetch(server, url, options);
    if (!isCloud()) return response;
    const checkSource = () => {
        if (server !== historyServer()) throw new DOMException('The selected computer changed.', 'AbortError');
    };
    checkSource();
    // The source can change while the body is being decoded as well as while
    // waiting for headers. Never paint an old computer's reply into a new one.
    for (const method of ['json', 'text']) {
        const read = response[method].bind(response);
        response[method] = async (...args) => { const data = await read(...args); checkSource(); return data; };
    }
    return response;
}
