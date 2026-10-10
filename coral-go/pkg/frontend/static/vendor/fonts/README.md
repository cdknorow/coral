# Vendored fonts

Served from the app's own origin so the UI makes no request to Google (privacy: no user IP or
referrer reaches a third party; CSP: no external font/style hosts).

| File | Font | Licence | Source |
|---|---|---|---|
| `jakarta-latin.woff2`, `jakarta-latin-ext.woff2` | Plus Jakarta Sans (400-700, latin and latin-ext subsets) | SIL Open Font License 1.1 | Google Fonts, `fonts.gstatic.com/s/plusjakartasans/v12/` |
| `material-icons.woff2` | Material Icons (regular) | Apache License 2.0 | Google Fonts, `fonts.gstatic.com/s/materialicons/v145/` |

Cyrillic and Vietnamese subsets of Plus Jakarta Sans are not bundled; those characters fall back to the
system font. Add them with a matching `@font-face` in `css/fonts.css` if needed.
