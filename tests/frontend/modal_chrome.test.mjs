// modal_chrome.js's dialog table (#3857): a backdrop click closes only the dialogs marked for it, through their own hide function where
// they have one; Escape closes the topmost visible dialog (its hide function, else display none) and only that one.
//   node modal_chrome.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
await importUi('modal_chrome');
const calls = [];
for (const n of ['hideLaunchModal', 'hideAlertModal', 'hideTeamWizardModal']) window[n] = () => calls.push(n);
const dialog = (id, shown = true) => {
    const m = document.createElement('div'); m.className = 'modal'; m.id = id; m.style.display = shown ? 'block' : 'none';
    m.getClientRects = () => (shown ? [{}] : []);
    document.body.appendChild(m); return m;
};
const esc = () => document.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

const launch = dialog('launch-modal'), macro = dialog('macro-modal'), wizard = dialog('team-wizard-modal'), other = dialog('job-modal');
launch.click();
assert.deepEqual(calls, ['hideLaunchModal'], 'a backdrop click calls the dialog\'s hide function');
macro.click();
assert.equal(macro.style.display, 'none', 'a dialog with no hide function is hidden');
wizard.click();
assert.deepEqual(calls, ['hideLaunchModal'], 'the team wizard is not closed by a backdrop click here (it wires its own)');
assert.equal(wizard.style.display, 'block');
other.click();
assert.equal(other.style.display, 'block', 'an unlisted dialog is not closed by a backdrop click');

calls.length = 0;
// the topmost visible one is the last in the document: job-modal, then team-wizard-modal
esc();
assert.equal(other.style.display, 'none', 'Escape hides an unlisted dialog by display none');
assert.deepEqual(calls, [], 'and only that one');
esc();
assert.deepEqual(calls, ['hideTeamWizardModal'], 'then the next visible one, through its hide function');
wizard.style.display = 'none'; wizard.getClientRects = () => [];
esc();
assert.deepEqual(calls, ['hideTeamWizardModal', 'hideLaunchModal'], 'then the launch dialog');
console.log('modal_chrome: ok');
process.exit(0); // importing modal_chrome loads controls.js, whose timers would keep node alive
