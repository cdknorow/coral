import { safeNext, getSession, postJSON, setStatus } from './common.js';

const next = safeNext(new URLSearchParams(location.search).get('next'));
const form = document.getElementById('magic-form');
const email = document.getElementById('email');
const status = document.getElementById('status');
const github = document.getElementById('github');
const submit = document.getElementById('send');

(async function init() {
    const s = await getSession();
    if (s.authenticated) { location.replace(next); return; }
    if (!s.github) github.hidden = true;
    else github.href = '/auth/github/login';
    if (s.magic === false) form.hidden = true;
    // The divider only makes sense between two sign-in methods.
    if (s.magic === false || !s.github) document.getElementById('or').hidden = true;

    form.addEventListener('submit', async e => {
        e.preventDefault();
        submit.disabled = true;
        setStatus(status, 'Sending…');
        const r = await postJSON('/auth/magic', { email: email.value.trim() }, s.csrf);
        if (r.status === 202) {
            setStatus(status, 'Check your inbox. The link works once, for 15 minutes, in this browser.', 'ok');
        } else if (r.status === 429) {
            setStatus(status, 'Too many attempts. Wait a while and try again.', 'error');
            submit.disabled = false;
        } else if (r.status === 400) {
            setStatus(status, 'Enter a valid email address.', 'error');
            submit.disabled = false;
        } else {
            setStatus(status, 'Could not send the link. Try again.', 'error');
            submit.disabled = false;
        }
    });
})();
