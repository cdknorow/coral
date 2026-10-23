// cloud_words.js: the closed permissions summary (permissive switches named, a protective one off marked), the Operator sentence and the spelled code.
import assert from 'node:assert/strict';
import { morePermsWords, operatorLine, spellCode } from '../../coral-go/pkg/frontend/static/cloud_words.js';

assert.deepEqual(morePermsWords({}, null), { text: 'More Coral Hub permissions: may change cross-team settings', warn: '' }, 'cross-team is on unless turned off');
assert.equal(morePermsWords({ hub_cross_team_settings: false }, false).text, 'More Coral Hub permissions: none on');
assert.equal(morePermsWords({ hub_cross_team_settings: true }, true).text, 'More Coral Hub permissions: may change cross-team settings and act as the Operator');
assert.equal(morePermsWords({ hub_cross_team_settings: true, hub_relaunch_updates: true }, true).text, 'More Coral Hub permissions: may change cross-team settings, act as the Operator and relaunch Coral to install updates');
assert.equal(morePermsWords({ hub_cross_team_settings: false, hub_relaunch_updates: true }, false).text, 'More Coral Hub permissions: may relaunch Coral to install updates');
assert.equal(morePermsWords({ hub_relaunch_updates: false, hub_cross_team_settings: false }, null).text, 'More Coral Hub permissions: none on', 'an Operator this page cannot tell about is not claimed');
assert.equal(morePermsWords({ redact_tokens: true }, null).warn, '', 'tokens hidden: the safe state, no mark');
assert.equal(morePermsWords({}, null).warn, '', 'unset is the default, on');
assert.equal(morePermsWords({ redact_tokens: false }, null).warn, 'Tokens are visible to view-only hub users');
assert.equal(operatorLine(true), 'Coral Hub may act as the Operator.');
assert.equal(operatorLine(false), 'Coral Hub may not act as the Operator.');
assert.equal(operatorLine(null), '');
assert.equal(spellCode('WXYZ-1234'), 'W X Y Z, 1 2 3 4');
assert.equal(spellCode('H2XR-HS16'), 'H 2 X R, H S 1 6');
assert.equal(spellCode(''), '');
assert.equal(spellCode(undefined), '');
console.log('cloud_words: ok');
