/* A time as a short list cell (#6751): "today 9:46", "yesterday", "Oct 21" (with the year when it is not this one), in the viewer's own time. The list is narrow; the full time is in the row's Details. Pure (node-tested). */
const day = d => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();

export function shortWhen(iso, now = new Date()) {
    const t = new Date(iso);
    if (!iso || Number.isNaN(t.getTime())) return '—';
    const dd = Math.round((day(now) - day(t)) / 86400000);
    if (dd === 0) return `today ${t.getHours()}:${String(t.getMinutes()).padStart(2, '0')}`;
    if (dd === 1) return 'yesterday';
    const o = { month: 'short', day: 'numeric' };
    if (t.getFullYear() !== now.getFullYear()) o.year = 'numeric';
    return t.toLocaleDateString('en-US', o);
}
