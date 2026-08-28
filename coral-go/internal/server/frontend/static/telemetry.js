/* Telemetry disclosure: shown once, on the first run of a build that can send
 * anything. The event list is rendered from /api/system/telemetry rather than
 * written into this file, so the disclosure can never describe a different set
 * of events than the one Coral actually sends. */

import { escapeHtml as esc } from './utils.js';

const MODAL_ID = 'telemetry-modal';

/**
 * Show the disclosure if this build can send data and the user has not yet
 * acknowledged it. Silent no-op otherwise — including for builds compiled from
 * source, which carry no analytics key and send nothing, so there is nothing
 * to disclose.
 */
export async function maybeShowTelemetryDisclosure() {
    try {
        const resp = await fetch('/api/system/telemetry');
        if (!resp.ok) return;
        const data = await resp.json();
        if (!data.enabled || data.acknowledged) return;
        renderTelemetryDisclosure(data);
        const modal = document.getElementById(MODAL_ID);
        if (modal) modal.style.display = '';
    } catch { /* never block the dashboard on this */ }
}

function renderTelemetryDisclosure(data) {
    const body = document.getElementById('telemetry-modal-body');
    if (!body) return;

    const events = (data.events || []).map(e => `
        <tr>
            <td><code>${esc(e.name)}</code></td>
            <td>${esc(e.when)}${e.extra ? `<br><span class="text-muted-sm">Also sends: ${esc(e.extra)}</span>` : ''}</td>
        </tr>`).join('');

    const props = (data.properties || []).map(p => `<li>${esc(p)}</li>`).join('');
    const never = (data.never_collected || []).map(n => `<li>${esc(n)}</li>`).join('');

    body.innerHTML = `
        <p>Coral runs entirely on your machine, so we cannot see where people get stuck
        unless the app tells us. This build reports the events below and nothing else.
        Read it once and it will not appear again.</p>

        <h4>Every event Coral sends</h4>
        <div class="telemetry-event-table-wrap">
            <table class="telemetry-event-table">
                <thead><tr><th>Event</th><th>When it fires</th></tr></thead>
                <tbody>${events}</tbody>
            </table>
        </div>

        <h4>What every event carries</h4>
        <ul>${props}</ul>
        <p>All of it is attached to a random ID stored at
        <code>${esc(data.install_id_path || '')}</code>. That ID is generated randomly — it is
        not derived from your name, email, hostname, or hardware. Delete the file and you
        become a new, unlinked install.</p>

        <h4>What Coral never sends</h4>
        <ul>${never}</ul>

        <h4>There is no opt-out switch</h4>
        <p>Rather than soften that, here is what is true instead, and all of it is
        verifiable:</p>
        <ul>
            <li>Coral is Apache 2.0. The entire implementation is <code>internal/tracking/</code>
                — you can read every event before you trust the list above.</li>
            <li><strong>Builds you compile yourself carry no analytics key and send nothing at
                all.</strong> The key is injected at build time. A source build has none, and it
                does not quietly consume its first-run events either.</li>
            <li>Failed deliveries are written to <code>${esc(data.failure_log || '')}</code>, so
                you can see what Coral tried to send and could not.</li>
        </ul>

        <h4>Separately, about your AI agents</h4>
        <p>Coral has no API keys of its own and never calls a model on your behalf. It runs the
        CLI agents you have already installed, using your credentials. To count tokens and
        costs, Coral can proxy those agents' API traffic locally — the traffic goes to the same
        provider it always did, and nothing is sent to us.</p>`;
}

/** Record the acknowledgement so the disclosure does not appear again. */
export async function acknowledgeTelemetryDisclosure() {
    const modal = document.getElementById(MODAL_ID);
    if (modal) modal.style.display = 'none';
    try {
        await fetch('/api/system/telemetry/acknowledge', { method: 'POST' });
    } catch { /* it will simply be shown again next launch */ }
}

/** Open the full telemetry reference in the Docs tab. */
export function openTelemetryDoc() {
    acknowledgeTelemetryDisclosure();
    if (window.switchNavTab) window.switchNavTab('docs');
    import('./docs.js').then(m => m.selectDoc('telemetry'));
}
