// fieldRow must emit the markup the health check wrote by hand (the number knob and the reviewers box), so the DOM, the accessible names and the styles do not move (#5790).
import assert from 'node:assert/strict';
import { fieldRow } from '../../coral-go/pkg/frontend/static/field_row.js';

// A number knob: the label and the unit name the box, the limits describe it.
assert.equal(fieldRow({ id: 'hk-claim_nudge_after_min', label: 'After', name: 'claim_nudge_after_min', type: 'number', value: 10, min: 5, max: 1440, unit: 'minutes idle', desc: '5 to 1440' }),
    '<label class="health-knob"><span id="hk-claim_nudge_after_min-l">After</span><span class="health-knob-box"><input type="number" name="claim_nudge_after_min" value="10" min="5" max="1440" step="1" inputmode="numeric" required aria-labelledby="hk-claim_nudge_after_min-l hk-claim_nudge_after_min-u" aria-describedby="hk-claim_nudge_after_min-d"> <span id="hk-claim_nudge_after_min-u">minutes idle</span></span><span class="health-limits" id="hk-claim_nudge_after_min-d">5 to 1440</span></label>');
// A wide text box whose description sits between the label text and the box, with a list after it.
assert.equal(fieldRow({ id: 'hk-r', label: 'Default reviewers', name: 'r', value: 'Boss, Dev', wide: true, descFirst: true, desc: 'Names.', attrs: { list: 'm', maxlength: 400 }, after: '<datalist id="m"></datalist>' }),
    '<label class="health-knob health-wide"><span id="hk-r-l">Default reviewers</span> <span class="health-limits" id="hk-r-d">Names.</span><input type="text" name="r" value="Boss, Dev" list="m" maxlength="400" aria-labelledby="hk-r-l" aria-describedby="hk-r-d"><datalist id="m"></datalist></label>');
// Text and values are escaped; numbers are numbers; no description means no aria-describedby.
assert.equal(fieldRow({ id: 'a"b', label: 'A <b> & "c"', name: 'n"', type: 'text', value: '<x>' }),
    '<label class="health-knob"><span id="a&quot;b-l">A &lt;b&gt; &amp; &quot;c&quot;</span><input type="text" name="n&quot;" value="&lt;x&gt;" aria-labelledby="a&quot;b-l"></label>');
assert.equal(fieldRow({ id: 'n', label: 'N', name: 'n', attrs: { 'on"x': 1, placeholder: '"<' } }), '<label class="health-knob"><span id="n-l">N</span><input type="text" name="n" value="" placeholder="&quot;&lt;" aria-labelledby="n-l"></label>'); // a bad attribute name is dropped, a value is escaped
assert.match(fieldRow({ id: 'n', label: 'N', name: 'n', type: 'number', value: '7abc', min: '1', max: '5x' }), /value="NaN" min="1" max="NaN"/);
console.log('field_row_unit: ok');
