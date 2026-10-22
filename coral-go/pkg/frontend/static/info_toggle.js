/* A small (i) disclosure for the text a screen does not need always on (the Settings and team dialogs keep long explanations behind it): a button after the label (aria-expanded, aria-controls,
 * a name that says what it is about) and the text, which stays in the page and is hidden until asked, so it is also what the control's aria-describedby points at. Same look as Settings' own (i) buttons
 * (.settings-info-btn, .settings-info-text). infoButton and infoBox return HTML for a template; bindInfo makes the buttons inside `root` open and close their text (once, delegated). */
import { escapeHtml, escapeAttr } from './html_escape.js';

/** The (i) button for the text box `id` (about: what it explains, e.g. "Send health check"). */
export function infoButton(id, about) {
    return `<button type="button" class="settings-info-btn" aria-expanded="false" aria-controls="${escapeAttr(id)}" aria-label="${escapeAttr(`About ${about}`)}"><span class="material-icons" aria-hidden="true">info</span></button>`;
}

/** The text an (i) opens, hidden until asked; `text` is plain text. */
export function infoBox(id, text) {
    return `<p class="settings-info-text" id="${escapeAttr(id)}" hidden>${escapeHtml(text)}</p>`;
}

/** Open and close the info text of the (i) buttons under `root`. */
export function bindInfo(root) {
    if (root.dataset.infoBound) return;
    root.dataset.infoBound = '1';
    root.addEventListener('click', e => {
        const btn = e.target.closest && e.target.closest('.settings-info-btn[aria-controls]');
        if (!btn || !root.contains(btn)) return;
        const box = root.querySelector(`#${CSS.escape(btn.getAttribute('aria-controls'))}`);
        if (!box) return;
        const open = box.hidden;
        box.hidden = !open;
        btn.setAttribute('aria-expanded', String(open));
    });
}
