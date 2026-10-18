/* The shapes of the artifact content paths a server hands out, kept apart (no imports) so they can be checked on their own. A registered server is another machine:
 * a content_url it sends is used only if it has exactly the shape this server's own route makes (team_artifacts.go: /api/board/<project>/tasks/<id>/artifact-content?source=<s>&index=<n>),
 * so it can name no other route (#4551, Security). */

const INLINE_RE = /^\/api\/board\/([^/?#\\]+)\/tasks\/([1-9][0-9]{0,15})\/artifact-content\?source=(completion|review)&index=([0-9]{1,6})$/;

/** The content path when url has that shape and names the board (an escaped project name, as encodeURIComponent makes it); otherwise null. */
export function inlineArtifactPath(url, boardName) {
    if (typeof url !== 'string') return null;
    const m = INLINE_RE.exec(url);
    if (!m) return null;
    return m[1] === encodeURIComponent(boardName) ? url : null;
}
