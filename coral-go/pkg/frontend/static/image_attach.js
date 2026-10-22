/* The Add image button (#6088): what a picked photo goes through before it is uploaded.
 *
 * The file input is the only way in (never an in-page camera: Coral Hub blocks getUserMedia). A photo over 2 MB is made smaller in the browser, on an off-page canvas, so a phone's
 * 8 to 12 MB photo goes up in about 1 MB on a slow connection; a PNG (a screenshot) and a GIF are kept as they are. The server still decides what it accepts. */

const MiB = 1024 * 1024;
export const SERVER_CAP = 20 * MiB; // the server's limit for one image (uploads.go maxFileSize)
// Coral Hub caps a request body at 16 MiB and 60 s today; for POST /api/upload it is being raised to 24 MiB and about 5 minutes. Until that ships, an image over 16 MiB is refused here, through the
// hub, with a plain message. When the hub's change is out, delete this constant and use SERVER_CAP alone.
export const HUB_CAP = 16 * MiB;
export const DOWNSCALE_OVER = 2 * MiB;
export const LONG_SIDE = 2560;
export const JPEG_QUALITY = 0.85;
// The picker offers these types and not SVG (it is kept for the agent only, never shown) or HEIC. A library photo is handed over as a JPEG when HEIC is not listed.
export const PICKER_ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff';

export const HEIC_REFUSAL = "Coral can't read HEIC photos. Send it as a JPEG or PNG (on an iPhone: Settings > Camera > Formats > Most Compatible, or pick the photo again from Photos).";

const isHeic = f => /^image\/(heic|heif|heics|avif)$/i.test(f.type || '') || /\.(heic|heif|heics|avif)$/i.test(f.name || '');
const mb = n => `${Math.round(n / MiB)} MB`;

/** The limit for one image: the hub's while the dashboard is Coral Hub's, else the server's. */
export const capFor = viaHub => (viaHub ? HUB_CAP : SERVER_CAP);

/** A photo is made smaller when it is over 2 MB and is not a PNG or a GIF (those are screenshots and animations: kept whole). */
export function shouldDownscale(file) {
    return file.size > DOWNSCALE_OVER && /^image\/(jpeg|webp|bmp|tiff)$/i.test(file.type || '');
}

/** The size a picture is drawn at: its long side at most LONG_SIDE, the shape kept. */
export function scaledSize(w, h) {
    const k = Math.min(1, LONG_SIDE / Math.max(w, h));
    return { width: Math.max(1, Math.round(w * k)), height: Math.max(1, Math.round(h * k)) };
}

async function downscale(file) {
    const bmp = await createImageBitmap(file); // the photo's own orientation is applied (the browser's default)
    try {
        const { width, height } = scaledSize(bmp.width, bmp.height);
        const canvas = document.createElement('canvas'); // never attached to the page
        canvas.width = width; canvas.height = height;
        canvas.getContext('2d').drawImage(bmp, 0, 0, width, height);
        const blob = await new Promise(res => canvas.toBlob(res, 'image/jpeg', JPEG_QUALITY));
        if (!blob || blob.size >= file.size) return file; // not smaller: the original goes
        return new File([blob], file.name.replace(/\.[^.]*$/, '') + '.jpg', { type: 'image/jpeg' });
    } finally { bmp.close && bmp.close(); }
}

/** A picked file as it is uploaded: { file } or { error } (a sentence for a toast). viaHub: the dashboard is Coral Hub's. */
export async function prepareImage(file, { viaHub = false } = {}) {
    if (isHeic(file)) return { error: HEIC_REFUSAL };
    if (!/^image\//i.test(file.type || '')) return { error: 'That file is not a picture.' };
    let out = file;
    if (shouldDownscale(file)) {
        try { out = await downscale(file); } catch { out = file; } // the original goes when the browser cannot draw it
    }
    const cap = capFor(viaHub);
    if (out.size > cap) return { error: `That image is over ${mb(cap)}${viaHub ? ' (the limit through Coral Hub)' : ''}.` };
    return { file: out };
}

/** The server's reason a shared copy was not made, with "try again at HH:MM" in the viewer's own time when it said when. */
export function artifactErrorText(result) {
    const why = String((result && result.artifact_error) || '');
    const at = result && result.artifact_retry_at ? new Date(result.artifact_retry_at) : null;
    if (!why || !at || isNaN(at)) return why;
    const hhmm = at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return `${why.replace(/;?\s*try again in .*$/, '')}; try again at ${hhmm}`;
}

export const UPLOAD_UNFINISHED = "Upload didn't finish. Try again.";

/** The upload's reply as an object, or null when it is not one: Coral Hub cut an upload short (its size or time limit) answers an EMPTY 200 and nothing reaches the machine, so an empty or unparseable
 *  body, or one with no saved path, is a failed upload, never a success. */
export function parseUploadReply(text) {
    if (typeof text !== 'string' || !text.trim()) return null;
    let r; try { r = JSON.parse(text); } catch { return null; }
    if (!r || typeof r !== 'object' || Array.isArray(r)) return null;
    if (typeof r.error === 'string' && r.error) return r; // an error the server said in words
    return typeof r.path === 'string' && r.path ? r : null;
}

/** A plain sentence and whether Retry makes sense, for an upload answered with a failing status: 413 (too big for the server or the hub), 408 (too slow for the hub's time limit), a hub that is not
 *  answering. null for any other status: the server's own words are shown. */
export function uploadStatusFailure(status) {
    if (status === 413) return { text: 'That image is too big to send. Pick a smaller one.', retry: false };
    if (status === 408) return { text: 'The upload took too long. Try again, on a faster connection if you can.', retry: true };
    if (status === 502 || status === 503 || status === 504) return { text: UPLOAD_UNFINISHED, retry: true };
    return null;
}
