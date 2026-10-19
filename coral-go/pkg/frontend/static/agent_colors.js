/* Agent and board colours (moved out of utils.js, #5065; no behaviour change). */
// A sender's name and avatar initials on the board in shades of the palette that meet WCAG AA
// (#3590): the palette is pastel, so the light theme takes darker shades and a few hues take
// lighter ones in the dark theme. Each holds at least 4.6:1 against the default themes' page,
// their hover highlight, and its own 18% tint (the avatar square). Same hue, HSL lightness only.
const BOARD_TEXT_SHADES = new Map([
    ['#81a1c1', { dark: '#99b3cd', light: '#344f6a' }],
    ['#a3be8c', { dark: '#a3be8c', light: '#40542f' }],
    ['#b48ead', { dark: '#c5a8c0', light: '#66435f' }],
    ['#d08770', { dark: '#dba493', light: '#7a3c28' }],
    ['#bf616a', { dark: '#d9a2a8', light: '#81343b' }],
    ['#88c0d0', { dark: '#88c0d0', light: '#265461' }],
    ['#ebcb8b', { dark: '#ebcb8b', light: '#654911' }],
    ['#8fbcbb', { dark: '#8fbcbb', light: '#325453' }],
]);

/** The board text shade of a palette colour for a theme ('dark' or 'light'). */
export function boardTextColor(color, theme) {
    const shades = BOARD_TEXT_SHADES.get(color);
    return shades ? shades[theme === 'light' ? 'light' : 'dark'] : color;
}

/** A board message's colours as CSS variables, both themes' shades; the stylesheet picks one by
 *  data-theme for the sender's name and avatar (#3590). color is a getAgentColor palette hex. */
export function boardMessageColorStyle(color) {
    const dark = boardTextColor(color, 'dark'), light = boardTextColor(color, 'light');
    return `--mb-color-dark:${dark};--mb-tint-dark:${hexToRgba(dark, 0.18)};--mb-color-light:${light};--mb-tint-light:${hexToRgba(light, 0.18)}`;
}

// Shared agent color palette (Nord-inspired muted tones)
const _agentColorPalette = [
    '#81a1c1', '#a3be8c', '#b48ead', '#d08770',
    '#bf616a', '#88c0d0', '#ebcb8b', '#8fbcbb',
];

// A Map, not an object: a sender named 'constructor' or '__proto__' must get a palette colour,
// not Object.prototype's (#3590, Security's NIT).
const _agentColorCache = new Map();

/** The palette offered for agent name colours (same hues as avatars). */
export const AGENT_NAME_PALETTE = _agentColorPalette;

const _hexColorRe = /^#[0-9a-f]{6}$/i;

/** The user's name-colour override for a session (#rrggbb), or null for the default. */
export function agentNameColor(s) {
    return s && s.name_color && _hexColorRe.test(s.name_color) ? s.name_color : null;
}

export function getAgentColor(name) {
    if (!name) return _agentColorPalette[0];
    const known = _agentColorCache.get(name);
    if (known) return known;
    const color = _agentColorPalette[_agentColorCache.size % _agentColorPalette.length];
    _agentColorCache.set(name, color);
    return color;
}

export function hexToRgba(hex, alpha) {
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return `rgba(${r},${g},${b},${alpha})`;
}
