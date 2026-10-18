/* A model id as people say it (#4589): "claude-opus-5-5" is "Opus 5.5", "gpt-5-codex" is "GPT-5 Codex". The server may send its own display name,
 * which wins; an id nothing here knows is shown as it is, so a new model is never mislabelled. Pure, no imports. */

const WORD = { codex: 'Codex', max: 'Max', mini: 'Mini', nano: 'Nano', pro: 'Pro', flash: 'Flash', lite: 'Lite', preview: 'Preview', high: 'High', low: 'Low', medium: 'Medium', turbo: 'Turbo', thinking: 'Thinking' };
const FAMILY = { opus: 'Opus', sonnet: 'Sonnet', haiku: 'Haiku', fable: 'Fable' };

/** "-codex-max" -> " Codex Max"; nothing -> "". */
const words = s => s.split('-').filter(Boolean).map(w => WORD[w] || w).join(' ').replace(/^(?=.)/, ' ');

/** "5-5" or "5.5" or "5" -> "5.5" / "5"; null if it is not a version. */
function version(parts) {
    const nums = [];
    for (const p of parts) { if (/^\d{1,2}$/.test(p)) nums.push(p); else break; }
    return nums.length ? nums.slice(0, 2).join('.') : null;
}

export function modelDisplayName(id, serverName) {
    if (typeof serverName === 'string' && serverName.trim()) return serverName.trim();
    if (typeof id !== 'string' || !id.trim()) return '';
    const raw = id.trim();
    // A context suffix such as claude-opus-5-5[1m] stays as a note.
    const ctx = /\[([0-9]+[km])\]$/i.exec(raw);
    const base = raw.replace(/\[[^\]]*\]$/, '').replace(/-\d{8}$/, '').toLowerCase();
    const note = ctx ? ` (${ctx[1].toUpperCase()})` : '';
    let m;
    // Claude: claude-<family>-<major>-<minor> and the older claude-<major>-<minor>-<family>.
    if ((m = /^claude-(opus|sonnet|haiku|fable)-(.+)$/.exec(base))) { const v = version(m[2].split('-')); if (v) return `${FAMILY[m[1]]} ${v}${note}`; }
    if ((m = /^claude-(\d{1,2}(?:-\d{1,2})?)-(opus|sonnet|haiku|fable)$/.exec(base))) return `${FAMILY[m[2]]} ${m[1].replace('-', '.')}${note}`;
    // OpenAI: gpt-5, gpt-5.1-codex-max, gpt-5-codex.
    if ((m = /^gpt-(\d+(?:\.\d+)?)((?:-[a-z]+)*)$/.exec(base))) return `GPT-${m[1]}${words(m[2])}${note}`;
    // Gemini: gemini-2.5-pro, gemini-3-flash.
    if ((m = /^gemini-(\d+(?:\.\d+)?)((?:-[a-z]+)*)$/.exec(base))) return `Gemini ${m[1]}${words(m[2])}${note}`;
    return raw;
}
