// The redaction of the evidence the Windows CDP runner uploads at a timeout (#6195, Security's two LOWs on 3a9d04aa7).
//   node redact_evidence.test.mjs
import assert from 'node:assert/strict';
import { redactEvidence } from './helpers/redact_evidence.mjs';

const r = (s, names) => redactEvidence(s, names);
// In an address or a log line.
assert.equal(r('GET /?login_token=abc123&x=1'), 'GET /?login_token=REDACTED&x=1');
assert.equal(r('http://127.0.0.1:1/p?key=k1 password=p2 token=t3'), 'http://127.0.0.1:1/p?key=REDACTED password=REDACTED token=REDACTED');
// A JSON member, any case.
assert.equal(r('{"token":"abc","n":1}'), '{"token":"REDACTED","n":1}');
assert.equal(r('{"Api_Key" : "k", "session_token":"s"}'), '{"Api_Key" : "REDACTED", "session_token":"REDACTED"}');
// A header or a bearer value.
assert.equal(r('Authorization: Bearer abc.def-ghi'), 'Authorization: REDACTED');
assert.equal(r('authorization=Basic dXNlcjpwYXNz'), 'authorization=REDACTED');
assert.equal(r('sent Bearer abc123xyz to the hub'), 'sent Bearer REDACTED to the hub');
assert.equal(r('X-Coral-Session-Token: s3cr3t'), 'X-Coral-Session-Token: REDACTED');
// Names of four characters or more, everywhere; a shorter one is left alone.
assert.equal(r('C:\\Users\\runneradmin\\x on RUNNERVM1', ['runneradmin', 'RUNNERVM1']), 'C:\\Users\\<redacted>\\x on <redacted>');
assert.equal(r('a bob b', ['bob', 'ab']), 'a bob b');
// Ordinary text is untouched.
assert.equal(r('the key idea: keyboard tokens'), 'the key idea: keyboard tokens');
console.log('redact_evidence: ok');
