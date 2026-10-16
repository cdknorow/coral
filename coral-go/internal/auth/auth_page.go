package auth

import (
	"bytes"
	"html/template"
)

// PageState says which screen the /auth page shows (#3948). The server decides it, so the page never asks an
// unauthenticated status question.
//
//   - PasswordSet: the sign-in form (password, "Keep me signed in on this browser"), "Forgot password?" and a way
//     to use an API key instead.
//   - not set, Local: "Create a password" (password, confirm, keep signed in); the page is on this computer, so
//     it needs nothing else.
//   - not set, not Local: the same, plus a setup code (printed on Coral's console where it was started, never in coral.log, and by coral setup-code on
//     the computer that runs Coral), so whoever reaches the port first cannot claim the install.
//
// Strict marks strict local auth: the app, the tray and `coral open` still sign in by themselves, and the page says so.
type PageState struct {
	PasswordSet bool
	Local       bool
	Strict      bool
}

// AuthPage renders the page for a state. The markup is fixed and has no user data; the behaviour lives in
// /static/auth_page.js (no inline script, so the dashboard's script-src 'self' policy would hold here too).
func AuthPage(s PageState) string {
	var b bytes.Buffer
	if err := authPageTmpl.Execute(&b, s); err != nil {
		return "<!DOCTYPE html><title>Coral</title><p>Could not render the sign-in page."
	}
	return b.String()
}

var authPageTmpl = template.Must(template.New("auth").Parse(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<meta name="referrer" content="no-referrer">
<title>{{if .PasswordSet}}Sign in to Coral{{else}}Create a password for Coral{{end}}</title>
<link rel="stylesheet" href="/static/css/fonts.css">
<link rel="stylesheet" href="/static/css/variables.css">
<link rel="stylesheet" href="/static/css/base.css">
<link rel="stylesheet" href="/static/css/components.css">
<style>
  html, body { height: 100%; }
  body { margin: 0; background: var(--bg-primary); color: var(--text-primary); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex; align-items: center; justify-content: center; padding: 16px; box-sizing: border-box; }
  .card { width: 100%; max-width: 440px; background: var(--bg-secondary); border: 1px solid var(--border); border-radius: 12px; padding: 32px 30px; box-sizing: border-box; }
  .brand { display: flex; align-items: center; gap: 10px; font-size: 20px; font-weight: 600; margin: 0 0 6px; }
  .brand .dot { width: 26px; height: 26px; border-radius: 7px; background: linear-gradient(135deg, #ff7a59, #f0506e); display: inline-block; }
  h1 { font-size: 20px; font-weight: 600; margin: 0 0 6px; }
  .sub { color: var(--text-secondary); font-size: 14px; line-height: 1.5; margin: 0 0 20px; }
  .err { display: flex; gap: 8px; align-items: flex-start; background: rgba(229, 83, 75, .12); border: 1px solid rgba(229, 83, 75, .5); border-radius: 8px; padding: 10px 12px; font-size: 13px; line-height: 1.45; margin: 0 0 16px; }
  .lock { display: flex; gap: 8px; align-items: flex-start; background: rgba(212, 167, 44, .13); border: 1px solid rgba(212, 167, 44, .55); border-radius: 8px; padding: 12px; font-size: 13px; line-height: 1.5; margin: 0 0 16px; }
  .lock .count { font-variant-numeric: tabular-nums; font-weight: 700; font-size: 16px; }
  .ok { color: var(--text-secondary); font-size: 13px; margin: 12px 0 0; }
  .btnrow { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
  .btn.wide { flex: 1; padding: 10px 14px; font-size: 15px; justify-content: center; }
  .link { color: var(--accent); font-size: 13px; background: none; border: 0; padding: 0; cursor: pointer; text-decoration: none; }
  .linkrow { margin-top: 16px; font-size: 13px; color: var(--text-secondary); display: flex; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
  .check { display: flex; align-items: flex-start; gap: 9px; font-size: 14px; margin: 4px 0 18px; }
  .check input { margin-top: 3px; width: 16px; height: 16px; flex: none; padding: 0; appearance: auto; -webkit-appearance: checkbox; background: none; accent-color: var(--accent); }
  .check small { display: block; color: var(--text-secondary); font-size: 12px; margin-top: 2px; }
  .code { display: flex; align-items: center; justify-content: space-between; gap: 10px; background: var(--bg-primary); border: 1px solid var(--border-light); border-radius: 8px; padding: 10px 12px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; margin: 0 0 16px; }
  ol.steps { margin: 0 0 18px 18px; padding: 0; font-size: 14px; line-height: 1.6; }
  ol.steps li { margin-bottom: 8px; }
  .note { font-size: 12px; color: var(--text-secondary); line-height: 1.5; margin: 14px 0 0; }
  .panel[hidden], .err[hidden], .lock[hidden] { display: none; }
  .pw-input.bad { border-color: #e5534b; }
  .setup-input { padding-right: 11px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: .12em; text-transform: uppercase; }
  @media (max-width: 480px) { .card { padding: 24px 18px; } }
</style>
<script src="/static/theme_init.js"></script>
</head>
<body data-password-set="{{.PasswordSet}}" data-local="{{.Local}}" data-strict="{{.Strict}}">
<main class="card">
  <div class="brand"><span class="dot"></span>Coral</div>
  <div id="live" class="panel" role="status" aria-live="polite"></div>
{{if .PasswordSet}}
  <section id="panel-signin" class="panel">
    <h1>Sign in</h1>
    <p class="sub">Enter the password you made for Coral on this computer.</p>
    <div id="err" class="err" role="alert" hidden></div>
    <div id="lock" class="lock" role="status" hidden><span>&#9202;</span><span><b>Sign-in is paused.</b> Too many wrong passwords. It opens again in <span class="count" id="count">0:00</span>.</span></div>
    <form id="signin-form" autocomplete="on">
      <input type="text" name="username" value="coral" autocomplete="username" hidden aria-hidden="true" readonly tabindex="-1">
      <label class="pw-field"><span class="pw-label">Password</span><span class="pw-input-row"><input class="pw-input" id="password" name="password" type="password" autocomplete="current-password" spellcheck="false" autocapitalize="off" autofocus><button type="button" class="pw-toggle" data-toggle="password" aria-pressed="false" aria-label="Show password">Show</button></span></label>
      <label class="check"><input type="checkbox" id="keep"><span>Keep me signed in on this browser<small>For 30 days. Leave it off on a shared computer.</small></span></label>
      <div class="btnrow"><button type="submit" id="submit" class="btn btn-primary wide">Sign in</button></div>
    </form>
    <div class="linkrow"><button type="button" class="link" id="show-forgot">Forgot password?</button><button type="button" class="link" id="show-key">Use an API key instead</button></div>
  </section>
  <section id="panel-forgot" class="panel" hidden>
    <h1>Forgot your password?</h1>
    <p class="sub">Coral has no email to send a link to. Resetting happens on the computer that runs Coral, which keeps it safe from anyone else on the network.</p>
    <ol class="steps"><li>Open a terminal on <b>this computer</b> (the one running Coral).</li><li>Run:</li></ol>
    <div class="code"><span id="reset-cmd">coral reset-password</span><button type="button" class="btn btn-small" id="copy-reset">Copy</button></div>
    <ol class="steps" start="3"><li>Choose a new password when it asks. Browsers that were signed in are signed out.</li><li>Come back here and sign in.</li></ol>
    <div class="btnrow"><button type="button" class="btn btn-primary wide" data-back="signin">Back to sign in</button></div>
    <p class="note">The Coral app and the tray open without a password.</p>
  </section>
{{else}}
  <section id="panel-create" class="panel">
    <h1>Create a password</h1>
    <p class="sub">{{if .Local}}Choose a password to open Coral in a browser. The Coral app and the tray open without it.{{else}}Coral has no password yet. Choose one to open it in this browser, with the setup code from the computer that runs Coral.{{end}}</p>
    <div id="err" class="err" role="alert" hidden></div>
    <div id="lock" class="lock" role="status" hidden><span>&#9202;</span><span><b>Setup is paused.</b> Too many wrong attempts. It opens again in <span class="count" id="count">0:00</span>.</span></div>
    <form id="create-form" autocomplete="on">
      <input type="text" name="username" value="coral" autocomplete="username" hidden aria-hidden="true" readonly tabindex="-1">
{{if not .Local}}
      <label class="pw-field"><span class="pw-label">Setup code</span><span class="pw-input-row"><input class="pw-input setup-input" id="setup-code" name="setup-code" type="text" autocomplete="off" spellcheck="false" autocapitalize="characters" inputmode="text" maxlength="16"></span></label>
      <p class="note" style="margin:-4px 0 14px">Coral prints it where it was started (its console), or run <b>coral setup-code</b> on the computer that runs Coral.</p>
{{end}}
      <label class="pw-field"><span class="pw-label">Password</span><span class="pw-input-row"><input class="pw-input" id="password" name="new-password" type="password" autocomplete="new-password" spellcheck="false" autocapitalize="off" aria-describedby="strength"><button type="button" class="pw-toggle" data-toggle="password" aria-pressed="false" aria-label="Show password">Show</button></span></label>
      <div class="pw-strength" id="meter" data-level="empty"><div class="pw-meter" role="img" aria-label="Strength"><i></i></div><p class="pw-hint" id="strength" aria-live="polite"></p></div>
      <label class="pw-field"><span class="pw-label">Confirm password</span><span class="pw-input-row"><input class="pw-input" id="confirm" name="confirm-password" type="password" autocomplete="new-password" spellcheck="false" autocapitalize="off"><button type="button" class="pw-toggle" data-toggle="confirm" aria-pressed="false" aria-label="Show confirm password">Show</button></span></label>
      <label class="check"><input type="checkbox" id="keep"><span>Keep me signed in on this browser<small>For 30 days. Leave it off on a shared computer.</small></span></label>
      <div class="btnrow"><button type="submit" id="submit" class="btn btn-primary wide">Create password and open Coral</button></div>
    </form>
    <p class="note">Your password manager can save it. There is no email: if you forget it, run coral reset-password on this computer.</p>
    <div class="linkrow"><span></span><button type="button" class="link" id="show-key">Use an API key instead</button></div>
  </section>
{{end}}
  <section id="panel-key" class="panel" hidden>
    <h1>Use an API key</h1>
    <p class="sub">Paste the key from Settings in the Coral app, or from the file ~/.coral/api_key on the computer that runs Coral.</p>
    <div id="key-err" class="err" role="alert" hidden></div>
    <form id="key-form" autocomplete="off">
      <label class="pw-field"><span class="pw-label">API key</span><span class="pw-input-row"><input class="pw-input" id="api-key" name="api-key" type="password" autocomplete="off" spellcheck="false" style="padding-right:11px"></span></label>
      <div class="btnrow"><button type="submit" id="key-submit" class="btn btn-primary wide">Connect</button></div>
    </form>
    <div class="linkrow"><button type="button" class="link" data-back="main">Back</button></div>
  </section>
{{if .Strict}}  <p class="note">The Coral app, the tray and coral open sign in by themselves; this page is for a browser.</p>
{{end}}</main>
<script type="module" src="/static/auth_page.js"></script>
</body>
</html>`))
