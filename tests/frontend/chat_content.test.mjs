// The live chat's content interface (#3837): transcript content goes before the trailing chrome rows (Working, needs-input, empty
// state, pending messages), lastContent skips them, and a turn's work folds into one group with its final reply kept apart.
//   node chat_content.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const c = await importUi('chat_content');
const el = (cls, text = '') => { const d = document.createElement('div'); d.className = cls; d.textContent = text; return d; };
const tool = label => { const d = el('tool-call'); d.innerHTML = `<span class="tool-call-label">${label}</span>`; return d; };
const classes = box => [...box.children].map(n => n.className);

const box = document.createElement('div');
assert.equal(c.lastContent(box), null, 'an empty container has no last content');
c.appendContent(box, el('chat-bubble', 'one'));
box.appendChild(el('chat-working'));
box.appendChild(el('pending-messages'));
assert.equal(c.lastContent(box).textContent, 'one', 'lastContent skips the trailing chrome');
c.appendContent(box, el('chat-bubble', 'two'));
assert.deepEqual(classes(box), ['chat-bubble', 'chat-bubble', 'chat-working', 'pending-messages'], 'content goes before the chrome');
for (const cls of ['chat-needs-input', 'chat-empty']) {
    const b = document.createElement('div'); b.appendChild(el('x')); b.appendChild(el(cls));
    assert.equal(c.lastContent(b).className, 'x', cls + ' is trailing chrome');
}
assert.equal(c.TRAILING_CHROME, '.chat-working, .chat-needs-input, .chat-empty, .pending-messages');

const turn = document.createElement('div');
c.appendContent(turn, el('chat-bubble human', 'ask'));
turn.appendChild(el('chat-working'));
c.appendWork(turn, tool('Read a.go'));
assert.deepEqual(classes(turn), ['chat-bubble human', 'work-group', 'chat-working'], 'the first step starts a work group before the chrome');
c.appendWork(turn, tool('Edit a.go'));
const group = turn.querySelector('.work-group');
assert.equal(group.querySelectorAll(':scope > .work-group-body > .tool-call').length, 2, 'later steps join the same group while the Working row shows');
c.updateWorkGroupSummary(group);
assert.equal(group.querySelector('.work-group-count').textContent, '2 steps');
assert.equal(group.querySelector('.work-group-latest').textContent, 'Edit a.go', 'the summary names the latest tool');
c.appendTurnFinal(turn, el('chat-bubble agent', 'done'));
assert.deepEqual(classes(turn), ['chat-bubble human', 'work-group', 'chat-bubble agent turn-final', 'chat-working']);
c.appendWork(turn, tool('Run tests'));
assert.deepEqual(classes(turn), ['chat-bubble human', 'work-group', 'chat-working'], 'more work folds the earlier final reply back into the group');
assert.equal(turn.querySelector('.work-group-body').children.length, 4, 'two steps, the folded reply and the new step');
assert.equal(c.htmlToElement('  <b>x</b> ').tagName, 'B');
console.log('chat_content: ok');
