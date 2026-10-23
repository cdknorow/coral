/* The confirmation that comes before a relaunch (#5275, design #5270): it names what restarts. It never relaunches on the first click; the safe answer ("Not now") has the focus when agents would restart;
 * the primary button says what it will do. The list is the server's (the pty agents a relaunch restarts, and an ack of exactly that list): a list that changed since it was read is shown again and has to be
 * answered again (409 stale_ack). A dialog on a desktop, a sheet at the bottom on a phone (css). Escape and the focus trap are modal_chrome's; focus goes back to the control that opened it (dialog_focus.js). */
import { attachDialogFocus } from './dialog_focus.js';
import { browserSession } from './safe_storage.js';
import { importOnce } from './import_once.js';

const { RESUME_KEY, RELEASES_REPO, RELEASES_URL, reasonText, requestRelaunch } = await importOnce('./update_api.js');

// What happens after Relaunch, by how this Coral was installed (the docs' per-platform table): none of it promises that this page comes back by itself
const AFTER = {
    app: 'Coral is quitting to install the update. It may open the disk image to finish. If Coral does not start again by itself, open it.',
    msi: 'Windows will ask for your permission to install the update (the usual admin prompt). Coral starts again when the installer is done.',
    portable: 'Coral is quitting to swap in the update. If it does not start again by itself, start it.',
    tarball: 'Coral is quitting to install the update. If it was not started by the tray or a service, it stays stopped: start it again.',
};
const AFTER_DEFAULT = 'Coral is quitting to install the update. If it does not start again by itself, start it.';
const STATE_WORDS = { working: 'working', idle: 'idle', waiting: 'waiting for you' };

const mk = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
/** Which of the restarted agents come back on their own (the server's `resumes`), in words: " 2 start again on their own afterwards; 1 starts a new conversation." Empty when the server does not say. */
function comeBack(back, fresh, n) {
    const parts = [];
    if (back) parts.push(back === n ? `All ${n} start again on their own afterwards` : `${back} start${back === 1 ? 's' : ''} again on their own afterwards`);
    if (fresh) parts.push(fresh === n ? `All ${n} start a new conversation` : `${fresh} start${fresh === 1 ? 's' : ''} a new conversation`);
    return parts.length ? ` ${parts.join('; ')}.` : '';
}
let dialog = null;
let current = null; // the status the dialog shows

function ensure() {
    if (dialog) return dialog;
    dialog = mk('div', 'modal update-confirm-modal');
    dialog.id = 'update-confirm-modal';
    dialog.setAttribute('role', 'alertdialog'); dialog.setAttribute('aria-modal', 'true');
    dialog.setAttribute('aria-labelledby', 'update-confirm-title'); dialog.setAttribute('aria-describedby', 'update-confirm-body');
    dialog.style.display = 'none'; // display-ok: modal_chrome (Escape, the Tab trap) and dialog_focus read a .modal's display, as for every dialog
    document.body.appendChild(dialog);
    return dialog;
}

function draw(note) {
    const s = current, r = s.relaunch, n = r.restarts.length;
    dialog.replaceChildren();
    const content = mk('div', 'modal-content modal-md update-confirm');
    const head = mk('div', 'modal-header'); const h = mk('h3', null, `Install Coral ${s.staged.version} and relaunch?`); h.id = 'update-confirm-title'; head.append(h);
    const body = mk('div', 'modal-body'); body.id = 'update-confirm-body';
    const days = s.staged.days;
    // Where it comes from: Coral's releases on the one pinned repository. The status says the repository only after verification; the words and the link are this page's (RELEASES_URL), never built from the status's text.
    const from = mk('p'); from.append(document.createTextNode(`Coral ${s.staged.version} comes from Coral's releases on GitHub`));
    // the address and its link only for the one repository this page knows (RELEASES_URL); any other name the status sends is not shown at all
    if (s.source === RELEASES_REPO) {
        from.append(document.createTextNode(' ('));
        const a = mk('a', 'update-source-link', RELEASES_URL.replace('https://', '')); a.href = RELEASES_URL; a.target = '_blank'; a.rel = 'noopener noreferrer'; from.append(a);
        from.append(document.createTextNode(')'));
    }
    from.append(document.createTextNode(`. It was downloaded and its signature was checked${days >= 1 ? ` (ready for ${plural(days, 'day')})` : ''}. You are on ${s.installed}.`));
    body.append(from);
    if (note) { const p = mk('p', 'update-note', note); p.setAttribute('role', 'status'); body.append(p); }
    if (n > 0) {
        const warn = mk('div', 'update-warn');
        const icon = mk('span', 'material-icons', 'warning'); icon.setAttribute('aria-hidden', 'true');
        const inner = mk('div', 'update-warn-body');
        const back = r.restarts.filter(a => a.resumes === true).length, fresh = r.restarts.filter(a => a.resumes === false).length;
        const lead = mk('p'); lead.append(mk('b', null, `Relaunching restarts ${plural(n, 'agent')}`), document.createTextNode(` that run in Coral's own terminal (pty). Their work in progress stops.${comeBack(back, fresh, n)}`));
        const list = mk('ul', 'update-agents'); list.tabIndex = 0; list.setAttribute('aria-label', 'Agents that restart');
        for (const a of r.restarts) {
            const li = mk('li'); li.append(mk('span', 'update-agent-name', a.name || a.sessionId), ' ', mk('span', 'update-agent-state', STATE_WORDS[a.state] || a.state)); // spaces between the parts: the row's text reads as words wherever it is read from (#6315)
            if (a.resumes === true) li.append(' ', mk('span', 'update-agent-note', 'starts again afterwards'));
            else if (a.resumes === false) li.append(' ', mk('span', 'update-agent-note', 'starts a new conversation'));
            list.append(li);
        }
        inner.append(lead, list, mk('p', 'update-keep', r.tmuxRestart ? 'Agents in tmux restart too: the bundled tmux changed.' : 'Agents in tmux keep running and reattach.'));
        warn.append(icon, inner); body.append(warn);
    }
    body.append(mk('p', 'update-later-note', 'Nothing is installed until you choose Install. Later changes nothing: the update stays ready and the button stays in the top bar.'));
    const acts = mk('div', 'modal-actions modal-footer');
    const later = mk('button', 'btn update-later', 'Later'); later.type = 'button';
    later.addEventListener('click', close);
    const go = mk('button', 'btn btn-primary update-go', n > 0 ? `Install and restart ${plural(n, 'agent')}` : 'Install and relaunch'); go.type = 'button';
    go.addEventListener('click', () => sendRelaunch(go, later));
    // the safe answer starts with focus when something would restart; with nothing to lose the action itself does
    (n > 0 ? later : go).setAttribute('data-initial-focus', '');
    acts.append(later, go);
    content.append(head, body, acts); dialog.append(content);
}

async function sendRelaunch(go, later) {
    const ack = current.relaunch.ack;
    go.disabled = true; later.disabled = true;
    const res = await requestRelaunch(ack);
    if (res.ok) {
        const body = dialog.querySelector('.modal-body');
        const p = mk('p', 'update-note', AFTER[current.installKind] || AFTER_DEFAULT); p.setAttribute('role', 'status');
        body.querySelector('.update-later-note')?.remove(); // sent: "Not now?" no longer applies
        body.append(p); later.textContent = 'Close'; later.disabled = false; go.hidden = true;
        return;
    }
    if (res.status === 409 && res.code === 'stale_ack' && res.relaunch) {
        current = { ...current, relaunch: res.relaunch };
        draw('The list of agents changed while this was open. Check it again before you relaunch.');
        dialog.querySelector('[data-initial-focus]')?.focus();
        return;
    }
    go.disabled = false; later.disabled = false;
    // 404: Coral Hub has relaunching switched off for this machine: nothing to offer, so nothing is shown
    if (res.status === 404) { close(); (await importOnce('./update_ui.js')).refreshUpdateStatus(); return; }
    const body = dialog.querySelector('.modal-body');
    const say = (text) => { const p = mk('p', 'update-note update-error', text); p.setAttribute('role', 'alert'); body.append(p); return p; };
    if (res.status === 401 && res.code === 'reauth_required') {
        // The hub wants a recent sign-in. Never retry the request by itself: the person signs in, comes back to this dialog, and the status is read again for a fresh ack.
        say(reasonText('reauth_required'));
        const a = mk('a', 'btn btn-primary update-signin', 'Sign in again'); a.href = res.signInUrl || (location.pathname + location.search);
        a.addEventListener('click', () => browserSession.set(RESUME_KEY, '1')); // (no storage: the person returns to the dashboard and opens the dialog again)
        dialog.querySelector('.modal-actions').insertBefore(a, go); go.hidden = true; a.focus();
        return;
    }
    say(reasonText(res.code)); // 'Unavailable' for a reason this build does not know; never the server's own words
}

function close() { if (dialog) dialog.style.display = 'none'; } // display-ok: modal_chrome (Escape, the Tab trap) and dialog_focus read a .modal's display, as for every dialog

/** Show the confirmation for the status s (a relaunch is offered in it). */
export function openUpdateDialog(s) {
    ensure(); current = s;
    attachDialogFocus(dialog); // records the opener (the control that has focus now) before the dialog is shown
    draw('');
    dialog.style.display = 'flex'; // display-ok: modal_chrome (Escape, the Tab trap) and dialog_focus read a .modal's display, as for every dialog
}
