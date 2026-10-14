// auth_redirect.js (#3418): where a dashboard whose session ended goes, and the no-loop guard.
import assert from 'node:assert/strict';
import { authUrlFor, shouldRedirect, AUTH_BOUNCE_MS } from '../../coral-go/pkg/frontend/static/auth_redirect.js';

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
console.log('auth_redirect: ok');
