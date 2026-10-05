# Agent UI

Agent UI lets an agent display diagrams, dashboards, and interactive panels in its Coral workspace. It is useful when a visual explanation or a small interface is easier to use than a long chat reply. For downloadable reports and files, use [Files and artifacts](files-and-artifacts.md).

## Request a panel

1. Select the live agent you want to handle the request.
2. Open **Agent UI**. Describe the panel you want in the request box.
3. Choose **Send request**, or press Ctrl+Enter / Command+Enter.

For example:

> Show a diagram of the main services in this project, with a short explanation of each dependency.

Coral sends your description together with panel-generation instructions to the selected agent. The request appears in chat as a Coral notice. The form is also available when the agent already has published panels.

Drafts are kept separately for each agent in the current browser tab and survive the panel's refreshes and page reloads. Opening Agent UI or switching agents does not send a draft. Requests are limited to 8,000 bytes, so keep the description focused.

**Request sent** means Coral delivered the request to the agent's terminal. It does not mean the panel has been built or that the agent has started handling it. Published panels appear in the selected agent's Agent UI home list.

If the form offers **Retry request**, you can retry the same request. If it says **Delivery uncertain**, check the agent's chat or terminal before sending again: the original input may already have reached the agent. A warning that delivery could not be recorded can accompany a successful send; do not resend that successful request just to clear the warning.

## Open and interact with panels

Select a published panel from the home list. You can expand it or open it in a new tab. Expanding currently reloads the panel, so save important responses before doing so.

**Close panel** returns to the list without deleting the panel. Select it again to reopen it. **Delete panel** asks for confirmation and removes the panel and its saved responses. Switching agents returns you to that agent's home list.

A panel may include choices or a form that sends structured responses to its owning agent. Coral reports whether a response was saved and whether the agent was notified. A notification failure does not discard a saved response; the agent can read it later. Sleeping agents are not automatically awakened by panel responses.

## What panels can do

Panels are self-contained content shown in a restricted frame. They do not have unrestricted access to Coral or the network. Ask for inline styles, scripts, and embedded images rather than a panel that depends on remote libraries or external API calls.

Publishing supports self-contained HTML, inline SVG, PNG, JPEG, GIF, and WebP. A panel is limited to 2 MiB after image embedding. A panel interaction does not itself complete a [task](teams-and-tasks.md) or grant the agent permission for other work.

For agents creating panels directly, the basic publication command is:

```sh
coral-agent ui publish --id architecture --title "Architecture" --file diagram.html
```

Publishing again with the same ID creates a new revision. Agents can inspect published panels and saved responses:

```sh
coral-agent ui list
coral-agent ui events --id architecture --after 0
```

These commands belong in the originating agent's Coral session so the panel appears in the intended workspace. The normal request form supplies the agent with publication instructions; users do not need to run these commands to request a panel.

For CLI setup and connection problems, see [Getting started](getting-started.md) and [Troubleshooting](troubleshooting.md). See [Privacy](privacy.md) for how Coral handles data.
