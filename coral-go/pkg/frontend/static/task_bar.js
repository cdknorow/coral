/* The Tasks panel's search box, filter menu, sort and chips (#4744): its words, fields and sorts on the shared list bar (list_bar.js). The server does the work: this only collects what
 * to ask for (task_filters.js). */
import { EMPTY, STATUSES, PRIORITIES, SORTS, STATUS_LABEL, PRIORITY_LABEL, SORT_LABEL, clean, chips, isFiltered, without } from './task_filters.js';
import { buildListBar } from './list_bar.js';

const SPEC = {
    idp: 'tk',
    noun: 'task',
    label: 'Search and filter tasks',
    searchLabel: 'Search tasks',
    placeholder: 'Search tasks',
    sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort tasks' },
    fields: [
        { type: 'select', key: 'status', id: 'tk-status', label: 'Status', values: STATUSES, labels: STATUS_LABEL, any: 'Any status' },
        { type: 'select', key: 'priority', id: 'tk-priority', label: 'Priority', values: PRIORITIES, labels: PRIORITY_LABEL, any: 'Any priority' },
        { type: 'text', key: 'assignee', id: 'tk-assignee', className: 'list-bar-by', label: 'Agent', max: 80, placeholder: 'Agent name', datalist: 'tk-assignee-list' },
        { type: 'date', key: 'from', id: 'tk-from', label: 'Updated since' },
    ],
    model: { EMPTY, clean, chips, isFiltered, without },
};

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). */
export function buildTaskBar(onChange) {
    return buildListBar(SPEC, onChange);
}
