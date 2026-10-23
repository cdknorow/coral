#!/usr/bin/env python3
"""The release code: a 6-digit code the release owner enters when a release is promoted (#6274, RFC 6238, HMAC-SHA-1, 30-second steps).

    release_code.py verify   checks RELEASE_CODE against the seed in RELEASE_CODE_SEED (both from the environment, never from arguments)
    release_code.py show     prints the current code for the seed in RELEASE_CODE_SEED, or asked for without echo; for the release owner's own computer

Standard library only. The code is accepted for the current 30-second step and the one before it (about a minute); a step ahead, two behind, a wrong code, a malformed
one and a missing or bad seed are all refused. The check is constant-time over both steps. Nothing here prints the code or the seed except `show`, which prints the code
for the person who holds the seed, and `verify`'s first output line is the ::add-mask:: command that keeps the code out of the logs.

verify exits 0 when the code is right, 1 when it is not, 2 when it cannot be checked (no seed, a bad seed, no code, a malformed code, arguments given): a failure to check is never a pass.
"""
import base64
import binascii
import getpass
import hashlib
import hmac
import os
import re
import struct
import sys
import time

STEP = 30
DIGITS = 6
MIN_SEED_BYTES = 16  # RFC 4226 section 4: at least 128 bits; the enrolment makes 160


class CodeError(Exception):
    """The code or the seed cannot be checked (as opposed to being wrong)."""


def decode_seed(text):
    """The seed bytes from an RFC 4648 base32 string (spaces, dashes and case do not matter, padding is optional). Raises CodeError for anything else."""
    cleaned = re.sub(r"[\s-]", "", text or "").upper().rstrip("=")
    if not cleaned or not re.fullmatch(r"[A-Z2-7]+", cleaned):
        raise CodeError("the seed is missing or is not base32")
    padded = cleaned + "=" * (-len(cleaned) % 8)
    try:
        seed = base64.b32decode(padded)
    except (binascii.Error, ValueError):
        raise CodeError("the seed is not base32")
    if len(seed) < MIN_SEED_BYTES:
        raise CodeError("the seed is too short")
    return seed


def hotp(seed, counter, digits=DIGITS):
    mac = hmac.new(seed, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = mac[-1] & 0x0F
    value = struct.unpack(">I", mac[offset:offset + 4])[0] & 0x7FFFFFFF
    return str(value % (10 ** digits)).zfill(digits)


def totp(seed, now, digits=DIGITS):
    return hotp(seed, int(now) // STEP, digits)


def normalize_code(text):
    """The six digits typed, with the space or dash an authenticator shows between the halves allowed. Raises CodeError for anything else."""
    cleaned = re.sub(r"[\s-]", "", text or "")
    if not re.fullmatch(r"[0-9]{%d}" % DIGITS, cleaned):
        raise CodeError("the code is not %d digits" % DIGITS)
    return cleaned


def check(code, seed_text, now=None):
    """True when code is the right code for the current or the previous step. Raises CodeError when it cannot be checked."""
    seed = decode_seed(seed_text)
    typed = normalize_code(code)
    now = time.time() if now is None else now
    ok = False
    for back in (0, 1):  # both are always computed and compared: no early exit on the first
        counter = int(now) // STEP - back
        if counter < 0:
            continue  # a clock before 1970 has no such step (and the counter is unsigned)
        want = hotp(seed, counter)
        ok |= hmac.compare_digest(typed.encode(), want.encode())
    return ok


# What a typed code may look like: six digits, with one space or dash in the middle as an authenticator shows them, and nothing else.
CODE_SHAPE = re.compile(r"([0-9]{3})[ -]?([0-9]{3})")


def cmd_verify():
    """Checks the shape of the code FIRST: anything that is not six digits (a line break, a ::command::, a long string, nothing) is refused with a fixed message that contains none of it, and
    nothing is written to stdout, so a hostile workflow input never reaches the log at all. Only the normalised six digits are masked, as the first output line. Every dispatch also needs the
    release owner's approval as the environment's reviewer, so guessing a code costs an approval per try; this check has no memory of its own."""
    code, seed = os.environ.get("RELEASE_CODE", ""), os.environ.get("RELEASE_CODE_SEED", "")
    m = CODE_SHAPE.fullmatch(code)
    if not m:
        print("release code not checked: it is not six digits", file=sys.stderr)
        return 2
    digits = m.group(1) + m.group(2)
    print("::add-mask::" + digits, flush=True)
    try:
        right = check(digits, seed)
    except CodeError as e:
        print("release code not checked: %s" % e, file=sys.stderr)
        return 2
    if not right:
        print("release code refused", file=sys.stderr)
        return 1
    print("release code accepted")
    return 0


def cmd_show():
    seed = os.environ.get("RELEASE_CODE_SEED") or getpass.getpass("seed (not shown): ")
    try:
        now = time.time()
        code = totp(decode_seed(seed), now)
    except CodeError as e:
        print("cannot make a code: %s" % e, file=sys.stderr)
        return 2
    left = STEP - int(now) % STEP
    print("%s %s   (%d s left in this step)" % (code[:3], code[3:], left))
    return 0


def main(argv):
    if len(argv) != 2 or argv[1] not in ("verify", "show"):
        print(__doc__.strip().splitlines()[2].strip() + "\n" + __doc__.strip().splitlines()[3].strip(), file=sys.stderr)
        return 2
    return cmd_verify() if argv[1] == "verify" else cmd_show()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
