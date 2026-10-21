// switchRow must emit the markup each caller used to write by hand (the strings below are copies of it), so the DOM, the styles and the golden traces do not move.
import assert from 'node:assert/strict';
import { switchRow } from '../../coral-go/pkg/frontend/static/switch_row.js';

// team_cross_team_settings.js: label wraps the box; description ids come from describedBy.
assert.equal(switchRow({ label: 'Cross-team messaging', name: 'cross_team_messaging', checked: true, disabled: true, describedBy: ['xt-changed-cross_team_messaging', 'xt-hub-may'] }),
    '<label><input type="checkbox" name="cross_team_messaging" checked disabled aria-describedby="xt-changed-cross_team_messaging xt-hub-may"> Cross-team messaging</label>');
assert.equal(switchRow({ label: 'Share files with other teams', name: 'cross_team_artifacts' }),
    '<label><input type="checkbox" name="cross_team_artifacts"> Share files with other teams</label>');
// editions.js and mobile.js: label class and extra attributes.
assert.equal(switchRow({ label: 'Coral Hub can turn on auto-accept', labelClass: 'editions-auto-accept', attrs: ' data-cloud="allow-auto-accept"' }),
    '<label class="editions-auto-accept"><input type="checkbox" data-cloud="allow-auto-accept"> Coral Hub can turn on auto-accept</label>');
assert.equal(switchRow({ label: 'Show server', labelClass: 'list-display-option', labelAttrs: ' data-display-row="server" hidden', attrs: ' data-display-input="server" data-on-change="toggle-show-server"' }),
    '<label class="list-display-option" data-display-row="server" hidden><input type="checkbox" data-display-input="server" data-on-change="toggle-show-server"> Show server</label>');
// team_health_settings.js: a rule head names the switch by its title and describes it by the sentence after the label.
assert.equal(switchRow({ label: 'Nudge an idle agent', name: 'claim_nudge_on', checked: true, labelClass: 'health-rule-head', labelId: 'hr-claim_nudge-t', desc: 'Why.', descId: 'hr-claim_nudge-w', descTag: 'p', descClass: 'health-why' }),
    '<label class="health-rule-head"><input type="checkbox" name="claim_nudge_on" checked aria-labelledby="hr-claim_nudge-t" aria-describedby="hr-claim_nudge-w"> <span id="hr-claim_nudge-t">Nudge an idle agent</span></label><p class="health-why" id="hr-claim_nudge-w">Why.</p>');
// ... or holds the sentence inside the label, under a bold title.
assert.equal(switchRow({ label: 'Send it', name: 'on', labelClass: 'health-rule-head', labelId: 'hc-on-t', titleTag: 'b', desc: 'Off.', descId: 'hc-on-w', descClass: 'health-why', descInside: true, sep: '<br>', gap: '' }),
    '<label class="health-rule-head"><input type="checkbox" name="on" aria-labelledby="hc-on-t" aria-describedby="hc-on-w"><span><b id="hc-on-t">Send it</b><br><span class="health-why" id="hc-on-w">Off.</span></span></label>');
// servers.js allow_private (the reading change): the title names the switch, the warning inside the label describes it; before, the warning was part of the name.
assert.equal(switchRow({ label: 'Allow private network address', name: 'allow_private', checked: true, labelClass: 'servers-check', labelId: 'servers-private-t', desc: 'Only tick this.', descId: 'servers-private-d', descClass: 'servers-help servers-warning', descInside: true }),
    '<label class="servers-check"><input type="checkbox" name="allow_private" checked aria-labelledby="servers-private-t" aria-describedby="servers-private-d"> <span><span id="servers-private-t">Allow private network address</span><span class="servers-help servers-warning" id="servers-private-d">Only tick this.</span></span></label>');
// Text is escaped; a description id only counts when there is a description.
assert.equal(switchRow({ label: 'A <b> & "c"', name: 'x', descId: 'd' }), '<label><input type="checkbox" name="x"> A &lt;b&gt; &amp; &quot;c&quot;</label>');
console.log('switch_row_unit: ok');
