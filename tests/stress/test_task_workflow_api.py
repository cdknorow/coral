#!/usr/bin/env python3
"""Real-server API regression checks. Set CORAL_BIN to a freshly built dev binary.

Uses a temporary data directory and free localhost port; never the user's server.
Retains logs and the test database for inspection. No model or agent launches.
The stress harness optionally supplies CORAL_TEST_ARTIFACT_DIR and
CORAL_TEST_RESULT_FILE to collect artifacts and a machine-readable result.
"""
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    binary = Path(os.environ["CORAL_BIN"]).resolve()
    data = Path(tempfile.mkdtemp(prefix="coral-workflow-api-",
                               dir=os.environ.get("CORAL_TEST_ARTIFACT_DIR")))
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    server = None
    log = (data / "server.log").open("ab")
    checks = 0
    success = False

    def check(condition, label):
        nonlocal checks
        assert condition, label
        checks += 1
        print("PASS:", label, flush=True)

    def api(method, path, body=None, expected=200):
        req = urllib.request.Request(base + path, method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Content-Type": "application/json"})
        try:
            response = urllib.request.urlopen(req, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            content = response.read().decode()
            assert response.status == expected, (method, path, response.status, content)
            return json.loads(content)

    def start():
        nonlocal server
        env = {k: v for k, v in os.environ.items() if not k.startswith("CORAL_")}
        server = subprocess.Popen([str(binary), "--home", str(data), "--host", "127.0.0.1",
            "--port", str(port), "--no-browser", "--backend", "tmux"], env=env,
            stdout=log, stderr=subprocess.STDOUT)
        for _ in range(100):
            assert server.poll() is None, "Server exited; inspect " + str(data / "server.log")
            try:
                api("GET", "/api/health")
                return
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        raise AssertionError("Server did not start")

    def stop():
        if server is not None and server.poll() is None:
            server.kill()  # Abrupt restart, no graceful shutdown callbacks.
            server.wait(timeout=10)

    tasks = "/api/board/api-regression/tasks"

    def create(title, **kwargs):
        return api("POST", tasks, {"title": title, "created_by": "api-test", **kwargs}, 201)

    def detail(task):
        return api("GET", f"{tasks}/{task['id']}")

    try:
        start()
        names = [f"output-{i}" for i in range(33)]
        error = api("POST", tasks, {"title": "Impossible outputs", "created_by": "api-test",
            "workflow": {"required_outputs": names}}, 400)
        check("at most 32" in json.dumps(error), "33 required outputs rejected with HTTP 400")
        check(not api("GET", tasks)["tasks"], "invalid creation leaves no orphan task")
        up = create("32-output boundary", workflow={"required_outputs": names[:32]})
        dep = {"task_id": up["id"], "required_artifacts": names[:32]}
        child = create("Consumer", blocked_by=[dep])
        bad_dep = {**dep, "required_artifacts": names}
        api("POST", tasks, {"title": "Impossible dependency", "created_by": "api-test",
            "blocked_by": [bad_dep]}, 400)
        api("PATCH", f"{tasks}/{child['id']}", {"blocked_by": [bad_dep]}, 400)
        check(len(detail(child)["blocked_by"][0]["required_artifacts"]) == 32,
              "33-name dependency rejected on create/update; existing rule unchanged")
        check(len(api("GET", tasks)["tasks"]) == 2, "invalid dependency creation leaves no orphan")
        evidence = [{"name": name, "content": "candidate", "revision": "api-rev-42"} for name in names[:32]]
        api("POST", f"{tasks}/{up['id']}/complete", {"subscriber_id": "builder", "artifacts": evidence})
        check(detail(child)["status"] == "pending", "32-output completion immediately unlocks consumer")
        stop()
        start()
        claimed = api("POST", tasks + "/claim", {"subscriber_id": "tester", "task_id": child["id"]})
        check(claimed["workflow"]["inputs"][0]["artifacts"] == evidence,
              "consumer claim after abrupt restart preserves all 32 upstream artifacts")
        api("POST", f"{tasks}/{child['id']}/complete", {"subscriber_id": "tester"})

        for outcome in ("failed", "cancelled"):
            root = create(outcome)
            children = {condition: create(outcome + " " + condition, blocked_by=[
                {"task_id": root["id"], "condition": condition}])
                for condition in ("success", "failure", "termination")}
            action = "cancel" if outcome == "cancelled" else "complete"
            api("POST", f"{tasks}/{root['id']}/{action}",
                {"subscriber_id": "builder", "outcome": "failed"})
            for condition, task in children.items():
                expected = "pending" if condition == "termination" or (condition == "failure" and outcome == "failed") else "blocked"
                check(detail(task)["status"] == expected, f"{outcome}: {condition} dependency is {expected}")

        legacy = create("Legacy completed build")
        legacy_child = create("Recover legacy consumer", blocked_by=[legacy["id"]])
        unmet = create("Still needs missing artifact", blocked_by=[
            {"task_id": legacy["id"], "required_artifacts": ["missing"]}])
        draft = create("Keep draft", draft=True, blocked_by=[legacy["id"]])
        stop()
        # Seed only the old crash state while this isolated server is stopped.
        # All verification and normal lifecycle actions go through HTTP.
        with sqlite3.connect(data / "messageboard.db") as db:
            db.execute("UPDATE board_tasks SET status='completed' WHERE id=?", (legacy["id"],))
        start()
        check(detail(legacy_child)["status"] == "pending", "startup repairs legacy missed readiness callback")
        check(detail(unmet)["status"] == "blocked", "startup preserves unsatisfied artifact dependency")
        check(detail(draft)["status"] == "draft", "startup does not publish drafts")
        api("POST", tasks + "/claim", {"subscriber_id": "recovery-worker", "task_id": legacy_child["id"]})
        check(detail(legacy_child)["status"] == "in_progress", "recovered task is claimable through API")
        print(f"Results: {checks} checks passed", flush=True)
        success = True
    finally:
        stop()
        log.close()
        result_file = os.environ.get("CORAL_TEST_RESULT_FILE")
        if result_file:
            Path(result_file).write_text(json.dumps({
                "success": success, "passed": checks, "data_dir": str(data)
            }) + "\n")
        print("Retained test data:", data, flush=True)


if __name__ == "__main__":
    main()
