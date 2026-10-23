#!/usr/bin/env python3
"""promote.yml and tools/promote_manifest.py (#6274). The workflow is read as text (no YAML library on a bare runner): what must hold is the order of the steps, who gets
which permission, that the code is checked before anything signs, that the file checks nothing out of main and that inputs only reach scripts through env."""
import json
import os
import re
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

    def test_an_existing_promotion_is_replaced_only_when_asked(self):
        self.assertIn("--clobber", WF)
        self.assertIn('"${PROMOTE_REPLACE}" != true', WF)


class Carry(unittest.TestCase):
    def test_the_previous_manifest_is_verified_against_this_workflows_identity_before_its_list_is_read(self):
        block = job_block("validate")
        i = block.index("name: Carry the previous promotion's revocations forward")
        step = block[i:]
        self.assertLess(step.index("cosign verify-blob prev/promote.json"), step.index("tools/promote_manifest.py prev"))
        self.assertIn('--certificate-identity "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/.github/workflows/promote.yml@refs/heads/promote"', step)
        self.assertIn("--certificate-github-workflow-trigger workflow_dispatch", step)
        self.assertIn("set -euo pipefail", step)
        self.assertNotIn("|| true", step)   # a manifest that does not verify stops the run: it is never skipped

    def test_the_promote_job_gets_the_carried_list_and_the_unrevoke_input(self):
        block = job_block("promote")
        self.assertIn("PROMOTE_REVOKED_PREV: ${{ needs.validate.outputs.revoked_prev }}", block)
        self.assertIn("PROMOTE_UNREVOKE: ${{ inputs.unrevoke }}", block)


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

    def test_earlier_revocations_stay_unless_named_in_unrevoke(self):
        m = pm.build(env(PROMOTE_REVOKED_PREV="1.4.100,1.4.101", PROMOTE_REVOKE="1.4.102"), now=1)
        self.assertEqual(m["revoked"], ["1.4.100", "1.4.101", "1.4.102"])
        m = pm.build(env(PROMOTE_REVOKED_PREV="1.4.100,1.4.101", PROMOTE_REVOKE="1.4.101", PROMOTE_UNREVOKE="v1.4.100"), now=1)
        self.assertEqual(m["revoked"], ["1.4.101"])

    def test_the_previous_list_is_validated_and_bounded(self):
        with self.assertRaises(pm.InputError):
            pm.build(env(PROMOTE_REVOKED_PREV="1.4.100;rm"), now=1)
        with self.assertRaises(pm.InputError):
            pm.build(env(PROMOTE_REVOKED_PREV=",".join("1.4.%d" % i for i in range(513))), now=1)
        pm.build(env(PROMOTE_REVOKED_PREV=",".join("1.4.%d" % i for i in range(512))), now=1)
        with self.assertRaises(pm.InputError):
            pm.build(env(PROMOTE_REVOKED_PREV=",".join("1.4.%d" % i for i in range(512)), PROMOTE_REVOKE="2.0.0"), now=1)

    def test_the_previous_manifest_is_read_strictly(self):
        with tempfile.TemporaryDirectory() as d:
            def read(obj):
                path = os.path.join(d, "p.json")
                with open(path, "w") as f:
                    f.write(obj if isinstance(obj, str) else json.dumps(obj))
                return pm.read_previous(path)
            self.assertEqual(read({"schema": 1, "revoked": ["1.4.2", "1.4.1", "1.4.2"]}), ["1.4.1", "1.4.2"])
            self.assertEqual(read({"schema": 1, "revoked": []}), [])
            for bad in ({"schema": 2, "revoked": []}, {"schema": 1}, {"schema": 1, "revoked": ["1.4"]}, {"schema": 1, "revoked": [1]}, {"schema": 1, "revoked": ["1.4.1-rc1"]}, [], "not json"):
                with self.assertRaises((pm.InputError, ValueError), msg=str(bad)):
                    read(bad)
            r = subprocess.run([sys.executable, "-I", TOOL, "prev"], env=dict(PROMOTE_PREV_FILE=os.path.join(d, "missing.json")), capture_output=True, text=True)
            self.assertEqual(r.returncode, 1)

    def test_a_tag_must_be_a_version_tag(self):
        for bad in ("1.4.160", "v1.4", "v1.4.160 ", "v1.4.160\n", "../v1.4.160", "v1.4.160;x", "V1.4.160", "", "v1.4.160-rc1", "v1.4.160-"):
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
