/* Agent-specific chat semantics.
 *
 * Transcript readers normalize every CLI into the same small message shape.
 * This layer handles the remaining differences that are presentation
 * semantics rather than transport concerns: whether assistant prose is an
 * in-progress update or a final reply, and how native tool names are shown.
 */

class ChatAgentAdapter {
    assistantPlacement(_message) { return "final"; }

    toolView(tool) {
        return {
            name: tool.name || "Tool",
            icon: tool.name || "Tool",
            isQuestion: tool.name === "AskUserQuestion",
            questionLabel: "Question",
        };
    }
}

// Named adapters make agent selection explicit even while they share today's
// default behavior. Their overrides can evolve independently as each CLI's
// transcript gains richer semantics.
class ClaudeChatAdapter extends ChatAgentAdapter {}
class GeminiChatAdapter extends ChatAgentAdapter {}
class PiChatAdapter extends ChatAgentAdapter {}

class CodexChatAdapter extends ChatAgentAdapter {
    assistantPlacement(message) {
        // Codex records this distinction in the rollout. Using it avoids the
        // generic renderer's guess-and-demote behavior while a turn streams.
        return message.phase === "commentary" ? "work" : "final";
    }

    toolView(tool) {
        const sourceName = tool.name || "Tool";
        const raw = tool.operation || sourceName;
        const leaf = raw.split(/__|\./).pop();
        const views = {
            exec: ["Tools", "Bash"],
            exec_command: ["Command", "Bash"],
            shell: ["Command", "Bash"],
            apply_patch: ["Changes", "Edit"],
            view_image: ["Image", "Read"],
            imagegen: ["Image", "Write"],
            request_user_input: ["Question", "AskUserQuestion"],
            web__run: ["Web", "WebSearch"],
            web_search: ["Web", "WebSearch"],
            spawn_agent: ["Agent", "Agent"],
            send_message: ["Agent message", "Agent"],
            wait_agent: ["Agent", "Agent"],
        };
        const [name, icon] = views[raw] || views[leaf] || [leaf || raw, raw];
        return {
            name,
            icon,
            isQuestion: (raw === "request_user_input" || leaf === "request_user_input") && Array.isArray(tool.questions),
            questionLabel: "Question",
        };
    }
}

const DEFAULT_ADAPTER = new ChatAgentAdapter();
const ADAPTERS = new Map([
    ["claude", new ClaudeChatAdapter()],
    ["codex", new CodexChatAdapter()],
    ["gemini", new GeminiChatAdapter()],
    ["pi", new PiChatAdapter()],
]);

export function chatAgentAdapter(agentType) {
    return ADAPTERS.get(agentType) || DEFAULT_ADAPTER;
}
