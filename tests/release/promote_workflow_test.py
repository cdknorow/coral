#!/usr/bin/env python3
"""promote.yml and tools/promote_manifest.py (#6274). The workflow is read as text (no YAML library on a bare runner): what must hold is the order of the steps, who gets
which permission, that the code is checked before anything signs, that the file checks nothing out of main and that inputs only reach scripts through env."""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")
with open(os.path.join(ROOT, ".github", "workflows", "promote.yml"), encoding="utf-8") as _f:
    WF = _f.read()
CODE = "\n".join(re.sub(r"\s+#.*$", "", line) for line in WF.splitlines() if not line.lstrip().startswith("#"))   # the workflow without its comments
TOOL = os.path.join(ROOT, "tools", "promote_manifest.py")
MANIFEST_GO = os.path.join(ROOT, "coral-go", "internal", "updater", "verify", "manifest.go")
sys.path.insert(0, os.path.join(ROOT, "tools"))
import promote_manifest as pm  # noqa: E402

SHA = "a" * 40
SUMS = "b" * 64


def job_block(name):
    m = re.search(r"(?m)^  %s:\n(.*?)(?=^  [a-z][a-z-]*:\n|\Z)" % re.escape(name), WF, re.S)
    assert m, name
    return m.group(1)


def steps(block):
    parts = re.split(r"(?m)^      - ", block)[1:]
    return parts


def publish_problems(wf):
    """What is wrong with the step that publishes the promotion (an empty list when it is right)."""
    m = re.search(r"- name: Publish the promotion as its own release\n(.*?)(?=\n      - name: |\Z)", wf, re.S)
    if not m:
        return ["the publish step is missing"]
    t = m.group(1)
    out = []
    view = t.find('if gh release view "$name"')
    create = t.find('gh release create "$name"')
    upload = t.find('gh release upload "$name" promote.json promote.json.sigstore.json')
    edit = t.find('gh release edit "$name"')
    if not (0 <= view < create < upload < edit):
        out.append("the order is not: refuse an existing release, create, upload, publish")
    for need in ("--draft", "--prerelease", "--latest=false", '--target "${GITHUB_SHA}"'):
        if need not in t[create:upload if upload > create else len(t)]:
            out.append("create lacks " + need)
    if "--draft=false" not in t[edit:] or "--latest=false" not in t[edit:]:
        out.append("publish must be --draft=false and keep --latest=false")
    if 'name="promote-${PROMOTE_TAG}-${seq}"' not in t:
        out.append("the release is not named promote-<tag>-<sequence>")
    if "[[ \"$seq\" =~ ^[0-9]+$ ]]" not in t:
        out.append("the sequence is not checked to be a number")
    if 'gh release upload "${PROMOTE_TAG}"' in t or "--clobber" in t:
        out.append("a file is added to the product release or clobbered")
    return out


def release_step_lines(first, last):
    """The lines of the validate job's release step from the one starting with first to the one containing last, unindented: a piece of the real script to run."""
    script = re.search(r"- name: Read the release and check what release.yml signed\n.*?run: \|\n(.*?)(?=\n      - |\n\n  [a-z])", WF, re.S).group(1)
    lines = [l[10:] for l in script.split("\n")]
    start = next(i for i, l in enumerate(lines) if l.startswith(first))
    end = next(i for i in range(start, len(lines)) if last in lines[i])
    return "\n".join(lines[start:end + 1])


def shell_quote(s):
    return "'" + s.replace("'", "'\\''") + "'"


def run_bash(script):
    """Runs a piece of the workflow's script under bash with its options (set -euo pipefail); jq must be on PATH."""
    return subprocess.run(["bash", "-c", "set -euo pipefail\n" + script], capture_output=True, text=True)


def env(**kw):
    e = dict(PROMOTE_TAG="v1.4.160", PROMOTE_LIFETIME_DAYS="30", PROMOTE_REVOKE="", PROMOTE_PRERELEASE="false",
             PROMOTE_SOURCE_SHA=SHA, PROMOTE_SUMS_SHA256=SUMS, GITHUB_REPOSITORY="subgentic/coral")
    e.update(kw)
    return e


class Workflow(unittest.TestCase):
    def test_only_a_manual_dispatch_starts_it(self):
        on = re.search(r"(?m)^on:\n(.*?)(?=^[a-z]+:)", WF, re.S).group(1)
        self.assertEqual(re.findall(r"(?m)^  ([a-z_]+):", on), ["workflow_dispatch"])

    def test_the_top_level_grants_nothing_and_one_promotion_runs_at_a_time(self):
        self.assertRegex(WF, r"(?m)^permissions: \{\}$")
        self.assertRegex(WF, r"(?m)^concurrency:\n  group: promote\n  cancel-in-progress: false$")

    def test_only_the_promote_job_can_sign_and_write(self):
        self.assertEqual(len(re.findall(r"id-token: write", CODE)), 1)
        self.assertEqual(len(re.findall(r"contents: write", CODE)), 1)
        self.assertIn("id-token: write", job_block("promote"))
        self.assertIn("contents: write", job_block("promote"))
        self.assertIn("contents: read", job_block("validate"))
        self.assertNotIn("environment:", job_block("validate"))
        self.assertNotIn("secrets.", job_block("validate").replace("secrets.GITHUB_TOKEN", ""))

    def test_the_promote_job_is_in_its_own_environment_and_holds_the_seed(self):
        block = job_block("promote")
        self.assertRegex(block, r"(?m)^    environment: release-promote\b")
        self.assertEqual(WF.count("secrets.PROMOTE_TOTP_SEED"), 1)

    def test_the_code_is_masked_then_the_file_is_checked_out_without_a_ref_then_verified_before_anything_signs(self):
        s = steps(job_block("promote"))
        self.assertIn("name: Mask the release code", s[0])
        self.assertIn("uses: actions/checkout@", s[1])
        self.assertNotIn("ref:", s[1])
        self.assertIn("persist-credentials: false", s[1])
        self.assertIn("tools/release_code.py verify", s[2])
        self.assertIn("secrets.PROMOTE_TOTP_SEED", s[2])
        # nothing before the check holds the seed, signs or uploads
        for before in s[:2]:
            for word in ("PROMOTE_TOTP_SEED", "sign-blob", "gh release", "id-token"):
                self.assertNotIn(word, before)
        names = [re.search(r"name: (.*)", x).group(1) if "name:" in x else x.split("\n")[0] for x in s]
        self.assertLess(names.index("Check the release code"), min(i for i, n in enumerate(names) if n.startswith("Sign") or n.startswith("Upload")))

    def test_the_workflow_holds_only_visible_ascii(self):
        # U+2028 and U+2029 are line breaks to YAML: a typed escape that became the character itself broke the file once (the workflow did not parse)
        bad = [hex(ord(c)) for c in WF if (ord(c) > 126) or (ord(c) < 32 and c not in "\n")]
        self.assertEqual(bad, [])

    def test_the_mask_step_masks_every_line_of_a_hostile_code_and_prints_nothing_else(self):
        block = steps(job_block("promote"))[0]
        m = re.search(r"<<'PY'\n(.*?)\n          PY", block, re.S)
        self.assertTrue(m)
        code = "\n".join(line[10:] for line in m.group(1).split("\n"))
        hostile = "123456" + chr(10) + "::error::x" + chr(0x2028) + "::stop-commands::t" + chr(0x0b) + "tail" + chr(0x85) + "end" + chr(13) + "last"
        r = subprocess.run([sys.executable, "-I", "-c", code], env={"RELEASE_CODE": hostile}, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        lines = r.stdout.split("\n")[:-1]
        self.assertTrue(lines)
        for line in lines:
            self.assertTrue(line.startswith("::add-mask::"), line)
            for ch in (chr(10), chr(13), chr(0x0b), chr(0x0c), chr(0x85), chr(0x2028), chr(0x2029)):
                self.assertNotIn(ch, line)
        self.assertEqual([line[len("::add-mask::"):] for line in lines], ["123456", "::error::x", "::stop-commands::t", "tail", "end", "last"])

    def test_no_checkout_names_a_ref(self):
        self.assertNotRegex(WF, r"(?m)^\s+ref:")

    def test_every_action_is_pinned_to_a_commit(self):
        for line in re.findall(r"(?m)^\s+- uses: (\S+)|^\s+uses: (\S+)", WF):
            use = line[0] or line[1]
            self.assertRegex(use, r"@[0-9a-f]{40}$", use)

    def test_inputs_reach_scripts_only_through_env(self):
        for m in re.finditer(r"(?m)^        run: \|\n((?:          .*\n|\s*\n)+)", WF):
            self.assertNotIn("${{", m.group(1), m.group(1)[:80])
        for m in re.finditer(r"(?m)^        run: (.*)$", WF):
            self.assertNotIn("${{", m.group(1))

    def test_the_mask_step_prints_only_mask_commands(self):
        block = steps(job_block("promote"))[0]
        prints = re.findall(r"print\((.*)\)", block)
        self.assertEqual(len(prints), 1)
        self.assertTrue(prints[0].startswith('"::add-mask::"'))
        self.assertIn("RELEASE_CODE", block)

    def test_the_release_is_checked_against_the_exact_identity_and_the_signature_against_this_workflow(self):
        self.assertIn('--certificate-identity "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/.github/workflows/release.yml@refs/tags/${PROMOTE_TAG}"', WF)
        self.assertIn("--certificate-github-workflow-sha", WF)
        self.assertIn("--certificate-github-workflow-trigger push", WF)
        self.assertIn('--certificate-identity "${GITHUB_SERVER_URL}/${GITHUB_WORKFLOW_REF}"', WF)
        self.assertIn("--certificate-github-workflow-ref refs/heads/promote", WF)
        self.assertIn("--certificate-github-workflow-trigger workflow_dispatch", WF)
        self.assertNotIn("--certificate-identity-regexp", WF)

    def test_the_revoke_input_says_a_revocation_cannot_be_undone(self):
        self.assertIn("A revocation cannot be undone on a Coral that has seen it: a mistaken revoke is fixed by promoting a new version", WF)

    def test_the_copy_on_main_says_it_is_never_the_pinned_one(self):
        self.assertIn("THIS COPY ON MAIN IS NEVER THE PINNED ONE", WF)
        self.assertIn("tools/devops/promote-copy-check.sh", WF)

    def test_both_jobs_have_a_timeout(self):
        for job in ("validate", "promote"):
            self.assertRegex(job_block(job), r"(?m)^    timeout-minutes: 15\b", job)

    def test_the_summary_says_when_a_promotion_is_a_prerelease_and_lists_the_revoked_versions(self):
        up = steps(job_block("promote"))[-1]
        self.assertIn("jq -r .prerelease promote.json", up)
        self.assertIn("prerelease: true, clients will refuse this promotion", up)
        self.assertIn("Revoked versions in this manifest", up)
        self.assertIn(".revoked", up)

    def test_a_promotion_is_never_replaced(self):
        # This repository's releases are immutable (#6394): a second promotion is a new release with a higher sequence, so there is no replace input and no clobber.
        self.assertNotIn("replace:", re.search(r"(?m)^on:\n(.*?)(?=^[a-z]+:)", WF, re.S).group(1))
        self.assertNotIn("--clobber", WF)
        self.assertNotIn("PROMOTE_REPLACE", WF)

    def test_a_promotion_points_only_at_an_immutable_release(self):
        v = job_block("validate")
        self.assertIn('[ "$(jq -r .immutable <<<"$rel")" = true ]', v)
        self.assertIn("the release is not immutable", v)

    def test_a_promotion_is_its_own_draft_release_published_after_both_files(self):
        self.assertEqual(publish_problems(WF), [])

    @unittest.skipUnless(shutil.which("jq"), "jq is not on PATH (it is on the release runners)")
    def test_a_release_marked_revoked_is_not_promoted(self):
        # approve-release.sh's revoke writes "Revoked: <UTC> by <actor>" into the body; the same line refuses the promotion (Security's #337)
        lines = release_step_lines('body="$(jq', "then echo \"the release is marked revoked\"")
        for body, refused in (("notes\n\nRevoked: 2026-10-21T00:00:00Z by someone", True), ("notes\nRevoked: x", True), ("notes", False),
                              ("notes\n  Revoked: indented is not the marker", False), ("", False)):
            rel = json.dumps({"body": body})
            r = run_bash("rel=" + shell_quote(rel) + "\n" + lines + "\necho passed\n")
            self.assertEqual(r.returncode == 1 and "marked revoked" in r.stdout, refused, (body, r.stdout, r.stderr))
            if not refused:
                self.assertIn("passed", r.stdout)

    def test_the_tag_is_resolved_by_its_exact_ref_and_an_annotated_tag_is_peeled(self):
        # commits/<name> also takes a branch of that name; git/ref/tags/<tag> is the tag itself (Security's #337)
        self.assertNotIn("commits/${PROMOTE_TAG}", WF)
        lines = release_step_lines('obj="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/', "the tag's commit could not be read")
        c, t1, t2 = "c" * 40, "1" * 40, "2" * 40
        cases = (
            ({"git/ref/tags/v1.4.160": "commit " + c}, c),                                                    # a lightweight tag
            ({"git/ref/tags/v1.4.160": "tag " + t1, "git/tags/" + t1: "commit " + c}, c),                       # an annotated tag
            ({"git/ref/tags/v1.4.160": "tag " + t1, "git/tags/" + t1: "tag " + t2, "git/tags/" + t2: "commit " + c}, c),  # a tag of a tag
            ({"git/ref/tags/v1.4.160": "tag " + t1, "git/tags/" + t1: "tag " + t1}, None),                      # a loop never ends in a commit
            ({"git/ref/tags/v1.4.160": "tree " + c}, None),                                                   # not a commit
            ({"git/ref/tags/v1.4.160": "commit nothex"}, None),
            ({}, None),                                                                                       # no such tag
        )
        for answers, want in cases:
            fake = "gh() {\n  case \"$2\" in\n"
            for path, answer in answers.items():
                fake += "    repos/subgentic/coral/%s) echo %s ;;\n" % (path, shell_quote(answer))
            fake += "    *) echo \"gh: 404 $2\" >&2; return 1 ;;\n  esac\n}\n"
            r = run_bash(fake + "GITHUB_REPOSITORY=subgentic/coral\nPROMOTE_TAG=v1.4.160\n" + lines + '\necho "resolved $sha"\n')
            if want:
                self.assertEqual(r.returncode, 0, (answers, r.stdout, r.stderr))
                self.assertIn("resolved " + want, r.stdout)
            else:
                self.assertNotEqual(r.returncode, 0, (answers, r.stdout))
                self.assertNotIn("resolved", r.stdout)

    def test_the_publish_check_notices_each_mistake(self):
        mutants = {
            "added to the product release": WF.replace('gh release upload "$name" promote.json', 'gh release upload "${PROMOTE_TAG}" promote.json', 1),
            "not a draft first": WF.replace("--draft --prerelease", "--prerelease", 1),
            "not a prerelease": WF.replace("--draft --prerelease", "--draft", 1),
            "can become Latest": WF.replace("--prerelease --latest=false", "--prerelease", 1),
            "published before the files": WF.replace('gh release edit "$name" --repo "${GITHUB_REPOSITORY}" --draft=false --latest=false\n', "", 1).replace(
                'gh release upload "$name"', 'gh release edit "$name" --repo "${GITHUB_REPOSITORY}" --draft=false --latest=false\n          gh release upload "$name"', 1),
            "a name without the tag": WF.replace('name="promote-${PROMOTE_TAG}-${seq}"', 'name="promote-${seq}"', 1),
            "an existing release is not refused": WF.replace('if gh release view "$name"', 'if false && gh release view "$name"', 1),
            "the sequence is not checked": WF.replace('[[ "$seq" =~ ^[0-9]+$ ]]', "true", 1),
            "pinned to no commit": WF.replace('--target "${GITHUB_SHA}"', "", 1),
        }
        for name, text in mutants.items():
            self.assertNotEqual(text, WF, name)
            self.assertNotEqual(publish_problems(text), [], name)

    def test_the_release_name_is_the_shape_the_client_looks_for(self):
        # The client (internal/updater/stage, #6393) lists releases and keeps only promote-<product tag>-<sequence>: this is its pattern, with the tag a plain version
        # (the client's own file holds the same expression; a change there is a change here).
        client = re.compile(r"^promote-(v[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4})-([0-9]{1,12})$")
        line = re.search(r'(?m)^\s*name="(promote-[^"]*)"$', WF).group(1)
        self.assertEqual(line, "promote-${PROMOTE_TAG}-${seq}")
        manifest = pm.build(env(), now=1761200000)
        name = line.replace("${PROMOTE_TAG}", manifest["tag"]).replace("${seq}", str(manifest["sequence"]))
        m = client.match(name)
        self.assertIsNotNone(m, name)
        self.assertEqual("v" + manifest["version"], m.group(1))
        self.assertEqual(manifest["sequence"], int(m.group(2)))
        # a sequence is epoch seconds: ten digits today, well inside the client's twelve
        self.assertLessEqual(len(str(manifest["sequence"])), 12)

    def test_no_release_listing_can_pick_a_promote_release(self):
        # A published promote-<tag>-<sequence> release is a pre-release that is newer than the product it promotes: any place that takes "the newest release"
        # from a list must filter to product tags (v and a digit), or to non-pre-releases, or it would pick a release with no packages in it.
        listing = re.compile(r"gh release list|releases\?per_page|releases\"\)")
        offenders = []
        for rel in subprocess.check_output(["git", "ls-files", ".github/workflows", "tools"], cwd=ROOT, text=True).split():
            if rel.endswith("promote_workflow_test.py") or not rel.endswith((".yml", ".sh", ".py")):
                continue
            with open(os.path.join(ROOT, rel), encoding="utf-8") as f:
                body = f.read()
            if listing.search(body) and not re.search(r"\^v\[0-9\]|\^v\\d|prerelease==false|isPrerelease == false", body):
                offenders.append(rel)
        self.assertEqual(offenders, [], "these list releases without skipping promote releases")


class Manifest(unittest.TestCase):
    def test_the_fields_are_exactly_the_clients_schema(self):
        with open(MANIFEST_GO, encoding="utf-8") as f:
            tags = re.findall(r'json:"([a-z0-9_]+)"', f.read().split("type Manifest struct")[1].split("}")[0])
        self.assertEqual(sorted(pm.build(env(), now=1_700_000_000)), sorted(tags))

    def test_a_release_manifest(self):
        m = pm.build(env(), now=1_700_000_000)
        self.assertEqual((m["schema"], m["repo"], m["tag"], m["version"], m["prerelease"], m["sequence"]), (1, "subgentic/coral", "v1.4.160", "1.4.160", False, 1_700_000_000))
        self.assertEqual((m["promoted_at"], m["expires_at"]), ("2023-11-14T22:13:20Z", "2023-12-14T22:13:20Z"))
        self.assertEqual(m["revoked"], [])

    def test_a_prerelease_manifest_says_so(self):
        self.assertTrue(pm.build(env(PROMOTE_PRERELEASE="true"), now=1)["prerelease"])

    def test_the_lifetime_is_bounded_under_the_clients_sixty_days(self):
        pm.build(env(PROMOTE_LIFETIME_DAYS="59"), now=1)
        for bad in ("0", "60", "-1", "3.5", "x", "1e1", "100"):
            with self.assertRaises(pm.InputError, msg=bad):
                pm.build(env(PROMOTE_LIFETIME_DAYS=bad), now=1)
        self.assertEqual(pm.parse_lifetime(""), 30)

    def test_revoked_versions_are_validated_deduplicated_and_capped(self):
        self.assertEqual(pm.parse_revoked("v1.4.101, 1.4.100  1.4.101"), ["1.4.100", "1.4.101"])
        for bad in ("1.4", "1.4.100;rm", "evil", "1.4.100\n::error::x", "1.4.100-rc1"):
            with self.assertRaises(pm.InputError, msg=bad):
                pm.parse_revoked(bad)
        with self.assertRaises(pm.InputError):
            pm.parse_revoked(",".join("1.4.%d" % i for i in range(21)))
        self.assertEqual(len(pm.parse_revoked(",".join("1.4.%d" % i for i in range(20)))), 20)

    def test_a_promotion_never_revokes_its_own_version(self):
        # every client would refuse such a manifest, and the channel would be stuck until the next promotion (Security's #337)
        for revoke in ("1.4.160", "v1.4.160", "1.4.100, 1.4.160"):
            with self.assertRaises(pm.InputError, msg=revoke):
                pm.build(env(PROMOTE_REVOKE=revoke), now=1)
            r = subprocess.run([sys.executable, "-I", TOOL, "check"], env=dict(PROMOTE_TAG="v1.4.160", PROMOTE_REVOKE=revoke), capture_output=True, text=True)
            self.assertEqual(r.returncode, 1, revoke)
            self.assertIn("the promoted version is in the versions to revoke", r.stderr)
        self.assertEqual(pm.build(env(PROMOTE_REVOKE="1.4.159, 1.4.161"), now=1)["revoked"], ["1.4.159", "1.4.161"])

    def test_a_tag_must_be_a_version_tag(self):
        for bad in ("1.4.160", "v1.4", "v1.4.160 ", " v1.4.160", "v1.4.160\n", "../v1.4.160", "v1.4.160;x", "v1.4.160/../x", "V1.4.160", "", "v1.4.160-rc1", "v1.4.160-beta", "v1.4.160-dev", "v1.4.160-forDropbox", "v1.4.160-"):
            with self.assertRaises(pm.InputError, msg=repr(bad)):
                pm.build(env(PROMOTE_TAG=bad), now=1)

    def test_the_facts_must_be_well_formed_and_the_repository_the_pinned_one(self):
        for key, bad in (("PROMOTE_SOURCE_SHA", "A" * 40), ("PROMOTE_SOURCE_SHA", "a" * 39), ("PROMOTE_SUMS_SHA256", "b" * 63), ("PROMOTE_PRERELEASE", "yes"), ("GITHUB_REPOSITORY", "evil/coral")):
            with self.assertRaises(pm.InputError, msg=key):
                pm.build(env(**{key: bad}), now=1)

    def test_an_error_never_repeats_the_input(self):
        try:
            pm.build(env(PROMOTE_REVOKE="SECRET-VALUE-123"), now=1)
        except pm.InputError as e:
            self.assertNotIn("SECRET-VALUE-123", str(e))

    def test_the_command_line_takes_everything_from_the_environment(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "promote.json")
            r = subprocess.run([sys.executable, "-I", TOOL, "build"], env=dict(env(PROMOTE_OUT=out)), capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            with open(out) as f:
                self.assertEqual(json.load(f)["tag"], "v1.4.160")
            r = subprocess.run([sys.executable, "-I", TOOL, "build", "extra"], env=dict(env(PROMOTE_OUT=out)), capture_output=True, text=True)
            self.assertEqual(r.returncode, 2)
            r = subprocess.run([sys.executable, "-I", TOOL, "build"], env=dict(env(PROMOTE_TAG="nope", PROMOTE_OUT=out)), capture_output=True, text=True)
            self.assertEqual(r.returncode, 1)
            r = subprocess.run([sys.executable, "-I", TOOL, "check"], env=dict(PROMOTE_TAG="v1.4.160", PROMOTE_LIFETIME_DAYS="", PROMOTE_REVOKE=""), capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)


if __name__ == "__main__":
    unittest.main()
