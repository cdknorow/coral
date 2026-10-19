/* The markdown and HTML a board export is written as (moved out of message_board.js, #5065; no behaviour change). */
import { escapeHtml, renderMarkdown } from './utils.js';

export function parseAgentHistory(messages, agentRole) {
    const entries = [];
    for (const msg of messages) {
        const ts = (msg.timestamp || '').substring(0, 16);
        if (!ts) continue;

        if (msg.type === 'user') {
            // Human/operator message to this agent
            const content = typeof msg.content === 'string' ? msg.content : '';
            if (!content.trim()) continue;
            // Skip system notifications (board read reminders, task notifications)
            if (content.startsWith('You have ') && content.includes('unread message')) continue;
            if (content.startsWith('<task-notification>')) continue;
            entries.push({
                timestamp: ts,
                role: `Operator → ${agentRole}`,
                content: content.trim(),
                source: 'agent-chat',
            });
        } else if (msg.type === 'assistant') {
            // Agent's text response (skip tool-only messages)
            const text = (msg.text || '').trim();
            if (!text) continue;
            entries.push({
                timestamp: ts,
                role: agentRole,
                content: text,
                source: 'agent-chat',
            });
        }
        // Skip tool_result messages — they're tool output, not conversation
    }
    return entries;
}

function agentHue(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xFFFFFF;
    return h % 360;
}

export function renderExportMarkdown(d) {
    let out = `# Board Chat Export: ${d.project}\n\n`;
    out += `**Exported**: ${d.exported_at}\n`;
    out += `**Messages**: ${d.stats.total}\n`;
    if (d.stats.agent_chat > 0) {
        out += `**Board**: ${d.stats.board} | **Agent chats**: ${d.stats.agent_chat}\n`;
    }
    if (d.subscribers.length) {
        out += `**Subscribers**: ${d.subscribers.length}\n\n`;
        out += '| Agent | Role |\n|-------|------|\n';
        d.subscribers.forEach(s => { out += `| ${s.subscriber_id} | ${s.role} |\n`; });
    }
    out += '\n---\n\n## Messages\n\n';
    d.messages.forEach(e => {
        const tag = e.source === 'agent-chat' ? ' (agent chat)' : '';
        out += `**[${e.timestamp}] ${e.role}${tag}:**\n\n${e.content}\n\n---\n\n`;
    });
    return out;
}

export function renderExportHTML(d) {
    const msgs = d.messages.map(e => {
        const cls = e.source === 'agent-chat' ? 'msg side-chat' : 'msg';
        const color = `hsl(${agentHue(e.role)}, 60%, 45%)`;
        const tag = e.source === 'agent-chat' ? '<span class="msg-tag">AGENT CHAT</span>' : '';
        return `<div class="${cls}">
  <div class="msg-header">
    <span class="msg-role" style="color:${color}">${escapeHtml(e.role)}</span>
    <span class="msg-time">${escapeHtml(e.timestamp)}</span>
    ${tag}
  </div>
  <div class="msg-content">${renderMarkdown(e.content, undefined, 'board')}</div>
</div>`;
    }).join('\n');

    const subList = d.subscribers.map(s =>
        `<div class="sub-chip"><span class="role">${escapeHtml(s.role)}</span></div>`
    ).join('\n');

    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Board Chat Export: ${escapeHtml(d.project)}</title>
<style>
  :root { --bg: #1a1a1e; --surface: #212126; --border: #3a3a40; --text: #e0e0e4; --muted: #8c8c94; --accent: #58a6ff; --side-chat: #22222a; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); line-height: 1.6; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .stats { color: var(--muted); margin-bottom: 1.5rem; font-size: 0.9rem; }
  .stats span { margin-right: 1.5rem; }
  .subscribers { margin-bottom: 2rem; }
  .subscribers summary { cursor: pointer; color: var(--accent); font-weight: 600; margin-bottom: 0.5rem; }
  .sub-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; padding: 0.5rem 0; }
  .sub-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 0.4rem 0.8rem; font-size: 0.85rem; }
  .sub-chip .role { font-weight: 600; }
  .messages { display: flex; flex-direction: column; gap: 0.75rem; }
  .msg { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1rem; border-left: 3px solid var(--accent); }
  .msg.side-chat { background: var(--side-chat); border-left-color: #f0883e; }
  .msg-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; }
  .msg-role { font-weight: 700; font-size: 0.95rem; }
  .msg-time { color: var(--muted); font-size: 0.8rem; }
  .msg-tag { background: #f0883e33; color: #f0883e; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px; font-weight: 600; }
  .msg-content { word-wrap: break-word; font-size: 0.9rem; }
  .msg-content p { margin: 0.4em 0; }
  .msg-content h1, .msg-content h2, .msg-content h3, .msg-content h4 { margin: 0.8em 0 0.3em; font-size: 1.1em; }
  .msg-content h2 { font-size: 1.05em; }
  .msg-content h3 { font-size: 1em; }
  .msg-content code { background: rgba(255,255,255,0.08); padding: 0.15em 0.4em; border-radius: 4px; font-size: 0.9em; }
  .msg-content pre { background: rgba(0,0,0,0.3); padding: 0.8em; border-radius: 6px; overflow-x: auto; margin: 0.5em 0; }
  .msg-content pre code { background: none; padding: 0; }
  .msg-content ul, .msg-content ol { padding-left: 1.5em; margin: 0.3em 0; }
  .msg-content li { margin: 0.2em 0; }
  .msg-content strong { font-weight: 700; }
  .msg-content blockquote { border-left: 3px solid var(--border); padding-left: 0.8em; color: var(--muted); margin: 0.5em 0; }
  .msg-content table { border-collapse: collapse; margin: 0.5em 0; width: 100%; }
  .msg-content th, .msg-content td { border: 1px solid var(--border); padding: 0.4em 0.6em; text-align: left; font-size: 0.85em; }
  .msg-content th { background: rgba(0,0,0,0.2); font-weight: 600; }
</style>
</head>
<body>
<div class="container">
<h1>${escapeHtml(d.project)}</h1>
<div class="stats">
  <span>Exported: ${escapeHtml(d.exported_at)}</span>
  <span>Messages: ${d.stats.total}</span>
  ${d.stats.agent_chat > 0 ? `<span>Board: ${d.stats.board}</span><span>Agent chats: ${d.stats.agent_chat}</span>` : ''}
</div>
${d.subscribers.length ? `<details class="subscribers"><summary>${d.subscribers.length} Subscribers</summary><div class="sub-grid">${subList}</div></details>` : ''}
<div class="messages">
${msgs}
</div>
</div>
</body>
</html>`;
}
