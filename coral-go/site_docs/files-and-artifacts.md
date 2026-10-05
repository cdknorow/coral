# Files and artifacts

Select an agent and open its Files viewer. The viewer has four source tabs:

| Tab | What it shows |
| --- | --- |
| **Files** | The existing changed-files view for the selected agent's repository. |
| **Browse** | A directory tree rooted at the selected agent's repository root, or its working directory when there is no repository. |
| **Artifacts** | Task-result artifacts attributed to the selected agent on its team. |
| **Team Artifacts** | Task-result artifacts shared across the selected agent's team, including items without an individual agent attribution. |

These lists serve different purposes. Uploading a file does not change the repository, and an upload by itself does not add a task-result entry to the artifact lists.

## Browse files

In **Browse**, expand a directory to load its contents. Directories appear before files. Large directories offer **Load more**; Coral does not scan the entire tree when you open the tab. Use **Refresh** to reload the root and **Collapse all** to close expanded directories.

The path shown above the tree is its root. An agent working in a repository subdirectory can still see the repository-root tree. Select a file to preview it, then use **Back** to return to the selected source and expanded directories. Switching agents resets the browsing context.

Arrow keys navigate the tree; right and left expand or collapse directories. Home and End move to the first and last visible entries.

## Preview or download an artifact

Open **Artifacts** or **Team Artifacts**, then select the artifact name or **Preview**. The lists load on demand and have refresh and pagination controls. An empty personal list can mean that no task-result artifacts are attributed to that agent; check **Team Artifacts** for the broader team collection.

Coral can preview supported images, audio/video, text, Markdown, HTML, and JSON. Recognized JSON reports offer a readable view and the original raw JSON. Declare `text/markdown` when publishing a Markdown artifact to select its formatted preview. In v1.3.23, an older upload with a generic binary media type and an extensionless display label may appear only as a download, even if the original file was Markdown. Use **Download** to read that file.

Use **Download** to save a managed or inline artifact. Unsupported binary files use a download fallback. Text previews are limited to 2 MiB; larger files remain available to download. HTML previews run in a restricted frame. External references offer **Open link**; an embedded preview may be blocked by the destination website.

The preview's title is the artifact label, which can differ from the uploaded filename. A **Missing** or **Unavailable** item cannot currently be opened from that entry.

## Publish a file into Coral

An agent can upload a report from its Coral session:

```sh
coral-agent artifact upload report.md --media-type text/markdown
```

The command returns a `coral://artifacts/<digest>` URI and a browser URL. The file is stored by Coral, with a 64 MiB upload limit. Uploading an artifact does not publish it to an external hosting service.

To include it in the artifact lists, attach it to a task result. Create a manifest such as `artifacts.json`, replacing the URI below with the actual upload result:

```json
[
  {
    "name": "review_report",
    "media_type": "text/markdown",
    "uri": "coral://artifacts/<digest>"
  }
]
```

Then complete the appropriate task, using its actual task number:

```sh
coral-board task complete 123 --message "Review completed" --artifacts artifacts.json
```

Use the output names required by the task's workflow. Small reports can use an inline `content` field instead of a URI. A filesystem path such as `/tmp/report.md` is not a shared artifact: upload the file so another agent or the browser can reach it.

Without a task, the returned URI/browser link can still open the uploaded file. It will not appear in **Artifacts** or **Team Artifacts** until attached to a task result. External URLs can be included as references, but Coral does not host their contents.

For the surrounding workflow, see [Teams and tasks](teams-and-tasks.md). For interactive panels that live inside the agent workspace, see [Agent UI](agent-ui.md). Setup and access questions are covered in [Getting started](getting-started.md), [Privacy](privacy.md), and [Troubleshooting](troubleshooting.md).
