# Platform Layer

Centralizes browser vs native app differences. See `docs/frontend-app-separation-spec.md` for full design.

## Key Rules

- **Check capabilities, not platforms:** Use `platform.capabilities.layoutOnDisplay` not `platform.isMacOS`
- **`isNative` is UX only, not a security boundary.** Auth uses server-side `auth.IsLocalhost()`
- **Native CSS scoped by class:** `.native-app`, `.native-macos`, `.native-windows` in `css/native*.css`
- **coral-app/main.go only injects `window.__CORAL_APP__ = true`** — everything else lives here

## Where to put new fixes

| Fix type | File |
|----------|------|
| WKWebView layout quirk | `macos.js` |
| WebView2 quirk | `windows.js` |
| Shared native behavior (health, links) | `native.js` |
| New capability flag | `detect.js` |
| macOS-only CSS | `css/native-macos.css` |
| Windows-only CSS | `css/native-windows.css` |
