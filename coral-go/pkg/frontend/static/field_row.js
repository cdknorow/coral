// One labelled field (a number box or a text box) with an optional unit and description, built the same way everywhere the dashboard draws one from script (the health check's numbers and reviewers).
// The label text names the field (with the unit after the box, when there is one: "After minutes idle"); the description (the limits or a sentence) is tied to it by aria-describedby, so it is not part of the name.
import { escapeHtml, escapeAttr } from './html_escape.js';

// Options: id (the prefix of the ids: <id>-l label, <id>-u unit, <id>-d description), label, name, type ('number' or 'text'), value;
// number boxes: min, max (step 1, whole numbers, required); attrs (an object of more attributes: list, maxlength, placeholder ...; names are lowercase letters and hyphens, values are escaped);
// unit (text after the box, part of the name; unitName is what a screen reader says instead of an abbreviation: "min" is named "minutes"); desc (text; hideDesc keeps it for a screen reader only, as a range the box already shows), descFirst (the description sits between the label text and the box, else after it);
// wide (the field takes the whole row); after (raw HTML after the description, inside the label: a datalist).
export function fieldRow({ id, label, name, type = 'text', value = '', min, max, attrs = {}, unit = '', desc = '', unitName = '', hideDesc = false, descFirst = false, wide = false, after = '' }) {
    const number = type === 'number';
    const shown = number ? Number(value) : value;
    const names = `${escapeAttr(id)}-l${unit ? ` ${escapeAttr(id)}-u` : ''}`;
    const input = `<input type="${escapeAttr(type)}" name="${escapeAttr(name)}" value="${escapeAttr(shown)}"`
        + `${number ? ` min="${Number(min)}" max="${Number(max)}" step="1" inputmode="numeric" required` : ''}${Object.entries(attrs).filter(([k]) => /^[a-z-]+$/.test(k)).map(([k, v]) => ` ${k}="${escapeAttr(v)}"`).join('')}`
        + ` aria-labelledby="${names}"${desc ? ` aria-describedby="${escapeAttr(id)}-d"` : ''}>`;
    const box = unit ? `<span class="health-knob-box">${input} <span id="${escapeAttr(id)}-u">${unitName ? `<span aria-hidden="true">${escapeHtml(unit)}</span><span class="sr-only">${escapeHtml(unitName)}</span>` : escapeHtml(unit)}</span></span>` : input;
    const note = desc ? `<span class="health-limits${hideDesc ? ' sr-only' : ''}" id="${escapeAttr(id)}-d">${escapeHtml(desc)}</span>` : '';
    return `<label class="health-knob${wide ? ' health-wide' : ''}"><span id="${escapeAttr(id)}-l">${escapeHtml(label)}</span>${descFirst ? ` ${note}` : ''}${box}${descFirst ? '' : note}${after}</label>`;
}
