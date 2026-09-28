#!/usr/bin/env python3
"""Concurrent review recovery against the stress harness's isolated live server.

Each round creates a unique board; never uses operator tasks or subscriptions.
Run multiple rounds by setting CORAL_REVIEW_STRESS_ROUNDS (default 8).
"""
import concurrent.futures
import json
import os
import sys
import urllib.error
import urllib.request
import uuid

BASE = sys.argv[1]
ROUNDS = int(os.environ.get("CORAL_REVIEW_STRESS_ROUNDS", "8"))
assert ROUNDS >= 2, "Use at least two rounds to exercise concurrent reuse"


def api(method, path, body=None):
    request = urllib.request.Request(BASE + path, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json"})
    try:
        response = urllib.request.urlopen(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


def expect(method, path, body=None, status=200):
    actual, result = api(method, path, body)
    assert actual == status, (path, actual, result)
    return result


def round_trip(index):
    project = "review-stress-" + uuid.uuid4().hex
    root = "/api/board/" + project
    tasks = root + "/tasks"
    expect("POST", root + "/subscribe", {"subscriber_id": "reviewer", "job_title": "Orchestrator"})
    create = lambda title, **extra: expect("POST", tasks,
        {"title": title, "created_by": "reviewer", "assigned_to": "worker", **extra}, 201)
    up = create("Candidate", workflow={"required_outputs": ["build"]})
    dependent = create("Success consumer", priority="critical",
        blocked_by=[{"task_id": up["id"], "required_artifacts": ["build"]}])
    independent = create("Independent work", priority="low")
    path = tasks + "/" + str(up["id"])
    expect("POST", tasks + "/claim", {"subscriber_id": "worker", "task_id": up["id"]})
    # A server validation rejection is reproducible. It is not a simulation of
    # an external safety system: external commands may never reach this API.
    expect("POST", path + "/complete", {"subscriber_id": "worker"}, 400)
    evidence = [{"name": "build", "content": "candidate " + str(index), "revision": "rev-" + str(index)}]
    candidate = {"subscriber_id": "worker", "reason": "completion requires review",
                 "message": "candidate ready", "outcome": "success", "artifacts": evidence}
    with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
        submissions = list(pool.map(lambda _: api("POST", path + "/submit-review", candidate), range(12)))
    assert sum(code == 200 for code, _ in submissions) == 1, submissions
    assert all(code in (200, 400) for code, _ in submissions), submissions
    expect("POST", tasks + "/claim", {"subscriber_id": "worker"}, 409)
    expect("POST", path + "/release-review", {"subscriber_id": "worker", "reason": "unauthorized"}, 403)
    expect("POST", path + "/release-review", {"subscriber_id": "fake-orchestrator", "reason": "unauthorized"}, 403)
    with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
        releases = list(pool.map(lambda _: api("POST", path + "/release-review",
            {"subscriber_id": "reviewer", "reason": "independent work may continue"}), range(12)))
    assert sum(code == 200 for code, _ in releases) == 1, releases
    assert all(code in (200, 400) for code, _ in releases), releases
    with concurrent.futures.ThreadPoolExecutor(max_workers=20) as pool:
        claims = list(pool.map(lambda _: api("POST", tasks + "/claim", {"subscriber_id": "worker"}), range(20)))
    claimed = [task for code, task in claims if code == 200]
    assert len(claimed) == 1 and claimed[0]["id"] == independent["id"], claims
    # A contender may observe an empty candidate list after the winner claims.
    assert all(code in (200, 404, 409) for code, _ in claims), claims
    expect("POST", path + "/complete", {"subscriber_id": "worker", "artifacts": evidence}, 400)
    review = expect("GET", path)
    assert review["status"] == "review_pending" and not review.get("completed_at"), review
    assert not review["workflow"].get("outcome") and not review["workflow"].get("artifacts"), review
    assert review["workflow"]["completion_review"]["artifacts"] == evidence, review
    child_path = tasks + "/" + str(dependent["id"])
    assert expect("GET", child_path)["status"] == "blocked"
    # Alternate accepted outcomes: only accepted success may satisfy the child.
    outcome = "success" if index % 2 == 0 else "failed"
    with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
        completions = list(pool.map(lambda _: api("POST", path + "/complete",
            {"subscriber_id": "reviewer", "outcome": outcome, "artifacts": evidence}), range(12)))
    assert sum(code == 200 for code, _ in completions) == 1, completions
    assert all(code in (200, 400) for code, _ in completions), completions
    assert expect("GET", child_path)["status"] == ("pending" if outcome == "success" else "blocked")
    final = expect("GET", path)
    assert final["workflow"]["completion_review"]["artifacts"] == evidence
    assert final["workflow"]["outcome"] == outcome
    expect("DELETE", root)
    print(f"PASS: concurrent review round {index + 1}: evidence, release, claims, accepted {outcome}", flush=True)


# Concurrent boards exercise multiple workers in addition to same-worker races.
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
    list(executor.map(round_trip, range(ROUNDS)))
print(f"PASS: {ROUNDS} concurrent review recovery rounds", flush=True)
