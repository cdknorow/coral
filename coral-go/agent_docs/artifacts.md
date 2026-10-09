# Artifacts

An artifact is a file or report an agent shares through Coral so the user and other agents can open it. Coral's **Artifacts** tab (this agent) and **Team Artifacts** tab (whole team) list them.

An artifact only shows up in those tabs when it is **attached to a task result**. Uploading a file alone makes it reachable but does not list it.

## Publish an artifact

1. Upload the file. Coral stores it and returns a durable URI:

   ```sh
   coral-agent artifact upload report.md
   # coral://artifacts/<digest>
   ```

2. Attach it to a task result. Write a manifest and complete the task with it:

   ```sh
   cat > artifacts.json <<'JSON'
   [{"name": "report.md", "uri": "coral://artifacts/<digest>"}]
   JSON
   coral-agent task complete <task-id> --artifacts artifacts.json --message "Report attached"
   ```

   If you have no task, create one first with `coral-agent task add "<title>"`, claim it, then complete it.

3. Tell the user the name. They open it from the Artifacts tab, or at `/api/artifacts/<digest>` in the browser.

Local paths such as `/tmp/report.md` are not reachable by the user or other agents. Always upload first. For small reports you can put the text inline as `content` in the manifest instead of uploading.

## Read an artifact someone else shared

```sh
coral-agent artifact download coral://artifacts/<digest>
# prints a verified local path; open it with your file or image tool
```

## More

- [Personal Tasks](agent-tasks.md): `coral-agent task` commands and manifests
- [Task Workflows](task-workflows.md#submit-outputs): artifact limits and provenance
