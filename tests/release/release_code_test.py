#!/usr/bin/env python3
"""tools/release_code.py (#6274): the RFC 6238 vectors, the window (current and previous step only), a wrong, stale, future, empty, malformed or unusable code or seed, the code
and the seed from the environment only, and add-mask as the first output. Standard library only."""
import base64
import os
import subprocess
import sys
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.join(HERE, "..", "..", "tools", "release_code.py")
sys.path.insert(0, os.path.join(HERE, "..", "..", "tools"))
import release_code as rc  # noqa: E402

RFC_SECRET = b"12345678901234567890"  # RFC 6238 appendix B, SHA-1
RFC_SEED = base64.b32encode(RFC_SECRET).decode()
SEED = base64.b32encode(bytes(range(1, 21))).decode()  # a 160-bit seed of the shape the enrolment makes


def run(args, env_extra=None, clear=True):
    env = {"PATH": os.environ.get("PATH", "")} if clear else dict(os.environ)
    env.update(env_extra or {})
    return subprocess.run([sys.executable, TOOL] + args, env=env, capture_output=True, text=True, stdin=subprocess.DEVNULL)


class Vectors(unittest.TestCase):
    def test_rfc_6238_sha1_vectors(self):
        # the RFC's 8-digit values, and their last six digits (the code the app shows)
        for t, eight in ((59, "94287082"), (1111111109, "07081804"), (1111111111, "14050471"), (1234567890, "89005924"), (2000000000, "69279037"), (20000000000, "65353130")):
            self.assertEqual(rc.totp(RFC_SECRET, t, 8), eight, t)
            self.assertEqual(rc.totp(RFC_SECRET, t, 6), eight[-6:], t)

    def test_seed_forms(self):
        self.assertEqual(rc.decode_seed(RFC_SEED), RFC_SECRET)
        self.assertEqual(rc.decode_seed(RFC_SEED.lower()), RFC_SECRET)
        spaced = " ".join(RFC_SEED[i:i + 4] for i in range(0, len(RFC_SEED), 4))
        self.assertEqual(rc.decode_seed(spaced), RFC_SECRET)
        self.assertEqual(rc.decode_seed(RFC_SEED.rstrip("=")), RFC_SECRET)


class Window(unittest.TestCase):
    now = 1700000000

    def code_at(self, t):
        return rc.totp(rc.decode_seed(SEED), t)

    def test_the_current_and_the_previous_step_are_accepted(self):
        self.assertTrue(rc.check(self.code_at(self.now), SEED, self.now))
        self.assertTrue(rc.check(self.code_at(self.now - rc.STEP), SEED, self.now))

    def test_older_and_future_steps_are_refused(self):
        self.assertFalse(rc.check(self.code_at(self.now - 2 * rc.STEP), SEED, self.now), "a code two steps old")
        self.assertFalse(rc.check(self.code_at(self.now - 10 * rc.STEP), SEED, self.now), "a stale code")
        self.assertFalse(rc.check(self.code_at(self.now + rc.STEP), SEED, self.now), "a code for the next step")

    def test_wrong_codes(self):
        good = self.code_at(self.now)
        wrong = str((int(good) + 1) % 1000000).zfill(6)
        self.assertFalse(rc.check(wrong, SEED, self.now))
        self.assertFalse(rc.check("000000" if good != "000000" else "000001", SEED, self.now))

    def test_the_authenticators_display_form_is_accepted(self):
        good = self.code_at(self.now)
        self.assertTrue(rc.check(good[:3] + " " + good[3:], SEED, self.now))

    def test_a_code_made_with_another_seed_is_refused(self):
        other = base64.b32encode(bytes(range(40, 60))).decode()
        self.assertFalse(rc.check(rc.totp(rc.decode_seed(other), self.now), SEED, self.now))


class Unusable(unittest.TestCase):
    def test_malformed_codes_cannot_be_checked(self):
        for bad in ("", "12345", "1234567", "abcdef", "12 34 56 7", "１２３４５６", None, "123456\n654321"):
            with self.assertRaises(rc.CodeError, msg=repr(bad)):
                rc.check(bad, SEED, 1700000000)

    def test_missing_or_bad_seeds_cannot_be_checked(self):
        for bad in ("", None, "not base32!", "1111111111111111", "AAAA", "A" * 25):
            with self.assertRaises(rc.CodeError, msg=repr(bad)):
                rc.check("123456", bad, 1700000000)

    def test_a_short_seed_is_refused_even_if_it_is_valid_base32(self):
        short = base64.b32encode(b"0123456789abcde").decode()  # 15 bytes
        with self.assertRaises(rc.CodeError):
            rc.decode_seed(short)


class Command(unittest.TestCase):
    def test_a_right_code_exits_0_and_masks_it_first(self):
        code = rc.totp(rc.decode_seed(SEED), time.time())
        r = run(["verify"], {"RELEASE_CODE": code, "RELEASE_CODE_SEED": SEED})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.splitlines()[0], "::add-mask::" + code, "add-mask is the first output")
        self.assertNotIn(SEED, r.stdout + r.stderr)

    def test_a_wrong_code_exits_1_and_the_code_is_masked_not_echoed(self):
        good = rc.totp(rc.decode_seed(SEED), time.time())
        wrong = str((int(good) + 7) % 1000000).zfill(6)
        r = run(["verify"], {"RELEASE_CODE": wrong, "RELEASE_CODE_SEED": SEED})
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.stdout.splitlines()[0], "::add-mask::" + wrong)
        self.assertNotIn(wrong, r.stderr)
        self.assertNotIn(SEED, r.stdout + r.stderr)

    def test_a_hostile_value_prints_nothing_of_itself(self):
        # the code is an input of the workflow: anything that is not six digits is refused before a single line is written to stdout, and the message holds none of the value
        hostile = ("123456\n::error::injected\n::stop-commands::x", "::error::x\r::warning::y", "12\x0b::stop-commands::z", "::add-mask::123456", "1234567", "12345", "123  456", "123-45-6", "12 3456\n", "123456\n", " 123456", "abcdef", "")
        for code in hostile:
            r = run(["verify"], {"RELEASE_CODE": code, "RELEASE_CODE_SEED": SEED})
            self.assertEqual(r.returncode, 2, (code, r.stdout, r.stderr))
            self.assertEqual(r.stdout, "", "nothing on stdout for %r" % code)
            self.assertNotIn("::", r.stderr)
            for piece in [x for x in code.replace("\r", "\n").split("\n") if x.strip()]:
                self.assertNotIn(piece, r.stderr)

    def test_only_the_normalised_digits_are_masked_and_the_display_forms_work(self):
        good = rc.totp(rc.decode_seed(SEED), time.time())
        for typed in (good, good[:3] + " " + good[3:], good[:3] + "-" + good[3:]):
            r = run(["verify"], {"RELEASE_CODE": typed, "RELEASE_CODE_SEED": SEED})
            self.assertEqual(r.returncode, 0, (typed, r.stderr))
            self.assertEqual(r.stdout.splitlines()[0], "::add-mask::" + good, "the digits only, as the first line")
            self.assertEqual([l for l in r.stdout.splitlines() if l.startswith("::")], ["::add-mask::" + good])

    def test_a_clock_before_the_first_step_is_a_refusal_not_a_crash(self):
        self.assertFalse(rc.check("123456", SEED, 10))
        self.assertFalse(rc.check("123456", SEED, 0))

    def test_nothing_that_cannot_be_checked_passes(self):
        good = rc.totp(rc.decode_seed(SEED), time.time())
        for env in ({}, {"RELEASE_CODE": good}, {"RELEASE_CODE_SEED": SEED}, {"RELEASE_CODE": "", "RELEASE_CODE_SEED": SEED}, {"RELEASE_CODE": good, "RELEASE_CODE_SEED": "AAAA"}, {"RELEASE_CODE": "12345", "RELEASE_CODE_SEED": SEED}):
            r = run(["verify"], env)
            self.assertEqual(r.returncode, 2, (env, r.stdout, r.stderr))

    def test_the_code_is_not_taken_from_arguments(self):
        good = rc.totp(rc.decode_seed(SEED), time.time())
        r = run(["verify", good], {"RELEASE_CODE": good, "RELEASE_CODE_SEED": SEED})
        self.assertEqual(r.returncode, 2)
        r = run(["verify"], {"RELEASE_CODE_SEED": SEED})  # the right code is not in the environment: not found by looking at argv or anywhere else
        self.assertEqual(r.returncode, 2)
        self.assertEqual(run([]).returncode, 2)
        self.assertEqual(run(["other"]).returncode, 2)

    def test_show_prints_a_code_the_check_accepts(self):
        r = run(["show"], {"RELEASE_CODE_SEED": SEED})
        self.assertEqual(r.returncode, 0, r.stderr)
        shown = r.stdout.split("(")[0].strip()
        self.assertTrue(rc.check(shown, SEED), shown)
        self.assertEqual(run(["show"], {"RELEASE_CODE_SEED": "AAAA"}).returncode, 2)


if __name__ == "__main__":
    unittest.main(verbosity=1)
