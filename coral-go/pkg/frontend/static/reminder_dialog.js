/* The dialog that starts a periodic reminder (#6329): the instruction to send (for an agent) and how often, with the checks said next to the field. It replaces window.prompt, which the native app
 * does not support. It is a native <dialog> opened with showModal(), so it sits above the Team view dialog it is opened from (the top layer), Escape closes it, Tab stays inside and focus goes back to
 * the button that opened it.
 * The instruction is ONE line: the server delivers everything Coral types into an agent through board.OneLine (a line break would be Enter in the terminal), so the box is a single-line field and the text is
 * normalised the same way here. Enter in either box starts the reminder. A refusal from the server is said in the dialog and the dialog stays open. */
export const REMINDER_MIN_SECONDS = 30;
export const REMINDER_MAX_SECONDS = 86400;
const MAX_MESSAGE = 1000;

const mk = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
let dialog = null;

/** The instruction as the server will deliver it (board.OneLine): control characters, line and paragraph separators become a space, runs of spaces collapse, the ends are trimmed (trim=false keeps them: a text still being typed). */
export function oneLine(text, trim = true) {
    const t = String(text == null ? '' : text).replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, ' ').replace(/ {2,}/g, ' ');
    return trim ? t.trim() : t;
}

/** The minutes a person typed, as seconds, or an error sentence: a number from half a minute to 24 hours. */
export function reminderSeconds(raw) {
    const text = String(raw == null ? '' : raw).trim();
    const minutes = Number(text);
    if (!text || !Number.isFinite(minutes)) return { error: 'Enter how many minutes, for example 5.' };
    const seconds = Math.round(minutes * 60);
    if (seconds < REMINDER_MIN_SECONDS) return { error: 'The shortest interval is half a minute (0.5).' };
    if (seconds > REMINDER_MAX_SECONDS) return { error: 'The longest interval is 24 hours (1440 minutes).' };
    return { seconds };
}

/** An interval in words: "every minute", "every 5 minutes", "every 2.5 minutes", "every 30 seconds", "every 2 hours". */
export function everyWords(seconds) {
    const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
    if (seconds < 60) return `every ${plural(seconds, 'second')}`;
    if (seconds === 60) return 'every minute';
    if (seconds >= 3600 && seconds % 3600 === 0) return seconds === 3600 ? 'every hour' : `every ${plural(seconds / 3600, 'hour')}`;
    return `every ${plural(Math.round(seconds / 6) / 10, 'minute')}`;
}

function close() { if (dialog && dialog.open) dialog.close(); }

/** Open the dialog. opts: title, intro (a sentence under it), askMessage (also ask for the instruction), minutes (the box's start), submitLabel, and onSubmit({ message, seconds }) -> Promise<{ ok, error }>. */
export function openReminderDialog(opts) {
    if (!dialog) {
        dialog = mk('dialog', 'modal-content modal-md reminder-dialog'); // the dashboard's own dialog card (components.css .modal-content): header, body, footer as its children
        dialog.id = 'reminder-dialog';
        dialog.setAttribute('aria-labelledby', 'reminder-title');
        dialog.addEventListener('cancel', (e) => { if (dialog.dataset.busy) e.preventDefault(); }); // Escape does not drop a request in flight
        document.body.appendChild(dialog);
    }
    close();
    dialog.replaceChildren();
    const head = mk('div', 'modal-header'); const h = mk('h3', null, opts.title || 'Remind'); h.id = 'reminder-title'; head.append(h);
    const form = mk('form', 'modal-body reminder-form'); form.id = 'reminder-form'; form.noValidate = true;
    // What will happen, in a sentence that follows the minutes as they are typed: opts.intro(every) with every = "every 5 minutes"
    const intro = mk('p', 'reminder-intro'); form.append(intro);

    let message = null, messageErr = null;
    if (opts.askMessage) {
        const l = mk('label', null, 'Instruction'); l.htmlFor = 'reminder-message';
        message = mk('input'); message.type = 'text'; message.id = 'reminder-message'; message.autocomplete = 'off'; message.maxLength = MAX_MESSAGE; message.required = true; message.setAttribute('aria-describedby', 'reminder-message-hint');
        const hint = mk('div', 'reminder-hint', 'One line. Line breaks and extra spaces are turned into single spaces.'); hint.id = 'reminder-message-hint';
        messageErr = mk('div', 'field-error'); messageErr.id = 'reminder-message-error'; messageErr.hidden = true;
        message.value = oneLine(opts.message || ''); // what the agent will get, so the box never shows a line break it will not
        form.append(l, message, hint, messageErr);
    }
    const ml = mk('label', null, 'Send it every (minutes)'); ml.htmlFor = 'reminder-minutes';
    const minutes = mk('input'); minutes.id = 'reminder-minutes'; minutes.type = 'number'; minutes.inputMode = 'decimal'; minutes.min = '0.5'; minutes.max = '1440'; minutes.step = 'any'; minutes.required = true;
    minutes.value = String(Number(opts.minutes) > 0 ? opts.minutes : 5); minutes.setAttribute('aria-describedby', 'reminder-minutes-hint');
    const mhint = mk('div', 'reminder-hint', 'From half a minute (0.5) to 24 hours (1440).'); mhint.id = 'reminder-minutes-hint';
    const minutesErr = mk('div', 'field-error'); minutesErr.id = 'reminder-minutes-error'; minutesErr.hidden = true;
    const serverErr = mk('div', 'field-error reminder-server-error'); serverErr.hidden = true; serverErr.setAttribute('role', 'alert');
    form.append(ml, minutes, mhint, minutesErr, serverErr);

    const acts = mk('div', 'modal-actions modal-footer');
    const cancel = mk('button', 'btn reminder-cancel', 'Cancel'); cancel.type = 'button'; cancel.addEventListener('click', close);
    const go = mk('button', 'btn btn-primary reminder-go', opts.submitLabel || 'Start reminder'); go.type = 'submit'; go.setAttribute('form', 'reminder-form'); // the footer is the card's child, outside the form, as in every dialog
    acts.append(cancel, go);
    dialog.append(head, form, acts);
    (message || minutes).autofocus = true; // showModal() puts focus on it

    // an error is said beside its field and the field is marked; a good value clears it
    const show = (input, err, text) => {
        err.hidden = !text; err.textContent = text || '';
        if (text) { input.setAttribute('aria-invalid', 'true'); input.setAttribute('aria-describedby', `${input.id}-error ${input.id === 'reminder-message' ? 'reminder-message-hint' : 'reminder-minutes-hint'}`); }
        else { input.removeAttribute('aria-invalid'); input.setAttribute('aria-describedby', input.id === 'reminder-message' ? 'reminder-message-hint' : 'reminder-minutes-hint'); }
    };
    message?.addEventListener('input', () => { if (message.value.trim()) show(message, messageErr, ''); });
    const sayIntro = () => { const m = reminderSeconds(minutes.value); intro.textContent = opts.intro(m.seconds ? everyWords(m.seconds) : 'at the interval below'); };
    minutes.addEventListener('input', () => { if (reminderSeconds(minutes.value).seconds) show(minutes, minutesErr, ''); sayIntro(); });
    sayIntro();
    message?.addEventListener('paste', () => setTimeout(() => { message.value = oneLine(message.value, false); }, 0)); // a pasted text with line breaks becomes one line at once (the end is kept: it may be more being typed)

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        serverErr.hidden = true;
        const text = message ? oneLine(message.value) : '';
        if (message) message.value = text;
        const m = reminderSeconds(minutes.value);
        if (message) show(message, messageErr, text ? '' : 'Write what the agent should be reminded to do.');
        show(minutes, minutesErr, m.error || '');
        if (message && !text) { message.focus(); return; }
        if (m.error) { minutes.focus(); return; }
        go.disabled = true; cancel.disabled = true; dialog.dataset.busy = '1';
        let res;
        try { res = await opts.onSubmit({ message: text, seconds: m.seconds }); } catch (err) { res = { ok: false, error: (err && err.message) || 'Could not start the reminder.' }; }
        go.disabled = false; cancel.disabled = false; delete dialog.dataset.busy;
        if (res && res.ok) { close(); return; }
        serverErr.textContent = (res && res.error) || 'Could not start the reminder.'; serverErr.hidden = false;
        go.focus();
    });

    dialog.showModal();
}
