// Coral's link policy for the shared renderer (#5754, Security's condition 2 on #5750): it owns only fixed same-origin shapes, only after a lookup that the id exists on this server, and never builds a path
// from agent text; everything else is undefined, which leaves the url to the renderer's default (http, https, mailto) or its refusal. One test per refusal; the policy cases of the renderer itself
// (a throwing policy, a javascript: href, a protocol-relative one, ...) are in fixtures/markdown-corpus.mjs, run in a real browser by hostile_markdown_browser.
import assert from 'node:assert/strict';
import { makeLinkPolicy } from '../../coral-go/pkg/frontend/static/safe_markdown.js';

const HEX = 'a1b2c3d4'.repeat(8);
const known = new Set([HEX]);
const policy = makeLinkPolicy({ artifactExists: hex => known.has(hex) });

// owned: a known artifact, as a fixed same-origin path
const owned = policy(`coral://artifacts/${HEX}`);
assert.deepEqual(owned, { href: `/api/artifacts/${HEX}`, shown: `artifact ${HEX.slice(0, 8)}`, internal: true }, 'a known artifact is owned, as one fixed path');
assert.ok(owned.href.startsWith('/') && !owned.href.startsWith('//') && !/[\\\s\u0000-\u001F]/.test(owned.href), 'the owned href is a same-origin absolute path');

// every refusal answers undefined (the renderer's default policy then decides)
const refusals = {
    'an artifact id the lookup does not know': `coral://artifacts/${'b'.repeat(64)}`,
    'an id that is not 64 hex digits': 'coral://artifacts/abc',
    'an id in capitals': `coral://artifacts/${HEX.toUpperCase()}`,
    'an id with a path after it': `coral://artifacts/${HEX}/../x`,
    'an id with a query': `coral://artifacts/${HEX}?x=1`,
    'a different host': `coral://elsewhere/${HEX}`,
    'a javascript: url': 'javascript:alert(1)',
    'a data: url': 'data:text/html,x',
    'a protocol-relative url': `//evil.example/${HEX}`,
    'a plain path': '/api/keys',
    'an https url': 'https://example.com/x',
    'a blank': '',
};
for (const [name, url] of Object.entries(refusals)) assert.equal(policy(url), undefined, `${name} is left to the default policy`);
for (const bad of [undefined, null, 7, {}, [], true]) assert.equal(policy(bad), undefined, `a non-string (${JSON.stringify(bad)}) is not owned`);

// no lookup, or a lookup that does not say exactly true: nothing is owned (the id must be known to exist)
for (const lookups of [undefined, {}, { artifactExists: undefined }, { artifactExists: () => 1 }, { artifactExists: () => 'yes' }, { artifactExists: () => undefined }, { artifactExists: () => false }]) {
    assert.equal(makeLinkPolicy(lookups)(`coral://artifacts/${HEX}`), undefined, 'without a lookup that answers true, an artifact link is not owned: ' + JSON.stringify(Object.keys(lookups || {})));
}
// the text decides nothing about the path: the shape is built from the matched hex only
assert.equal(policy(`coral://artifacts/${HEX}`).href, `/api/artifacts/${HEX}`);
console.log('safe_markdown_policy: ok');
