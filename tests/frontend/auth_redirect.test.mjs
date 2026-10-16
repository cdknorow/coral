// A dashboard that is signed out (#3418, #3939): the sign-in URL carries this page as return_to (never a protocol-relative one), and a 401's
// body decides whether it means "signed out" (strict mode's dashboard_auth_required, or the auth middleware's plain 401) or is
// the caller's own refusal (an agent-only route, an identity error).
import assert from 'node:assert/strict';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { authUrlFor, isSignedOut401 } = await importUi('auth_redirect');
assert.equal(authUrlFor({ pathname: '/', search: '', hash: '' }), '/auth?return_to=%2F');
assert.equal(authUrlFor({ pathname: '/', search: '?view=team', hash: '#chat/abc' }), '/auth?return_to=%2F%3Fview%3Dteam%23chat%2Fabc', 'this page, query and hash kept');
assert.equal(authUrlFor({ pathname: '//evil.example/x', search: '', hash: '' }), '/auth?return_to=%2F', 'never a protocol-relative return');
assert.equal(authUrlFor({ pathname: '', search: '', hash: '' }), '/auth?return_to=%2F');

assert.equal(isSignedOut401(JSON.stringify({ error: 'Coral is locked: ...', code: 'dashboard_auth_required' })), true, 'strict mode: signed out');
assert.equal(isSignedOut401('Unauthorized\n'), true, "the auth middleware's plain 401 (API-key sign-in)");
assert.equal(isSignedOut401(''), true, 'no body: treated as signed out');
assert.equal(isSignedOut401(JSON.stringify({ error: 'session_token_required', message: '...' })), false, "an agent-only route's 401 is the caller's to show");
assert.equal(isSignedOut401(JSON.stringify({ error: 'x', code: 'cli_signature_required' })), false);
assert.equal(isSignedOut401(JSON.stringify({ error: 'x', code: 'invalid_credential' })), false);
console.log('auth_redirect: ok');
process.exit(0);
