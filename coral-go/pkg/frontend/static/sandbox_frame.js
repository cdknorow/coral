/* The iframe an untrusted HTML artifact is shown in. Everything the artifact contains is inert: no scripts, no
 * same-origin access, no forms, popups, downloads or top-level navigation (an empty `sandbox`), plus a content
 * security policy for the embedded document (`csp` attribute where supported, and a <meta> in the document itself). */

export const FRAME_CSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:"; // #3593: data: fonts too
const DOC_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'";

/** Make `frame` a sandbox with no capabilities. */
export function lockFrame(frame) {
    frame.setAttribute('sandbox', '');
    frame.setAttribute('csp', FRAME_CSP);
    frame.referrerPolicy = 'no-referrer';
    return frame;
}

/** The srcdoc for artifact HTML: a CSP <meta> first, then the content. */
export function sandboxedDoc(content) {
    return `<!doctype html><meta http-equiv="Content-Security-Policy" content="${DOC_CSP}">${content}`;
}
