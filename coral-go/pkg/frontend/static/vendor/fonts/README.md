# Vendored fonts

Served from the app's own origin so the UI makes no request to Google (privacy: no user IP or
referrer reaches a third party; CSP: no external font/style hosts).

| File | Font | Licence | Source |
|---|---|---|---|
| `jakarta-latin.woff2`, `jakarta-latin-ext.woff2` | Plus Jakarta Sans (400-700, latin and latin-ext subsets) | SIL Open Font License 1.1 | Google Fonts, `fonts.gstatic.com/s/plusjakartasans/v12/` |
| `material-icons.woff2` | Material Icons (regular), **subset** to the icons the dashboard's source names (`material-icons.names.txt`) | Apache License 2.0 | Google Fonts, `fonts.gstatic.com/s/materialicons/v145/`, reduced by `coral-go/tools/subset_icons.py` (fonttools, MIT) |

Cyrillic and Vietnamese subsets of Plus Jakarta Sans are not bundled; those characters fall back to the
system font. Add them with a matching `@font-face` in `css/fonts.css` if needed.

The icon font is a ligature font (an element's text, such as `close`, is the icon), so an icon missing from the subset shows as
its name. The full font is 129 KB; the subset is about 9 KB. After using a new icon, rebuild it with the full upstream font
(not kept in this repository): `python3 coral-go/tools/subset_icons.py --source <full material-icons.woff2>`.
`tests/frontend/icon_subset.test.mjs` fails until that is done, and `icon_subset_browser.test.js` checks the rendered dashboard.
