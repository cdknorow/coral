// One labelled switch (a checkbox) with an optional description, built the same way everywhere the dashboard draws one from script.
// The label names the switch; the description is tied to it by aria-describedby. When the label also holds the description (descInside) or its text
// sits in a span, aria-labelledby names the switch by the title alone, so the description is not read twice as part of the name.
import { escapeHtml, escapeAttr } from './html_escape.js';

// Options: label (text, escaped here); name, attrs (raw, after name), checked, disabled; labelClass, labelAttrs (raw);
// labelId (wrap the text in a span with this id and name the switch by it), titleTag ('span' or 'b');
// desc (text), descId, descTag, descClass; descInside (description inside the label, after sep); describedBy (more ids);
// gap (text between the box and the label text).
export function switchRow({ label, name, attrs = '', checked = false, disabled = false, labelClass = '', labelAttrs = '', labelId = '', titleTag = 'span',
    desc = '', descId = '', descTag = 'span', descClass = '', descInside = false, sep = '', describedBy = [], gap = ' ' }) {
    const ids = [desc && descId, ...describedBy].filter(Boolean);
    const input = `<input type="checkbox"${name ? ` name="${escapeAttr(name)}"` : ''}${attrs}${checked ? ' checked' : ''}${disabled ? ' disabled' : ''}`
        + `${labelId ? ` aria-labelledby="${escapeAttr(labelId)}"` : ''}${ids.length ? ` aria-describedby="${ids.map(escapeAttr).join(' ')}"` : ''}>`;
    const title = labelId ? `<${titleTag} id="${escapeAttr(labelId)}">${escapeHtml(label)}</${titleTag}>` : escapeHtml(label);
    const note = desc ? `<${descTag}${descClass ? ` class="${escapeAttr(descClass)}"` : ''}${descId ? ` id="${escapeAttr(descId)}"` : ''}>${escapeHtml(desc)}</${descTag}>` : '';
    const text = descInside && note ? `<span>${title}${sep}${note}</span>` : title;
    return `<label${labelClass ? ` class="${escapeAttr(labelClass)}"` : ''}${labelAttrs}>${input}${gap}${text}</label>${descInside ? '' : note}`;
}
