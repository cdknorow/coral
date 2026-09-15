/* Shared CodeMirror 6 helpers — used by the file preview pane and the inline
 * diffs in the Files tab, so both render code the same way. */

/** Get the CodeMirror module (loaded via IIFE script tag as window.CoralCM). */
export function getCm() {
    if (!window.CoralCM) {
        console.error('[coral] CodeMirror not available — codemirror-bundle.js may not have loaded');
        return null;
    }
    return window.CoralCM;
}

/** Resolve a language extension by name, or null when unsupported. */
export function getLangExtension(cm, langName) {
    const loaders = {
        javascript: () => cm.javascript(),
        typescript: () => cm.javascript({ typescript: true }),
        jsx:        () => cm.javascript({ jsx: true }),
        tsx:        () => cm.javascript({ jsx: true, typescript: true }),
        python: () => cm.python(), html: () => cm.html(), css: () => cm.css(),
        json: () => cm.json(), markdown: () => cm.markdown(), sql: () => cm.sql(),
        rust: () => cm.rust(), cpp: () => cm.cpp(), c: () => cm.cpp(),
        java: () => cm.java(), go: () => cm.go(), xml: () => cm.xml(), yaml: () => cm.yaml(),
    };
    const loader = loaders[langName];
    if (!loader) return null;
    try { return loader(); } catch { return null; }
}

/** Map a file path to a language name for highlighting. */
export function getLangFromPath(fp) {
    const ext = (fp.match(/\.(\w+)$/) || [])[1] || '';
    const map = {
        js: 'javascript', ts: 'typescript', tsx: 'typescript', jsx: 'javascript',
        py: 'python', rb: 'ruby', rs: 'rust', go: 'go', java: 'java',
        sh: 'bash', zsh: 'bash', bash: 'bash', yml: 'yaml', yaml: 'yaml',
        json: 'json', toml: 'toml', css: 'css', scss: 'scss',
        html: 'html', xml: 'xml', sql: 'sql', c: 'c', cpp: 'cpp',
        h: 'c', hpp: 'cpp', cs: 'csharp', swift: 'swift', kt: 'kotlin',
        md: 'markdown',
    };
    return map[ext.toLowerCase()] || ext.toLowerCase() || 'plaintext';
}
