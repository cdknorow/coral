/* The Team Artifacts panel's search box, filter menu, sort and chips (#4573): its words, fields and sorts on the shared list bar (list_bar.js, #4744). The server does the work: this only
 * collects what to ask for (team_artifact_filters.js). */
import { EMPTY, KINDS, ORIGINS, SORTS, KIND_LABEL, ORIGIN_LABEL, SORT_LABEL, clean, chips, isFiltered, without } from './team_artifact_filters.js';
import { buildListBar } from './list_bar.js';

// The same fields for the team's list and for one agent's list (#5283): only the words and the ids differ, so the two bars (one per panel, both in the page) never share an id.
const specFor = (personal) => {
    const idp = personal ? 'aa' : 'ta';
    return {
        idp,
        noun: 'artifact',
        label: personal ? 'Search and filter this agent\'s artifacts' : 'Search and filter team artifacts',
        searchLabel: personal ? 'Search this agent\'s artifacts' : 'Search team artifacts',
        placeholder: 'Search artifacts',
        collapsible: true, // the agents tab's pattern (#5293): one icon opens Filters and Sort; the second icon opens or closes every row's details
        expandAll: { expand: 'Expand all details', collapse: 'Collapse all details' },
        sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort artifacts' },
        fields: [
            { type: 'select', key: 'kind', id: `${idp}-kind`, label: 'Type', values: KINDS, labels: KIND_LABEL, any: 'Any type' },
            { type: 'select', key: 'origin', id: `${idp}-origin`, label: 'Origin', values: ORIGINS, labels: ORIGIN_LABEL, any: 'Any origin' },
            { type: 'text', key: 'by', id: `${idp}-by`, className: 'list-bar-by', label: 'Uploaded by', max: 80, placeholder: 'Agent or person', datalist: `${idp}-by-list` },
            { type: 'check', key: 'kept', id: `${idp}-kept`, label: 'Kept only', wrapClass: 'list-bar-field list-bar-kept' },
            { type: 'date', key: 'from', id: `${idp}-from`, label: 'From' },
            { type: 'date', key: 'to', id: `${idp}-to`, label: 'Until' },
        ],
        model: { EMPTY, clean, chips, isFiltered, without },
    };
};

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). personal: the bar of one agent's Artifacts tab. */
export function buildBar(onChange, { personal = false, hooks = {} } = {}) {
    return buildListBar(specFor(personal), onChange, hooks);
}
