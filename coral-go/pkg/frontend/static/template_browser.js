/* Template Browser — browse and import agent/command templates from aitmpl.com */

import { escapeHtml, escapeAttr, showToast } from './utils.js';

let _categoriesCache = {};

async function fetchCategories(type) {
    if (_categoriesCache[type]) return _categoriesCache[type];
    try {
        const resp = await fetch(`/api/templates/${type}`);
        const data = await resp.json();
        _categoriesCache[type] = data;
        return data;
    } catch (e) {
        showToast('Failed to load template categories', true);
        return [];
    }
}

async function fetchTemplatesInCategory(type, category) {
    try {
        const resp = await fetch(`/api/templates/${type}/${encodeURIComponent(category)}`);
        return await resp.json();
    } catch (e) {
        showToast('Failed to load templates', true);
        return [];
    }
}

async function fetchTemplate(type, category, name) {
    try {
        const resp = await fetch(`/api/templates/${type}/${encodeURIComponent(category)}/${encodeURIComponent(name)}`);
        return await resp.json();
    } catch (e) {
        showToast('Failed to load template', true);
        return null;
    }
}

/**
 * Show the template browser modal.
 * @param {'agents'|'commands'} type
 * @param {function} onSelect — called with the full template object when user clicks Use/Add
 */
export async function showTemplateBrowser(type, onSelect) {
    const existing = document.getElementById('template-browser-modal');
    if (existing) existing.remove();

    const modal = document.createElement('div');
    modal.id = 'template-browser-modal';
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content template-browser">
            <div class="template-browser-header">
                <h3>${type === 'agents' ? 'Agent Templates' : 'Command Templates'}</h3>
                <input type="text" class="template-browser-search" placeholder="Filter templates...">
                <button class="btn template-browser-close" onclick="this.closest('.modal').remove()">&times;</button>
            </div>
            <div style="font-size:11px;color:var(--text-muted);padding:4px 0 8px">
                Browse community templates from <a href="https://www.aitmpl.com/${type === 'agents' ? 'agents' : 'commands'}" target="_blank" rel="noopener" style="color:var(--accent)">aitmpl.com</a> — open-source Claude Code templates library
            </div>
            <div class="template-browser-body">
                <div class="template-browser-categories">
                    <div class="template-browser-loading">Loading...</div>
                </div>
                <div class="template-browser-list">
                    <div class="template-browser-empty">Select a category</div>
                </div>
                <div class="template-browser-preview">
                    <div class="template-browser-empty">Select a template to preview</div>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);

    // Close on backdrop click
    modal.addEventListener('click', (e) => {
        if (e.target === modal) modal.remove();
    });

    const catPanel = modal.querySelector('.template-browser-categories');
    const listPanel = modal.querySelector('.template-browser-list');
    const previewPanel = modal.querySelector('.template-browser-preview');
    const searchInput = modal.querySelector('.template-browser-search');

    // Load categories
    const rawData = await fetchCategories(type);
    const categories = rawData.categories || rawData.commands || rawData || [];
    if (!categories.length) {
        catPanel.innerHTML = '<div class="template-browser-empty">No categories found</div>';
        return;
    }

    let allTemplates = []; // flat list for search
    let activeCategory = null;

    function renderCategories(filter) {
        const q = (filter || '').toLowerCase();
        const filtered = categories.filter(c => {
            const name = c.name || c;
            return !q || name.toLowerCase().includes(q);
        });
        catPanel.innerHTML = filtered.map(c => {
            const name = c.name || c;
            const display = name.replace(/-/g, ' ');
            const active = activeCategory === name ? ' active' : '';
            return `<button class="template-cat-btn${active}" data-cat="${escapeAttr(name)}">${escapeHtml(display)}</button>`;
        }).join('') || '<div class="template-browser-empty">No matches</div>';

        catPanel.querySelectorAll('.template-cat-btn').forEach(btn => {
            btn.addEventListener('click', () => selectCategory(btn.dataset.cat));
        });
    }

    async function selectCategory(catName) {
        activeCategory = catName;
        renderCategories(searchInput.value);
        listPanel.innerHTML = '<div class="template-browser-loading">Loading...</div>';
        previewPanel.innerHTML = '<div class="template-browser-empty">Select a template to preview</div>';

        const rawTemplates = await fetchTemplatesInCategory(type, catName);
        const templates = rawTemplates.agents || rawTemplates.commands || rawTemplates || [];
        renderTemplateList(templates);
    }

    function renderTemplateList(templates, filter) {
        const q = (filter || '').toLowerCase();
        const filtered = templates.filter(t => {
            const name = t.name || t.filename || '';
            return !q || name.toLowerCase().includes(q);
        });
        if (!filtered.length) {
            listPanel.innerHTML = '<div class="template-browser-empty">No templates found</div>';
            return;
        }
        listPanel.innerHTML = filtered.map(t => {
            const name = t.name || (t.filename || '').replace(/\.md$/, '');
            const display = name.replace(/-/g, ' ');
            return `<button class="template-item-btn" data-cat="${escapeAttr(activeCategory)}" data-name="${escapeAttr(name)}">${escapeHtml(display)}</button>`;
        }).join('');

        listPanel.querySelectorAll('.template-item-btn').forEach(btn => {
            btn.addEventListener('click', () => selectTemplate(btn.dataset.cat, btn.dataset.name));
        });

        // Store for search filtering
        allTemplates = templates;
    }

    async function selectTemplate(cat, name) {
        previewPanel.innerHTML = '<div class="template-browser-loading">Loading...</div>';
        const template = await fetchTemplate(type, cat, name);
        if (!template) {
            previewPanel.innerHTML = '<div class="template-browser-empty">Failed to load template</div>';
            return;
        }

        const displayName = (template.name || name).replace(/-/g, ' ');
        const desc = template.description || '';
        const body = template.body || '';
        const truncBody = body.length > 500 ? body.substring(0, 500) + '...' : body;
        const meta = [];
        if (template.model) meta.push(`Model: ${template.model}`);
        if (template.tools) meta.push(`Tools: ${template.tools}`);
        if (template.allowed_tools) meta.push(`Tools: ${template.allowed_tools}`);
        if (template.argument_hint) meta.push(`Args: ${template.argument_hint}`);

        const btnLabel = type === 'agents' ? 'Use Template' : 'Add as Macro';

        previewPanel.innerHTML = `
            <div class="template-preview-name">${escapeHtml(displayName)}</div>
            ${desc ? `<div class="template-preview-desc">${escapeHtml(desc)}</div>` : ''}
            ${meta.length ? `<div class="template-preview-meta">${meta.map(m => escapeHtml(m)).join(' &middot; ')}</div>` : ''}
            <div class="template-preview-body"><pre>${escapeHtml(truncBody)}</pre></div>
            <button class="btn btn-primary template-use-btn">${btnLabel}</button>
        `;

        previewPanel.querySelector('.template-use-btn').addEventListener('click', () => {
            modal.remove();
            onSelect(template);
        });
    }

    // Search filtering
    searchInput.addEventListener('input', () => {
        const q = searchInput.value.trim();
        renderCategories(q);
        if (allTemplates.length) renderTemplateList(allTemplates, q);
    });

    searchInput.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') modal.remove();
    });

    renderCategories();
}
