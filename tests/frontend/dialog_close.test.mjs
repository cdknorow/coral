// Close on backdrop click or Escape, with the Escape listener released on close (#3857): a click on the backdrop itself closes, a click
// inside does not; Escape closes; after releaseEscape Escape does nothing; wiring a dialog twice leaves one Escape listener.
//   node dialog_close.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { closeOnBackdropAndEscape, releaseEscape } = await importUi('dialog_close');
const modal = document.createElement('div');
const inner = document.createElement('button');
modal.appendChild(inner);
document.body.appendChild(modal);
const esc = key => document.dispatchEvent(new window.KeyboardEvent('keydown', { key, bubbles: true }));

let closed = 0;
closeOnBackdropAndEscape(modal, () => { closed++; });
inner.click();
assert.equal(closed, 0, 'a click inside the dialog does not close it');
modal.click();
assert.equal(closed, 1, 'a click on the backdrop closes it');
esc('Enter'); assert.equal(closed, 1, 'another key does nothing');
esc('Escape'); assert.equal(closed, 2, 'Escape closes it');
closeOnBackdropAndEscape(modal, () => { closed++; });
esc('Escape'); assert.equal(closed, 3, 'wired again while open: one Escape listener, one close');
releaseEscape(modal);
esc('Escape'); assert.equal(closed, 3, 'after releaseEscape, Escape does nothing');
assert.equal(modal._escHandler, null);
assert.doesNotThrow(() => releaseEscape(modal), 'releasing with nothing wired is fine');
console.log('dialog_close: ok');
