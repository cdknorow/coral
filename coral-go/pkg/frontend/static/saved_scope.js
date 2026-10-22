/* Where Saved for later is opened from (#4792): the panel shows the bookmarks of the agent or the team on screen, and everything on home. Pure (no DOM, no imports): the scope is worked out
 * from what is on screen and becomes GET /api/saved's parameters (team, agent, author), which the server matches, so nothing is filtered over a capped list. saved_marks.js reads the page
 * into the env; saved.js and the top bar's count use the scope. A scope belongs to one server: its agent or team is that server's. */

export const ALL = Object.freeze({ kind: 'all', server: null, label: 'all', key: 'all' });

/** The scope of the page on screen. env: { dock: 'agent' | 'board' | 'chats' | 'other', team: {server, name} | null (the team opened in the agent page), boardPage: {server, name} | null,
 *  session: the open agent or ended chat: { type: 'live' | 'history', name, display_name, session_id, server, board: {server, name} | null (its team), job_title (its name on that board) } | null }. */
export function scopeOf(env) {
    const e = env || {};
    const team = t => t && t.name ? { kind: 'team', server: t.server, team: t.name, label: t.name, key: `team:${t.server}:${t.name}` } : null;
    if (e.dock === 'board') return team(e.boardPage) || ALL;
    if (e.dock === 'agent') {
        const t = team(e.team);
        if (t) return t;
        const s = e.session;
        if (s && s.type === 'live' && s.session_id) {
            const label = s.display_name || s.name || s.session_id;
            const on = s.board && s.board.name ? s.board.name : '';
            return { kind: 'agent', server: s.server, agent: s.session_id, team: on, author: on ? s.job_title || '' : '', label, key: `agent:${s.server}:${s.session_id}` };
        }
        return ALL;
    }
    if (e.dock === 'chats') {
        const s = e.session;
        if (s && s.type === 'history' && s.name) return { kind: 'agent', server: s.server, agent: s.name, team: '', author: '', label: s.display_name || s.name, key: `agent:${s.server}:${s.name}` };
    }
    return ALL;
}

/** The scope's parameters of GET /api/saved, as [name, value] pairs: a team's is team; an agent's is agent, and with its team and the name it writes under there, those too (its messages on the board). */
export function scopeParams(scope) {
    if (!scope || scope.kind === 'all') return [];
    if (scope.kind === 'team') return [['team', scope.team]];
    const out = [['agent', scope.agent]];
    if (scope.team && scope.author) out.push(['team', scope.team], ['author', scope.author]);
    return out;
}

/** The scope joined to a query string that has a leading "?" or is empty (saved_filters toQuery): the whole string for the request. */
export function withScope(query, scope) {
    const p = new URLSearchParams((query || '').replace(/^\?/, ''));
    for (const [k, v] of scopeParams(scope)) p.set(k, v);
    const s = p.toString();
    return s ? `?${s}` : '';
}

/** The panel's heading: "Saved: QA Engineer", "Saved: coral-go-team", "All saved". */
export function scopeHeading(scope) { return !scope || scope.kind === 'all' ? 'All saved' : `Saved: ${scope.label}`; }

/** What an empty list says in this scope. */
export function scopeEmpty(scope, tab) {
    if (tab === 'done') return 'Nothing marked done.';
    if (!scope || scope.kind === 'all') return 'Bookmark a message to save it here.';
    return `Nothing saved from ${scope.label} yet. Use the bookmark on a message to save it for later.`;
}
