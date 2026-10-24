/* The knowledge page's markdown when marked.js is not loaded (moved out of team_artifacts.js, #7045; CodeQL js/xss): the text is escaped FIRST, then the few rules (headings, bold, code,
 * lists, links, line breaks) add their own tags, so nothing in the text is read as markup. A link is drawn only for an http(s) address; anything else stays text. The ./name.md links
 * to other notes become the page's own knowledge links (the label is already escaped; the target is a name the caller holds, escaped again as an attribute). */
import { escapeHtml, escapeAttr } from './html_escape.js';

const LINK_RE = /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g;

export function knowledgeFallbackHtml(text, slugToName = {}) {
    return escapeHtml(text)
        .replace(/\[([^\]]+)\]\(\.\/([^)]+)\.md\)/g, (_, label, slug) => (Object.hasOwn(slugToName, slug)
            ? `<a href="#" class="knowledge-link" data-knowledge-link="${escapeAttr(slugToName[slug])}">${label}</a>` : `<span class="knowledge-link-dead">${label}</span>`))
        .replace(/^### (.+)$/gm, '<h4 class="knowledge-h4">$1</h4>')
        .replace(/^## (.+)$/gm, '<h3 class="knowledge-h3">$1</h3>')
        .replace(/^# (.+)$/gm, '<h2 class="knowledge-h2">$1</h2>')
        .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
        .replace(/`([^`]+)`/g, '<code class="knowledge-code">$1</code>')
        .replace(LINK_RE, '<a href="$2" class="knowledge-link-ext" target="_blank" rel="noopener">$1</a>')
        .replace(/^- (.+)$/gm, '<div class="knowledge-li">&#8226; $1</div>')
        .replace(/\n\n/g, '<div class="knowledge-spacer"></div>')
        .replace(/\n/g, '<br>');
}
