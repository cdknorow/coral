/* The linked-computer chooser for the cloud Chats page. */
import { state } from './state.js';
import { isCloud } from './server_base.js';
import { historyServer, setHistorySource } from './history_source.js';
import { registerActions } from './actions.js';
import { filterState } from './search_filters.js';
import { importOnce } from './import_once.js';
import { setHistoryPage } from './history_state.js';

let drawnSource = null;
export function refreshHistorySource() {
    const row = document.getElementById('history-computer-row');
    const select = document.getElementById('history-computer');
    if (!row || !select) return;
    row.hidden = !isCloud();
    if (!isCloud()) return;
    const server = historyServer();
    if (server !== drawnSource) {
        drawnSource = server;
        importOnce('./history_filters.js').then(m => m.populateHfTagSelect());
    }
    const computers = (state.servers || []).filter(s => s.id !== 'local');
    select.replaceChildren(...computers.map(s => {
        const option = document.createElement('option');
        option.value = s.id;
        option.textContent = (s.label || s.name || s.id) + (s.status === 'online' ? '' : ' (offline)');
        option.disabled = s.status !== 'online';
        return option;
    }));
    if (!server) {
        const option = document.createElement('option');
        option.value = ''; option.textContent = 'No computers online';
        select.prepend(option);
    }
    select.value = server || '';
    select.disabled = !computers.some(s => s.status === 'online');
}

registerActions({
    'history-computer': el => {
        if (!setHistorySource(el.value)) return;
        if (state.currentSession?.type === 'history') state.currentSession = null;
        state.historySessionsList = [];
        filterState.tagIds = []; // tag ids belong to their computer
        document.getElementById('hf-tag-pills')?.replaceChildren();
        setHistoryPage(1);
        window.switchNavTab?.('history');
    },
});
