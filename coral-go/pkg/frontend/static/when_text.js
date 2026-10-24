/* When something happened, in the one format the Chats rows, the Group rows, the Saved items and the Inbox's tasks all use (#6961, #6986, UI Accessibility's #6960 and #6961 notes): under a minute "just now",
 * under an hour "5 min ago", after that today and yesterday with the time of day ("today 2:32 PM" or "today 14:32": the viewer's own 12- or 24-hour clock, Intl's locale), an older day by its date alone ("Oct 17",
 * with the year when it is not this one: "Sep 18, 2025"). Local time. Text, never a hover title alone: the time of day is what tells three chats of one day apart, and a phone has no hover. No imports (the
 * Inbox's panel is shared with the Hub and loads nothing else). */
// capitalised: the icon scanner reads a quoted lower-case word as a Material icon name (icon_subset)
const DAY_WORDS = ['Today', 'Yesterday'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** The time of day in the viewer's clock ("2:32 PM" or "14:32"); `locale` is for tests (undefined: the viewer's own). */
function clock(d, locale) {
    try { return d.toLocaleTimeString(locale, { timeStyle: 'short' }); } catch { return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`; }
}

/** "just now", "5 min ago", "today 2:32 PM", "yesterday 17:20", "Oct 17" or "Sep 18, 2025"; '' when `stamp` is empty or not a date. */
export function whenText(stamp, now = new Date(), locale) {
    const d = new Date(stamp);
    if (!stamp || isNaN(d)) return '';
    const mins = Math.floor((now - d) / 60000);
    if (mins < 1 && mins > -2) return 'just now'; // a clock a little ahead of the server's is "just now", not tomorrow
    if (mins >= 1 && mins < 60) return `${mins} min ago`;
    const day0 = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const days = Math.round((day0(now) - day0(d)) / 86400000);
    if (days === 0 || days === 1) return `${DAY_WORDS[days].toLowerCase()} ${clock(d, locale)}`;
    return `${MONTHS[d.getMonth()]} ${d.getDate()}${d.getFullYear() !== now.getFullYear() ? `, ${d.getFullYear()}` : ''}`;
}
