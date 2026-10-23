#!/usr/bin/env python3
"""tools/verify_buildinfo.sh (#6052 follow-up): after -trimpath, `go version -m` has no -ldflags line, so the version stamp is gone from the build info of
every program that does not link internal/config. A program with no stamp passes only as a -trimpath build of the tagged commit, unmodified; every case
fails closed. The fixtures are real `go version -m` shapes (the released v1.4.158 coral-board, and the pre-trimpath shape)."""
import os
import subprocess
import unittest

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")
LIB = os.path.join(ROOT, "tools", "verify_buildinfo.sh")
TAG = "16326ff2aaddced5a1d7a61c45e83d0cac77c5a7"
OTHER = "2d847f2059eb20941876006147dd75a4bc268b0a"


def info(rev=TAG, mod="false", trim="true", ldflags="", tags="fts5", goos="linux"):
    lines = ["\tpath\tgithub.com/subgentic/coral/cmd/coral-board", "\tmod\tgithub.com/subgentic/coral\t(devel)\t",
             "\tbuild\t-buildmode=exe", "\tbuild\t-compiler=gc", f"\tbuild\t-tags={tags}"]
    if trim is not None:
        lines.append(f"\tbuild\t-trimpath={trim}")
    if ldflags:
        lines.append(f"\tbuild\t-ldflags=\"-s -w -X github.com/subgentic/coral/internal/config.Version={ldflags}\"")
    lines += ["\tbuild\tCGO_ENABLED=0", "\tbuild\tGOARCH=amd64", f"\tbuild\tGOOS={goos}"]
    if rev is not None:
        lines.append(f"\tbuild\tvcs.revision={rev}")
    if mod is not None:
        lines.append(f"\tbuild\tvcs.modified={mod}")
    return "coral-board: go1.25.1\n" + "\n".join(lines) + "\n"


def sh(script, gi=""):
    r = subprocess.run(["bash", "-c", f'. "{LIB}"; {script}'], capture_output=True, text=True, env=dict(os.environ, GI=gi))
    return r.returncode, r.stdout.strip()


def verdict(text, want="1.4.158", tagc=TAG):
    """what verify-release.sh does for one program: parse the build info, then judge it"""
    code, parsed = sh('bi_parse "$GI"', text)
    assert code == 0, parsed
    v, _t, tg, trim, rev, mod = parsed.split("|")
    return sh(f'bi_verdict "{want}" "{tagc}" "{v}" "{trim}" "{rev}" "{mod}" "{tg}"')


class Verdicts(unittest.TestCase):
    def test_parse_reads_every_field(self):
        code, out = sh('bi_parse "$GI"', info(ldflags="1.4.158"))
        self.assertEqual(out, f"1.4.158|fts5|linux/amd64|true|{TAG}|false")

    def test_the_old_stamped_shape_passes(self):
        rc, out = verdict(info(ldflags="1.4.158", trim=None))
        self.assertEqual((rc, out.split("|")[0]), (0, "ok"), out)

    def test_a_trimpath_build_of_the_tagged_commit_passes_without_a_stamp(self):
        rc, out = verdict(info())
        self.assertEqual((rc, out.split("|")[0]), (0, "ok"), out)
        self.assertIn("-trimpath build of the tagged commit 16326ff2a", out)

    def test_a_wrong_revision_fails(self):
        rc, out = verdict(info(rev=OTHER))
        self.assertEqual(rc, 1, out)
        self.assertIn("is not the tag's commit", out)

    def test_a_stamped_program_with_a_wrong_revision_still_fails(self):
        rc, out = verdict(info(rev=OTHER, ldflags="1.4.158"))
        self.assertEqual(rc, 1, out)
        self.assertIn("is not the tag's commit", out)

    def test_a_windows_build_may_be_modified_but_only_by_the_regenerated_resources(self):
        # the Windows build rewrites the tracked rsrc_windows_amd64.syso files before compiling: modified=true there is by design, the revision still binds it
        rc, out = verdict(info(mod="true", goos="windows"))
        self.assertEqual((rc, out.split("|")[0]), (0, "ok"), out)
        for kw in (dict(mod="true", goos="windows", rev=OTHER), dict(mod=None, goos="windows"), dict(mod="maybe", goos="windows"), dict(mod="true", goos="linux"), dict(mod="true", goos="darwin")):
            rc, out = verdict(info(**kw))
            self.assertEqual(rc, 1, (kw, out))

    def test_a_modified_tree_fails(self):
        for mod in ("true", None, "maybe"):
            rc, out = verdict(info(mod=mod))
            self.assertEqual(rc, 1, (mod, out))
            self.assertIn("vcs.modified", out)

    def test_a_missing_revision_fails(self):
        rc, out = verdict(info(rev=None))
        self.assertEqual(rc, 1, out)
        self.assertIn("no vcs.revision", out)

    def test_a_short_or_upper_case_revision_fails(self):
        for rev in (TAG[:12], TAG.upper(), TAG + "0"):
            rc, out = verdict(info(rev=rev))
            self.assertEqual(rc, 1, (rev, out))
            self.assertIn("40-hex", out)

    def test_a_build_without_trimpath_and_without_a_stamp_fails(self):
        for trim in (None, "false"):
            rc, out = verdict(info(trim=trim))
            self.assertEqual(rc, 1, (trim, out))
            self.assertIn("not -trimpath=true", out)

    def test_a_wrong_stamped_version_fails(self):
        rc, out = verdict(info(ldflags="1.4.157"))
        self.assertEqual(rc, 1, out)
        self.assertIn("version '1.4.157'", out)

    def test_no_tag_commit_means_no_stamp_free_pass(self):
        rc, out = verdict(info(), tagc="-")
        self.assertEqual(rc, 1, out)
        rc2, out2 = verdict(info(ldflags="1.4.158"), tagc="-")   # a stamped program is judged by its stamp alone
        self.assertEqual(rc2, 0, out2)

    def test_the_strings_only_shape_keeps_working(self):
        # no go on the host: version and tags come from strings, with no vcs fields at all
        rc, out = sh('bi_verdict "1.4.158" "%s" "1.4.158" "" "" ""' % TAG)
        self.assertEqual(rc, 0, out)
        rc, out = sh('bi_verdict "1.4.158" "%s" "" "" "" ""' % TAG)
        self.assertEqual(rc, 1, out)

    def test_verify_release_uses_the_helper_and_reads_the_tag_from_the_repo(self):
        s = open(os.path.join(ROOT, "tools", "verify-release.sh")).read()
        self.assertIn('. "$here/verify_buildinfo.sh"', s)
        self.assertIn("bi_verdict", s)
        self.assertIn('gh api "repos/$REPO/commits/$TAG"', s)
        self.assertNotIn("none stamped')", s)   # the old inline version check is gone


if __name__ == "__main__":
    unittest.main()
