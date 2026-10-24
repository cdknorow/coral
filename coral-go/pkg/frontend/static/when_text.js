/* When something happened, in the one format the Chats rows, the Group rows, the Saved items and the Inbox's tasks all use (#6961, UI Accessibility's #6960): today and yesterday with the time of day ("today
 * 14:32", "yesterday 17:20"), an older day by its date alone ("Oct 17", with the year when it is not this one: "Sep 18, 2025"). The clock is 24-hour and the viewer's local time. Text, never a hover title alone:
 * the time of day is what tells three chats of one day apart, and a phone has no hover. No imports (the Inbox's panel is shared with the Hub and loads nothing else). */
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
// capitalised: the icon scanner reads a quoted lower-case word as a Material icon name (icon_subset)
const DAY_WORDS = ['Today', 'Yesterday'];
const pad = n => String(n).padStart(2, '0');

/** "today 14:32", "yesterday 17:20", "Oct 17" or "Sep 18, 2025"; '' when `stamp` is empty or not a date. */
export function whenText(stamp, now = new Date()) {
    const d = new Date(stamp);
    if (!stamp || isNaN(d)) return '';
    const day0 = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const days = Math.round((day0(now) - day0(d)) / 86400000);
    if (days === 0 || days === 1) return `${DAY_WORDS[days].toLowerCase()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    return `${MONTHS[d.getMonth()]} ${d.getDate()}${d.getFullYear() !== now.getFullYear() ? `, ${d.getFullYear()}` : ''}`;
}
