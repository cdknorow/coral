/* Read-only JSON report presentation. All artifact content stays text, never HTML. */
const MAX_REPORT_DEPTH = 8;
const MAX_REPORT_NODES = 600;
const MAX_REPORT_TEXT = 200000;

const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const scalar = value => value !== null && ['string', 'number', 'boolean'].includes(typeof value);
const humanize = key => key.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').replace(/^./, c => c.toUpperCase());

function node(tag, className, text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
}

function recognizedReport(value, filename) {
    if (!object(value)) return false;
    const name = (filename || '').split(/[\\/]/).pop().replace(/\.json$/i, '');
    const named = /(?:^|[_. -])report(?:$|[_. -])/i.test(name);
    // Unnamed JSON needs an explicit task/outcome plus report prose/evidence.
    const schema = (typeof value.task === 'number' || typeof value.task_id === 'number')
        && typeof value.outcome === 'string' && typeof value.summary === 'string'
        && Array.isArray(value.verification);
    return named || schema;
}

function withinBudget(value) {
    let count = 0;
    let text = 0;
    const stack = [[value, 0]];
    while (stack.length) {
        const [item, depth] = stack.pop();
        if (++count > MAX_REPORT_NODES || depth > MAX_REPORT_DEPTH) return false;
        if (typeof item === 'string') text += item.length;
        if (object(item)) {
            const entries = Object.entries(item);
            if (entries.length + count > MAX_REPORT_NODES) return false;
            for (const [key, child] of entries) { text += key.length; stack.push([child, depth + 1]); }
        } else if (Array.isArray(item)) {
            if (item.length + count > MAX_REPORT_NODES) return false;
            for (const child of item) stack.push([child, depth + 1]);
        }
        if (text > MAX_REPORT_TEXT) return false;
    }
    return true;
}

function renderValue(value) {
    if (Array.isArray(value)) {
        if (!value.length) return node('span', 'artifact-report-literal', '[]');
        const list = node('ul', 'artifact-report-list');
        for (const child of value) {
            const entry = node('li'); entry.append(renderValue(child)); list.append(entry);
        }
        return list;
    }
    if (object(value)) {
        if (!Object.keys(value).length) return node('span', 'artifact-report-literal', '{}');
        const fields = node('dl', 'artifact-report-fields');
        for (const [key, child] of Object.entries(value)) {
            const label = node('dt', '', humanize(key)); label.title = key;
            const detail = node('dd'); detail.append(renderValue(child)); fields.append(label, detail);
        }
        return fields;
    }
    return node('span', 'artifact-report-value', typeof value === 'string' ? (value || '""') : JSON.stringify(value));
}

// Return false for ordinary configs, malformed JSON and artifact manifests so
// callers retain their existing presentation. The raw view uses original text.
export function renderJSONReport(container, content, filename) {
    let report;
    try { report = JSON.parse(content); } catch { return false; }
    if (!recognizedReport(report, filename)) return false;
    const root = node('div', 'artifact-report');
    if (!withinBudget(report)) {
        root.append(node('p', 'artifact-report-notice', 'This report is too large or deeply nested for the report view. Showing the complete raw JSON; the original is also available with Download.'));
        root.append(node('pre', 'artifact-report-raw', content));
        container.replaceChildren(root);
        return true;
    }

    const controls = node('div', 'artifact-report-controls');
    controls.setAttribute('role', 'group'); controls.setAttribute('aria-label', 'Report view');
    const reportButton = node('button', 'team-artifacts-action', 'Report');
    const rawButton = node('button', 'team-artifacts-action', 'View raw JSON');
    reportButton.type = rawButton.type = 'button';
    const readable = node('div', 'artifact-report-readable');
    const raw = node('pre', 'artifact-report-raw', content);
    const select = showRaw => {
        readable.hidden = showRaw; raw.hidden = !showRaw;
        reportButton.setAttribute('aria-pressed', String(!showRaw));
        rawButton.setAttribute('aria-pressed', String(showRaw));
    };
    reportButton.addEventListener('click', () => select(false));
    rawButton.addEventListener('click', () => select(true));
    controls.append(reportButton, rawButton);

    const consumed = new Set();
    const taskKey = scalar(report.task) ? 'task' : scalar(report.task_id) ? 'task_id' : null;
    readable.append(node('h1', 'artifact-report-title', taskKey ? `Task ${String(report[taskKey]).startsWith('#') ? '' : '#'}${report[taskKey]}` : 'Report'));
    if (taskKey) consumed.add(taskKey);
    if (scalar(report.outcome)) {
        readable.append(node('p', 'artifact-report-outcome', `Reported outcome: ${report.outcome}`));
        consumed.add('outcome');
    }
    // Order familiar sections first, then retain every remaining top-level key.
    const preferred = ['summary', 'verification', 'files', 'isolation', 'limits'];
    const keys = [...preferred.filter(key => Object.hasOwn(report, key)), ...Object.keys(report).filter(key => !preferred.includes(key))];
    for (const key of keys) {
        if (consumed.has(key)) continue;
        const section = node('section', 'artifact-report-section');
        const heading = node('h2', '', humanize(key)); heading.title = key;
        section.append(heading, renderValue(report[key]));
        readable.append(section);
    }
    select(false);
    root.append(controls, readable, raw);
    container.replaceChildren(root);
    return true;
}
