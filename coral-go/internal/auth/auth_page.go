package auth

const authPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Coral — Authenticate</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #0f1117;
    color: #e1e4e8;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
  }
  .card {
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 12px;
    padding: 48px;
    max-width: 440px;
    width: 100%;
    text-align: center;
  }
  .card h1 { font-size: 24px; font-weight: 600; margin-bottom: 8px; }
  .card p { color: #8b949e; margin-bottom: 24px; font-size: 14px; line-height: 1.5; }
  .input-group { position: relative; margin-bottom: 16px; }
  .input-group input {
    width: 100%;
    padding: 12px 16px;
    background: #0d1117;
    border: 1px solid #30363d;
    border-radius: 8px;
    color: #e1e4e8;
    font-size: 15px;
    font-family: monospace;
    letter-spacing: 1px;
    outline: none;
    transition: border-color 0.2s;
  }
  .input-group input:focus { border-color: #58a6ff; }
  button {
    width: 100%;
    padding: 12px;
    background: #238636;
    color: #fff;
    border: none;
    border-radius: 8px;
    font-size: 15px;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.2s;
  }
  button:hover { background: #2ea043; }
  button:disabled { background: #21262d; color: #484f58; cursor: not-allowed; }
  .error { color: #f85149; font-size: 13px; margin-top: 12px; display: none; }
  .success { color: #3fb950; font-size: 13px; margin-top: 12px; display: none; }
</style>
</head>
<body>
<div class="card">
  <h1>Coral</h1>
  <p>Enter your API key to access the dashboard.</p>
  <form id="auth-form" method="post" action="/auth/key">
    <div class="input-group">
      <input type="text" id="api-key" placeholder="API Key" autocomplete="off" spellcheck="false" required>
    </div>
    <button type="submit" id="submit-btn">Connect</button>
  </form>
  <div class="error" id="error-msg"></div>
  <div class="success" id="success-msg"></div>
</div>
<script>
  // Auto-detect API key from URL query param (from QR code scan)
  const params = new URLSearchParams(window.location.search);
  const urlKey = params.get('api_key');
	const requestedReturnTo = params.get('return_to');
	const returnTo = requestedReturnTo && requestedReturnTo.startsWith('/') && !requestedReturnTo.startsWith('//')
		? requestedReturnTo
		: '/';
  if (urlKey) {
    document.getElementById('api-key').value = urlKey;
    // Strip key from URL for security
		params.delete('api_key');
		const safeQuery = params.toString();
		history.replaceState(null, '', window.location.pathname + (safeQuery ? '?' + safeQuery : ''));
    // Auto-submit
    setTimeout(() => document.getElementById('auth-form').dispatchEvent(new Event('submit', {cancelable: true})), 100);
  }

  const form = document.getElementById('auth-form');
  const input = document.getElementById('api-key');
  const btn = document.getElementById('submit-btn');
  const errorEl = document.getElementById('error-msg');
  const successEl = document.getElementById('success-msg');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const key = input.value.trim();
    if (!key) return;

    btn.disabled = true;
    btn.textContent = 'Connecting...';
    errorEl.style.display = 'none';
    successEl.style.display = 'none';

    try {
      const resp = await fetch('/auth/key', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key: key }),
      });
      const data = await resp.json();
      if (data.ok) {
        successEl.textContent = 'Connected! Redirecting...';
        successEl.style.display = 'block';
		setTimeout(() => window.location.href = returnTo, 500);
      } else {
        errorEl.textContent = data.error || 'Invalid API key.';
        errorEl.style.display = 'block';
        btn.disabled = false;
        btn.textContent = 'Connect';
      }
    } catch (err) {
      errorEl.textContent = 'Network error. Please try again.';
      errorEl.style.display = 'block';
      btn.disabled = false;
      btn.textContent = 'Connect';
    }
  });
</script>
</body>
</html>`
