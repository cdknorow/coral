/* The History tab's search, filter and paging (moved out of app.js, #5084; no behaviour change). */
import { refreshHistorySource } from './history_source_ui.js';
import { setHistoryPage, getHistoryPage } from './history_state.js';
import { loadHistorySessionsPaged } from './api.js';
import { filterState, serializeToUrl, countActiveFilters, hasActiveFilters, resetFilters } from './search_filters.js';
import { loadAllTags } from './tags.js';
import { escapeHtml } from './utils.js';
import { loadChatSearch } from './lazy_panels.js';

let filterGeneration = 0;

export function loadHistoryPage(page) {
    setHistoryPage(page);
    loadHistoryFiltered();
}

export function loadHistoryFiltered() {
    refreshHistorySource();
    serializeToUrl(getHistoryPage());
    const generation = ++filterGeneration;
    const query = filterState.q, page = getHistoryPage();
    let loaded;
    if (query) {
        loaded = loadChatSearch().then(m => {
            // Clear or a newer filter change can win while the module loads.
            if (generation === filterGeneration) return m.searchChats(query, page);
        }, () => undefined);
    } else {
        loadChatSearch.module?.searchChats('');
        loaded = loadHistorySessionsPaged(page, 50);
    }
    updateFilterBadge();
    return loaded;
}

export async function populateHfTagSelect() {
    const tags = await loadAllTags(true);
    const sel = document.getElementById('hf-tag-add');
    if (!sel || !tags) return;
    sel.innerHTML = '<option value="">+ tag</option>';
    for (const tag of tags) {
        const opt = document.createElement('option');
        opt.value = tag.id;
        opt.textContent = tag.name;
        sel.appendChild(opt);
    }
}

function renderFilterTagPills() {
    const container = document.getElementById('hf-tag-pills');
    if (!container) return;
    const sel = document.getElementById('hf-tag-add');
    const tagMap = {};
    if (sel) {
        for (const opt of sel.options) {
            if (opt.value) tagMap[parseInt(opt.value)] = opt.textContent;
        }
    }
    container.innerHTML = filterState.tagIds.map(id => {
        const name = tagMap[id] || `Tag ${id}`;
        return `<span class="hf-tag-pill" data-tag-id="${Number(id)}">
            ${escapeHtml(name)}
            <span class="hf-tag-remove">&times;</span>
        </span>`;
    }).join('');

    container.querySelectorAll('.hf-tag-remove').forEach(btn => {
        btn.addEventListener('click', (e) => {
            const tagId = parseInt(e.target.closest('[data-tag-id]').dataset.tagId);
            filterState.tagIds = filterState.tagIds.filter(id => id !== tagId);
            renderFilterTagPills();
            const logicRow = document.getElementById('hf-tag-logic-row');
            if (logicRow) logicRow.style.display = filterState.tagIds.length > 1 ? '' : 'none';
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    });
}

function updateFilterBadge() {
    const badge = document.getElementById('hf-active-count');
    if (!badge) return;
    const n = countActiveFilters();
    badge.textContent = String(n);
    badge.style.display = n > 0 ? '' : 'none';
}

export function syncFilterDomToState() {
    const searchInput = document.getElementById('history-search');
    if (searchInput) searchInput.value = filterState.q;

    const dateFrom = document.getElementById('hf-date-from');
    if (dateFrom) dateFrom.value = filterState.dateFrom;
    const dateTo = document.getElementById('hf-date-to');
    if (dateTo) dateTo.value = filterState.dateTo;

    const durMin = document.getElementById('hf-dur-min');
    if (durMin && filterState.minDurationSec != null)
        durMin.value = String(Math.round(filterState.minDurationSec / 60));
    const durMax = document.getElementById('hf-dur-max');
    if (durMax && filterState.maxDurationSec != null)
        durMax.value = String(Math.round(filterState.maxDurationSec / 60));

    document.querySelectorAll('[data-chat-type]').forEach(b => {
        b.classList.toggle('active', b.dataset.chatType === (filterState.chatType || 'all'));
    });
    document.querySelectorAll('[data-source]').forEach(b => {
        if (b.dataset.source === 'all')
            b.classList.toggle('active', filterState.sourceTypes.length === 0);
        else
            b.classList.toggle('active', filterState.sourceTypes.includes(b.dataset.source));
    });
    document.querySelectorAll('[data-logic]').forEach(b =>
        b.classList.toggle('active', b.dataset.logic === filterState.tagLogic));
    document.querySelectorAll('[data-mode]').forEach(b =>
        b.classList.toggle('active', b.dataset.mode === filterState.ftsMode));

    if (hasActiveFilters()) {
        const panel = document.getElementById('hf-advanced');
        if (panel) panel.style.display = '';
    }

    const ftsModeRow = document.getElementById('hf-fts-mode-row');
    if (ftsModeRow) ftsModeRow.style.display = filterState.q ? '' : 'none';

    renderFilterTagPills();
    updateFilterBadge();
}

/** The history filters: the search box, the advanced panel, the mode, source and chat-type buttons, tags, dates, durations and Clear. */
export function wireHistoryFilters() {
    // ── Filter event wiring ─────────────────────────────────────────────

    // Search bar with debounce
    const searchInput = document.getElementById('history-search');
    if (searchInput) {
        let debounceTimer;
        searchInput.addEventListener('input', () => {
            clearTimeout(debounceTimer);
            debounceTimer = setTimeout(() => {
                filterState.q = searchInput.value.trim();
                setHistoryPage(1);
                const ftsModeRow = document.getElementById('hf-fts-mode-row');
                if (ftsModeRow)
                    ftsModeRow.style.display = filterState.q ? '' : 'none';
                loadHistoryFiltered();
            }, 300);
        });
    }

    // Advanced panel toggle
    const advToggle = document.getElementById('hf-adv-toggle');
    if (advToggle) {
        advToggle.addEventListener('click', () => {
            const panel = document.getElementById('hf-advanced');
            if (panel) panel.style.display = panel.style.display === 'none' ? '' : 'none';
        });
    }

    // FTS mode buttons
    document.querySelectorAll('[data-mode]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.ftsMode = btn.dataset.mode;
            document.querySelectorAll('[data-mode]')
                .forEach(b => b.classList.toggle('active', b.dataset.mode === filterState.ftsMode));
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    });

    // Source toggle buttons
    document.querySelectorAll('[data-source]').forEach(btn => {
        btn.addEventListener('click', () => {
            const source = btn.dataset.source;
            if (source === 'all') {
                filterState.sourceTypes = [];
            } else {
                const idx = filterState.sourceTypes.indexOf(source);
                if (idx >= 0) filterState.sourceTypes.splice(idx, 1);
                else filterState.sourceTypes.push(source);
            }
            document.querySelectorAll('[data-source]').forEach(b => {
                if (b.dataset.source === 'all')
                    b.classList.toggle('active', filterState.sourceTypes.length === 0);
                else
                    b.classList.toggle('active', filterState.sourceTypes.includes(b.dataset.source));
            });
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    });

    // Chat type toggle buttons
    document.querySelectorAll('[data-chat-type]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.chatType = btn.dataset.chatType;
            document.querySelectorAll('[data-chat-type]').forEach(b => {
                b.classList.toggle('active', b.dataset.chatType === filterState.chatType);
            });
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    });

    // Tag add select
    const tagAddSel = document.getElementById('hf-tag-add');
    if (tagAddSel) {
        tagAddSel.addEventListener('change', () => {
            const id = parseInt(tagAddSel.value);
            if (!id || filterState.tagIds.includes(id)) return;
            filterState.tagIds.push(id);
            tagAddSel.value = '';
            renderFilterTagPills();
            const logicRow = document.getElementById('hf-tag-logic-row');
            if (logicRow) logicRow.style.display = filterState.tagIds.length > 1 ? '' : 'none';
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    }

    // Tag logic buttons
    document.querySelectorAll('[data-logic]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.tagLogic = btn.dataset.logic;
            document.querySelectorAll('[data-logic]')
                .forEach(b => b.classList.toggle('active', b.dataset.logic === filterState.tagLogic));
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    });

    // Date filters
    const dateFrom = document.getElementById('hf-date-from');
    const dateTo = document.getElementById('hf-date-to');
    if (dateFrom) dateFrom.addEventListener('change', () => {
        filterState.dateFrom = dateFrom.value || '';
        setHistoryPage(1);
        loadHistoryFiltered();
    });
    if (dateTo) dateTo.addEventListener('change', () => {
        filterState.dateTo = dateTo.value || '';
        setHistoryPage(1);
        loadHistoryFiltered();
    });

    // Duration filters
    const durMin = document.getElementById('hf-dur-min');
    const durMax = document.getElementById('hf-dur-max');
    if (durMin) durMin.addEventListener('change', () => {
        const val = parseFloat(durMin.value);
        filterState.minDurationSec = isNaN(val) ? null : Math.round(val * 60);
        setHistoryPage(1);
        loadHistoryFiltered();
    });
    if (durMax) durMax.addEventListener('change', () => {
        const val = parseFloat(durMax.value);
        filterState.maxDurationSec = isNaN(val) ? null : Math.round(val * 60);
        setHistoryPage(1);
        loadHistoryFiltered();
    });

    // Clear all filters button
    const clearBtn = document.querySelector('.hf-clear-btn');
    if (clearBtn) {
        clearBtn.addEventListener('click', () => {
            resetFilters();
            if (searchInput) searchInput.value = '';
            if (dateFrom) dateFrom.value = '';
            if (dateTo) dateTo.value = '';
            if (durMin) durMin.value = '';
            if (durMax) durMax.value = '';
            document.querySelectorAll('[data-chat-type]')
                .forEach(b => b.classList.toggle('active', b.dataset.chatType === 'all'));
            document.querySelectorAll('[data-source]')
                .forEach(b => b.classList.toggle('active', b.dataset.source === 'all'));
            document.querySelectorAll('[data-logic]')
                .forEach(b => b.classList.toggle('active', b.dataset.logic === 'AND'));
            document.querySelectorAll('[data-mode]')
                .forEach(b => b.classList.toggle('active', b.dataset.mode === 'or'));
            renderFilterTagPills();
            setHistoryPage(1);
            loadHistoryFiltered();
        });
    }
}
