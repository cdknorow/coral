/* The Team Artifacts panel's search box, filter menu, sort and chips (#4573): its words, fields and sorts on the shared list bar (list_bar.js, #4744). The server does the work: this only
 * collects what to ask for (team_artifact_filters.js). */
import { EMPTY, KINDS, ORIGINS, SORTS, KIND_LABEL, ORIGIN_LABEL, SORT_LABEL, clean, chips, isFiltered, without } from './team_artifact_filters.js';
import { buildListBar } from './list_bar.js';

const SPEC = {
    idp: 'ta',
    noun: 'artifact',
    label: 'Search and filter team artifacts',
    searchLabel: 'Search team artifacts',
    placeholder: 'Search artifacts',
    sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort artifacts' },
    fields: [
        { type: 'select', key: 'kind', id: 'ta-kind', label: 'Type', values: KINDS, labels: KIND_LABEL, any: 'Any type' },
        { type: 'select', key: 'origin', id: 'ta-origin', label: 'Origin', values: ORIGINS, labels: ORIGIN_LABEL, any: 'Any origin' },
        { type: 'text', key: 'by', id: 'ta-by', className: 'list-bar-by', label: 'Uploaded by', max: 80, placeholder: 'Agent or person', datalist: 'ta-by-list' },
        { type: 'check', key: 'kept', id: 'ta-kept', label: 'Kept only', wrapClass: 'list-bar-field list-bar-kept' },
        { type: 'date', key: 'from', id: 'ta-from', label: 'From' },
        { type: 'date', key: 'to', id: 'ta-to', label: 'Until' },
    ],
    model: { EMPTY, clean, chips, isFiltered, without },
};

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). */
export function buildBar(onChange) {
    return buildListBar(SPEC, onChange);
}
