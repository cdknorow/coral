// The images leading a user turn (#5284, #5481): Send types only each image's coral://artifacts link and one hint (never the local path); the older "<path> coral://artifacts/<hex>" pairs still parse (a session's history has them);
// a user's own text is never eaten.
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const { splitSharedImages, attachmentText, attachmentsLead, imageHint, sentToastText } = await import(pathToFileURL(path.resolve(here, '../../coral-go/pkg/frontend/static/shared_images.js')).href);

const H1 = 'a'.repeat(64), H2 = 'b1'.repeat(32);
const P1 = '/home/dev/.coral/uploads/screenshot_20261019_101500_ab12.png';
const P2 = '/home/dev/.coral/uploads/9f8e7d6c_photo.jpg';

let r = splitSharedImages(`${P1} coral://artifacts/${H1} please look at this`);
assert.deepEqual(r.images, [{ path: P1, name: 'screenshot_20261019_101500_ab12.png', hex: H1 }]);
assert.equal(r.rest, 'please look at this');

r = splitSharedImages(`${P1} coral://artifacts/${H1} ${P2} coral://artifacts/${H2} and the text`);
assert.deepEqual(r.images.map(i => i.hex), [H1, H2], 'two images');
assert.equal(r.rest, 'and the text');

r = splitSharedImages(`${P1} coral://artifacts/${H1}`);
assert.equal(r.images.length, 1); assert.equal(r.rest, '', 'images only');

// a Windows path, and a folder with a space in it
r = splitSharedImages(`C:\\Users\\Jane Doe\\.coral\\uploads\\shot_ab12.png coral://artifacts/${H1} hi`);
assert.equal(r.images.length, 1, 'a Windows path with a space in a folder'); assert.equal(r.images[0].name, 'shot_ab12.png'); assert.equal(r.rest, 'hi');

// never eaten: the user's own text
for (const own of [
    `coral://artifacts/${H1} what is this`,                      // a link first, no upload path before it
    `look at coral://artifacts/${H1}`,                           // text first
    `/home/dev/notes.txt coral://artifacts/${H1} read it`,       // a path that is not in an uploads folder
    `${P1} coral://artifacts/${H1.slice(0, 63)} short`,          // 63 hex digits
    `${P1} coral://artifacts/${H1}0 long`,                       // 65
    `${P1} coral://artifacts/${H1.toUpperCase()} upper`,         // not the lower-case digest
    `${P1} https://example.com/${H1}`,                           // not an artifact link
    `${P1} hello coral://artifacts/${H1}`,                       // text between
]) {
    const o = splitSharedImages(own);
    assert.deepEqual(o.images, [], `not taken for an image: ${own.slice(0, 60)}`);
    assert.equal(o.rest, own, 'and the text is whole');
}
assert.deepEqual(splitSharedImages(null), { images: [], rest: '' });

// today's shape: links, one hint that carries the exact command for each link (#5496), the text
const L1 = `coral://artifacts/${H1}`, L2 = `coral://artifacts/${H2}`;
const HINT1 = imageHint([L1]), HINT12 = imageHint([L1, L2]);
assert.equal(HINT1, `(To read the image(s) above, run first: coral-agent artifact download ${L1} and open the path each prints.)`, 'the real link is in the command');
assert.equal(HINT12, `(To read the image(s) above, run first: coral-agent artifact download ${L1} ; coral-agent artifact download ${L2} and open the path each prints.)`, 'one command per link');
r = splitSharedImages(`${L1} ${HINT1} what is this?`);
assert.deepEqual(r.images, [{ path: '', name: 'Pasted image', hex: H1 }]); assert.equal(r.rest, 'what is this?');
r = splitSharedImages(`${L1} ${L2} ${HINT12} and the text`);
assert.deepEqual(r.images.map(i => [i.name, i.hex]), [['Pasted image 1', H1], ['Pasted image 2', H2]]); assert.equal(r.rest, 'and the text');
r = splitSharedImages(`${L1} ${HINT1}`); assert.equal(r.images.length, 1); assert.equal(r.rest, '', 'images only');
r = splitSharedImages(`  ${L1}\n${HINT1}\n\nthe text`); assert.equal(r.images.length, 1, 'whitespace between is any whitespace'); assert.equal(r.rest, 'the text');
// links with no hint, a changed hint, a hint naming other links or in another order, a link after text, a short or upper-case digest: the person's own text
for (const own of [`${L1} ${L2} what are these`, `${L1} ${HINT1.replace('download', 'fetch')} x`, `look ${L1} ${HINT1}`, `coral://artifacts/${H1.slice(0, 63)} ${HINT1}`, `coral://artifacts/${H1.toUpperCase()} ${HINT1}`, `${H1} ${HINT1}`,
    `${L1} ${imageHint([L2])} x`, `${L1} ${L2} ${imageHint([L2, L1])} x`, `${L1} ${L2} ${HINT1} x`, `${L1} ${HINT1.replace('coral-agent artifact download', 'coral-agent artifact download --output /etc/x')} x`]) {
    const o = splitSharedImages(own); assert.deepEqual(o.images, [], `not an image: ${own.slice(0, 50)}`); assert.equal(o.rest, own);
}

// the text Send types into the agent: the link only, and the hint once, with the command for each link
assert.equal(attachmentText({ path: P1, artifact: { uri: L1 } }), L1, 'the link, never the path');
assert.equal(attachmentText({ path: P1, artifact: null }), P1, 'no artifact: the path is all there is');
assert.equal(attachmentText({ path: P1 }), P1);
assert.deepEqual(attachmentsLead([{ path: P1, artifact: { uri: L1 } }]), [L1, HINT1]);
assert.deepEqual(attachmentsLead([{ path: P1, artifact: { uri: L1 } }, { path: P2, artifact: { uri: L2 } }]), [L1, L2, HINT12], 'one hint for two images');
assert.deepEqual(attachmentsLead([{ path: P1, artifact: null }]), [P1], 'no link, no hint');
assert.deepEqual(attachmentsLead([{ path: P1, artifact: null }, { path: P2, artifact: { uri: L2 } }]), [P1, L2, imageHint([L2])], 'the command names only the images that have a link');
assert.ok(!attachmentsLead([{ path: P1, artifact: { uri: L1 } }]).join(' ').includes('uploads'), 'the uploads path never reaches the agent');
// what Send types comes back as the same images in the chat
r = splitSharedImages([...attachmentsLead([{ path: P1, artifact: { uri: L1 } }, { path: P2, artifact: { uri: L2 } }]), 'and the question'].join(' '));
assert.deepEqual(r.images.map(i => i.hex), [H1, H2]); assert.equal(r.rest, 'and the question');
console.log('shared_images: ok');

// The "Sent" toast says the person's words, short: never the image links or the hint for the agent (#6137).
{
    const link = `coral://artifacts/${H1}`, hint = imageHint([`coral://artifacts/${H1}`]);
    assert.equal(sentToastText(`${link} ${hint}`), 'Sent', 'images only: just Sent');
    assert.equal(sentToastText(`${link} ${hint} what is this?`), 'Sent: what is this?', 'the words, not the links or the hint');
    assert.equal(sentToastText(`${P1} ${link} look`), 'Sent: look', 'the older form');
    assert.equal(sentToastText('first message'), 'Sent: first message', 'plain text as before');
    assert.equal(sentToastText('line one\nline two'), 'Sent: line one', 'one line');
    const long = sentToastText('x'.repeat(300));
    assert.ok(long.length <= 'Sent: '.length + 28 && long.endsWith('\u2026'), 'cut: ' + long.length);
    assert.equal(sentToastText(''), 'Sent');
    assert.ok(!/artifact|coral-agent|download/.test(sentToastText(`${link} ${hint} ok`)));
}
