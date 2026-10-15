// auth_redirect.js (#3418): where a dashboard whose session ended goes, and the no-loop guard.
import assert from 'node:assert/strict';
import { authUrlFor, shouldRedirect, AUTH_BOUNCE_MS, isSignedOut401 } from '../../coral-go/pkg/frontend/static/auth_redirect.js';

assert.equal(authUrlFor({ pathname: '/', search: '', hash: '' }), '/auth?return_to=%2F');
assert.equal(authUrlFor({ pathname: '/', search: '?view=team', hash: '#chat/abc' }), '/auth?return_to=%2F%3Fview%3Dteam%23chat%2Fabc', 'this page, query and hash');
assert.equal(authUrlFor({ pathname: '//evil.example/x', search: '', hash: '' }), '/auth?return_to=%2F', 'never a protocol-relative return');
assert.equal(authUrlFor({ pathname: '', search: '', hash: '' }), '/auth?return_to=%2F');
// Redirect once; not again within the bounce window; again after it (or with nothing recorded).
const now = 1_000_000;
assert.equal(shouldRedirect(now, NaN), true, 'nothing recorded');
assert.equal(shouldRedirect(now, now - 1000), false, 'sent 1 s ago: stay');
assert.equal(shouldRedirect(now, now - AUTH_BOUNCE_MS + 1), false);
assert.equal(shouldRedirect(now, now - AUTH_BOUNCE_MS), true, 'after the window');
assert.equal(shouldRedirect(now, now + 5000), true, 'a clock that went backwards does not block forever');
// Only a 401 that means signed out sends the page to /auth (#3528).
assert.equal(isSignedOut401(JSON.stringify({ error: 'Coral is locked: ...', code: 'dashboard_auth_required' })), true, 'strict mode: signed out');
assert.equal(isSignedOut401('Unauthorized\n'), true, "the auth middleware's plain 401 (API-key sign-in)");
assert.equal(isSignedOut401(''), true, 'no body: treated as signed out');
assert.equal(isSignedOut401(JSON.stringify({ error: 'session_token_required', message: '...' })), false, "an agent-only route's 401 is the caller's");
assert.equal(isSignedOut401(JSON.stringify({ error: 'x', code: 'cli_signature_required' })), false);
assert.equal(isSignedOut401(JSON.stringify({ error: 'x', code: 'invalid_credential' })), false);
console.log('auth_redirect: ok');
