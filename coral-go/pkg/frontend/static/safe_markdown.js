/* The ES wrapper of the shared markdown renderer (#5754). vendor/safe-markdown.js is Coral Hub's renderer, byte for byte (its hash is pinned in tests/frontend/vendor_manifest.mjs and the hub pins the
 * same one); it is a classic script that defines window.AccountMarkdown, loaded on first use by vendor_loader.js (loadSafeMarkdown; script-src 'self'), not by the page (#5818). This file is the only place that reads that global.
 *
 * renderSafe(text, opts) -> { node, partial }: the DOM-built, sanitised-by-construction rendering of machine-supplied text (no HTML string, no image, links http/https/mailto with the host shown).
 * If the renderer did not load, the text is shown as plain text: never as markup.
 * opts.linkPolicy is the host's link callback; makeLinkPolicy builds Coral's. */

/** @param {string} text @param {{ linkPolicy?: (url: string) => unknown }} [opts] @returns {{ node: HTMLElement, partial: boolean }} */
export function renderSafe(text, opts) {
    const m = globalThis.AccountMarkdown;
    if (m && typeof m.render === 'function') {
        try { return m.render(String(text == null ? '' : text), opts); } catch { /* falls through to the plain text below */ }
    }
    const node = document.createElement('div');
    node.className = 'md md-plain';
    node.textContent = String(text == null ? '' : text);
    return { node, partial: true };
}

/** Coral's link policy: it owns only fixed same-origin shapes, and only after a lookup that the id exists on this server; it never builds a path from the text. Anything else is undefined, which leaves
 *  the url to the renderer's default (http, https and mailto, with the host shown) or its refusal.
 *  An artifact: coral://artifacts/<64 hex>, owned only when exists(hex) answers true (the host's lookup), as /api/artifacts/<hex>.
 *  @param {{ artifactExists?: (hex: string) => boolean }} [lookups] */
export function makeLinkPolicy(lookups = {}) {
    return function linkPolicy(url) {
        if (typeof url !== 'string') return undefined;
        const m = /^coral:\/\/artifacts\/([0-9a-f]{64})$/.exec(url);
        if (!m) return undefined;
        const exists = lookups.artifactExists;
        if (typeof exists !== 'function' || exists(m[1]) !== true) return undefined;
        return { href: `/api/artifacts/${m[1]}`, shown: `artifact ${m[1].slice(0, 8)}`, internal: true };
    };
}
