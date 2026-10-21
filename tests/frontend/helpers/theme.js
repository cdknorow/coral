// The theme swap for suites that look at the light theme (#5757). The dashboard keeps the user's custom colour variables INLINE on <html> (80 of them on a fresh page:
// --bg-primary and the rest of the dark set), and an inline variable beats the stylesheet's [data-theme="light"] block, so setting the attribute alone leaves every
// colour dark: the light-theme checks of a suite that did that ran against the dark colours (and a screenshot "in light" was dark). The app's own switch
// (settings_core.js: clearCustomThemeVars() then applyTheme()) clears them first; this is the same two steps as a snippet that runs in the page synchronously, for the
// expressions that swap and measure in one go.
//   const { themeJs, swapTheme } = require('./helpers/theme');
//   await ev(`${themeJs('light')}; ...measure...`);     // inside any page expression
//   await swapTheme(ev, 'light');                       // or on its own; resolves with the theme now set
// helpers/contrast.js setTheme() is the same through the app's own modules (it also stores the preference and settles two frames); use that where a frame later is fine.
'use strict';

const THEME_FN = `(t => { const r = document.documentElement; for (const p of [...r.style]) if (p.startsWith('--')) r.style.removeProperty(p); r.setAttribute('data-theme', t); return t; })`;

/** The page-side expression that swaps the theme: custom colour variables off, then the attribute. Valid inside a larger expression. */
const themeJs = theme => `(${THEME_FN})(${JSON.stringify(theme)})`;

async function swapTheme(ev, theme) { return ev(`${themeJs(theme)}`); }

module.exports = { themeJs, swapTheme };
