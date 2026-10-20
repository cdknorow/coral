// The leading "<path> coral://artifacts/<hex>" pairs of a user turn (#5284): exactly an upload path followed by a 64-hex link, repeated; a user's own text is never eaten.
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const { splitSharedImages, attachmentText } = await import(path.resolve(here, '../../coral-go/pkg/frontend/static/shared_images.js'));

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

// the text Send types into the agent
assert.equal(attachmentText({ path: P1, artifact: { uri: `coral://artifacts/${H1}` } }), `${P1} coral://artifacts/${H1}`);
assert.equal(attachmentText({ path: P1, artifact: null }), P1);
assert.equal(attachmentText({ path: P1 }), P1);
console.log('shared_images: ok');
