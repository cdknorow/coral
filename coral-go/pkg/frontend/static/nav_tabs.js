/* The top navigation at every width (#3816). The tab buttons in the top bar are the one list; this module draws the rest from
 * them, so a tab that appears or goes (Servers, Board, the Hub's local-only tabs) and the active tab follow:
 *  - Desktop and tablet: the tabs stay in one row with their normal gap, never wrapped or squeezed together. When they do not
 *    all fit (large text, zoom, more tabs) the last ones (never the active one) move under a "More" menu in the top bar.
 *  - Phone (<= 767 px): the row is replaced by one current-view dropdown in the top bar (44 px tall): it names the view you are
 *    on and opens the list of all views. Escape and a click outside close it.
 * The buttons drawn here carry the same data-on-click as the tabs, so switching a tab is still the one action. */
import { registerActions } from './actions.js';

const ICONS = { agents: 'smart_toy', history: 'chat_bubble_outline', workflows: 'account_tree', jobs: 'schedule', tokens: 'insights',
    docs: 'menu_book', servers: 'dns', 'hub-inbox': 'notifications', board: 'forum', artifacts: 'inventory_2' };

const tabsEl = () => document.querySelector('.top-nav-tabs');
const isPhone = () => window.matchMedia('(max-width: 767px)').matches;

/** The tabs that exist right now, in order: {id, arg, label, active, el}. */
function availableTabs() {
    const cloud = document.body.classList.contains('cloud-mode');
    return [...document.querySelectorAll('.top-nav-tabs > .top-nav-tab[data-arg]')]
        .filter(el => !el.hidden && el.style.display !== 'none' && !(cloud && el.hasAttribute('data-local-only')) && !(!cloud && el.hasAttribute('data-cloud-only')))
        .map(el => ({ id: el.id, arg: el.dataset.arg, label: (el.dataset.label || el.textContent).trim(), active: el.classList.contains('active'), el }));
}

// Built with the DOM, so no text from a tab is ever read as markup.
function makeItem(t) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'nav-more-item' + (t.active ? ' active' : '');
    b.dataset.onClick = 'm-switch-nav-tab';
    b.dataset.arg = t.arg;
    if (t.active) b.setAttribute('aria-current', 'page');
    const i = document.createElement('span');
    i.className = 'material-icons';
    i.setAttribute('aria-hidden', 'true');
    i.textContent = ICONS[t.arg] || 'circle';
    const l = document.createElement('span');
    l.textContent = t.label;
    b.append(i, l);
    // A tab with a count (the Inbox's waiting number) shows it in the list too, and the item says it (#4718).
    const badge = t.el.querySelector('.nav-tab-badge');
    if (badge && !badge.hidden && badge.textContent) {
        const c = document.createElement('span');
        c.className = 'nav-tab-badge nav-more-badge';
        c.textContent = badge.textContent;
        b.append(c);
        b.setAttribute('aria-label', t.el.getAttribute('aria-label') || `${t.label}, ${badge.textContent}`);
    }
    return b;
}

function makeMore() {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'top-nav-tab nav-more-btn';
    b.dataset.onClick = 'm-nav-more';
    b.setAttribute('aria-haspopup', 'menu');
    b.setAttribute('aria-expanded', 'false');
    b.setAttribute('aria-controls', 'nav-more-menu');
    const t = document.createElement('span');
    t.textContent = 'More';
    const c = document.createElement('span');
    c.className = 'material-icons';
    c.setAttribute('aria-hidden', 'true');
    c.textContent = 'expand_more';
    b.append(t, c);
    return b;
}

let _more = [];          // the tabs the row's More menu lists right now
let _menuFor = null;     // the button that opened the menu

function closeMenu() {
    const menu = document.getElementById('nav-more-menu');
    if (menu) menu.hidden = true;
    document.querySelectorAll('[data-on-click="m-nav-more"]').forEach(b => b.setAttribute('aria-expanded', 'false'));
    _menuFor = null;
}

function openMenu(btn) {
    const menu = document.getElementById('nav-more-menu');
    if (!menu) return;
    const all = btn.hasAttribute('data-nav-all');
    const items = all ? availableTabs() : _more;
    if (!items.length) return;
    menu.classList.toggle('nav-all', all);
    menu.replaceChildren(...items.map(makeItem));
    menu.hidden = false;
    const r = btn.getBoundingClientRect();
    menu.style.top = Math.round(r.bottom + 6) + 'px';
    menu.style.bottom = 'auto';
    if (all) { menu.style.left = '8px'; menu.style.right = '8px'; }
    else { menu.style.left = 'auto'; menu.style.right = Math.max(8, window.innerWidth - r.right) + 'px'; }
    btn.setAttribute('aria-expanded', 'true');
    _menuFor = btn;
}

// Desktop and tablet: show every tab, then take tabs off the end (not the active one) until the row fits.
function layoutRow() {
    const nav = tabsEl();
    if (!nav) return;
    const all = availableTabs();
    nav.querySelectorAll('.nav-more-wrap').forEach(n => n.remove());
    for (const t of all) t.el.classList.remove('nav-overflowed');
    _more = [];
    if (isPhone()) return;
    const fits = () => nav.scrollWidth <= nav.clientWidth + 1;
    if (fits()) return;
    const wrap = document.createElement('span');
    wrap.className = 'nav-more-wrap';
    const btn = makeMore();
    wrap.appendChild(btn);
    nav.appendChild(wrap);
    for (const t of all.filter(x => !x.active).reverse()) {
        if (fits()) break;
        t.el.classList.add('nav-overflowed');
        _more.unshift(t);
    }
    if (!_more.length) wrap.remove();
    else if (_more.some(t => t.active)) btn.classList.add('active');
}

// Phone: the dropdown names the current view.
function layoutSelect() {
    const label = document.getElementById('nav-select-label');
    if (!label) return;
    const cur = availableTabs().find(t => t.active);
    label.textContent = cur ? cur.label : '';
    if (_menuFor && _menuFor.id === 'nav-select' && !isPhone()) closeMenu();
}

let _scheduled = 0;
let _obs = null;
function syncNav() {
    cancelAnimationFrame(_scheduled);
    _scheduled = requestAnimationFrame(() => {
        layoutRow(); layoutSelect();
        if (_obs) _obs.takeRecords(); // what this redraw changed is not a reason to redraw
    });
}

registerActions({
    'm-nav-more': (el, event) => {
        event.stopPropagation();
        if (_menuFor === el) closeMenu(); else { closeMenu(); openMenu(el); }
    },
});

if (typeof document !== 'undefined') {
    const init = () => {
        const nav = tabsEl();
        if (!nav) return;
        const bar = document.querySelector('header.top-bar');
        if (typeof ResizeObserver !== 'undefined') new ResizeObserver(syncNav).observe(bar);
        // A tab appears or goes, or the active tab changes: redraw from the tabs.
        _obs = new MutationObserver(syncNav);
        _obs.observe(nav, { attributes: true, subtree: true, attributeFilter: ['class', 'hidden', 'style', 'aria-current'] });
        window.addEventListener('resize', syncNav);
        window.matchMedia('(max-width: 767px)').addEventListener('change', syncNav);
        document.addEventListener('click', e => {
            if (!_menuFor) return;
            if (e.target.closest('#nav-more-menu .nav-more-item') || !e.target.closest('#nav-more-menu, [data-on-click="m-nav-more"]')) closeMenu();
        });
        document.addEventListener('keydown', e => { if (e.key === 'Escape' && _menuFor) { const b = _menuFor; closeMenu(); b.focus(); } });
        syncNav();
    };
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
}
