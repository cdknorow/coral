package auth

// LockedPageHTML is what /auth shows on THIS computer when strict mode is on and the
// browser has no session (#3418). It explains how to get in; it never asks for or shows a
// key. It is a complete static page: no script, and nothing from the request is put in it.
//
// Coral's own apps recover from it by themselves: the desktop window (coral-app) notices it
// is on /auth and signs in again with a fresh link (#3394); the tray opens every window with
// a fresh link. A browser tab opened by hand gets in with 'coral open' (or the app/tray).
const LockedPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="coral-locked" content="1">
<title>Coral is locked</title>
<style>
  :root {
    --bg: #f6f8fa; --card: #ffffff; --border: #d0d7de; --text: #1f2328; --muted: #57606a;
    --code-bg: #eff2f5; --accent: #0969da;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0f1117; --card: #161b22; --border: #30363d; --text: #e1e4e8; --muted: #8b949e;
      --code-bg: #0d1117; --accent: #58a6ff;
    }
  }
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: var(--bg);
    color: var(--text);
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    padding: 16px;
  }
  main {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 40px 32px;
    max-width: 480px;
    width: 100%;
  }
  h1 { font-size: 22px; font-weight: 600; margin-bottom: 8px; display: flex; align-items: center; gap: 10px; }
  h1 .lock { font-size: 20px; }
  p { color: var(--muted); font-size: 14px; line-height: 1.55; margin-bottom: 16px; }
  ol { margin: 0 0 16px 20px; color: var(--text); font-size: 14px; line-height: 1.6; }
  li { margin-bottom: 8px; }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 1px 6px;
    font-size: 13px;
    white-space: nowrap;
  }
  .note { font-size: 13px; margin-bottom: 0; }
  @media (max-width: 420px) {
    main { padding: 28px 20px; }
    h1 { font-size: 20px; }
  }
</style>
</head>
<body>
<main role="main" aria-labelledby="locked-title">
  <h1 id="locked-title"><span class="lock" aria-hidden="true">🔒</span> Coral is locked</h1>
  <p>This Coral only opens for you, from this computer. This page has no sign-in session, so it can't show the dashboard.</p>
  <ol>
    <li>Open Coral from the <strong>Coral app</strong> or the <strong>tray menu</strong> (Open in App or Open in Browser). They sign you in by themselves.</li>
    <li>Or, in a terminal on this computer, run <code>coral open</code>. It opens a one-time sign-in link in your browser.</li>
  </ol>
  <p class="note">No key is needed or shown here. A sign-in link works once and for 10 minutes; if one has expired, ask for a new one the same way.</p>
</main>
</body>
</html>
`
