/* Image and media previews: panes, zoom and pan (moved out of utils.js, #5065; no behaviour change). */
// File types supported by the raw preview endpoints (keep in sync with the
// preview type maps in routes/sessions.go).
const IMAGE_EXT_RE = /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i;

const MEDIA_EXT_RE = /\.(mp3|wav|ogg|oga|m4a|aac|flac|mp4|webm|ogv|mov|m4v)$/i;

export function isImagePath(filepath) {
    return IMAGE_EXT_RE.test(filepath || '');
}

export function isMediaPath(filepath) {
    return MEDIA_EXT_RE.test(filepath || '');
}

/** Render images into container. `panes` is a list of { label, url, missing } (or `load` instead of `url`, an async
 *  function resolving to { src, revoke }):
 *  one pane for a preview, Before/After for a diff. A pane whose image fails
 *  to load shows its `missing` text instead (e.g. a file new since the base). */
export function renderImagePanes(container, panes) {
    container.innerHTML = '';
    const wrap = document.createElement('div');
    wrap.className = 'image-panes' + (panes.length > 1 ? ' image-panes-compare' : '');
    const frames = [];
    for (const pane of panes) {
        const fig = document.createElement('figure');
        fig.className = 'image-pane';
        if (pane.label) {
            const label = document.createElement('div');
            label.className = 'image-pane-label';
            label.textContent = pane.label;
            fig.appendChild(label);
        }
        const frame = document.createElement('div');
        frame.className = 'image-pane-frame';
        const img = document.createElement('img');
        img.alt = pane.label || '';
        img.decoding = 'async';
        const meta = document.createElement('figcaption');
        meta.className = 'image-pane-meta';
        img.addEventListener('load', () => {
            meta.textContent = `${img.naturalWidth} × ${img.naturalHeight}`;
        });
        img.addEventListener('error', () => {
            frame.innerHTML = '';
            const note = document.createElement('div');
            note.className = 'image-pane-missing';
            note.textContent = pane.missing || 'Image could not be loaded';
            frame.appendChild(note);
            meta.textContent = '';
        });
        if (pane.load) {
            // Async source (the Cloud Hub): { src, revoke } from image_source.fetchImageSrc.
            pane.load().then(({ src, revoke }) => {
                if (revoke) img.addEventListener('load', () => URL.revokeObjectURL(src), { once: true });
                img.src = src;
            }).catch(() => img.dispatchEvent(new Event('error')));
        } else {
            img.src = pane.url;
        }
        frame.appendChild(img);
        fig.append(frame, meta);
        wrap.appendChild(fig);
        frames.push({ frame, img });
    }
    container.appendChild(wrap);
    wrap.appendChild(_imageZoomControls(frames));
    _enableImagePanZoom(frames);
}

const ZOOM_MIN = 0.1;

const ZOOM_MAX = 32;

/** Wheel-zoom (toward the cursor), drag-pan and double-click-reset, shared by
 *  every frame so Before/After comparisons stay aligned. */
function _enableImagePanZoom(frames) {
    const view = { k: 1, x: 0, y: 0 };
    const listeners = new Set();
    const apply = () => {
        for (const { img } of frames) img.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.k})`;
        listeners.forEach(fn => fn(view.k));
    };
    const zoomAt = (factor, px, py) => {
        const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, view.k * factor));
        const r = k / view.k;
        // The image is centred in its frame, so px/py are measured from that centre.
        view.x = px - (px - view.x) * r;
        view.y = py - (py - view.y) * r;
        view.k = k;
        apply();
    };
    const reset = () => { view.k = 1; view.x = 0; view.y = 0; apply(); };
    frames._view = { zoomAt, reset, onChange: fn => listeners.add(fn), get k() { return view.k; } };

    for (const { frame, img } of frames) {
        frame.classList.add('image-pane-zoomable');
        const fromCentre = (e) => {
            const r = frame.getBoundingClientRect();
            return [e.clientX - (r.left + r.width / 2), e.clientY - (r.top + r.height / 2)];
        };
        frame.addEventListener('wheel', (e) => {
            e.preventDefault();
            const [px, py] = fromCentre(e);
            zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.002)), px, py);
        }, { passive: false });
        let drag = null;
        frame.addEventListener('pointerdown', (e) => {
            if (e.button !== 0) return;
            drag = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
            frame.setPointerCapture(e.pointerId);
            frame.classList.add('panning');
        });
        frame.addEventListener('pointermove', (e) => {
            if (!drag) return;
            view.x = drag.vx + e.clientX - drag.x;
            view.y = drag.vy + e.clientY - drag.y;
            apply();
        });
        const end = () => { drag = null; frame.classList.remove('panning'); };
        frame.addEventListener('pointerup', end);
        frame.addEventListener('pointercancel', end);
        frame.addEventListener('dblclick', reset);
        img.draggable = false;
    }
}

function _imageZoomControls(frames) {
    const bar = document.createElement('div');
    bar.className = 'image-zoom-controls';
    const mk = (text, title, fn) => {
        const b = document.createElement('button');
        b.type = 'button'; b.textContent = text; b.title = title;
        b.addEventListener('click', fn);
        return b;
    };
    const level = document.createElement('span');
    level.className = 'image-zoom-level';
    level.textContent = '100%';
    const centre = () => [0, 0];
    bar.append(
        mk('−', 'Zoom out', () => frames._view.zoomAt(1 / 1.5, ...centre())),
        level,
        mk('+', 'Zoom in', () => frames._view.zoomAt(1.5, ...centre())),
        mk('Fit', 'Reset zoom and position (or double-click the image)', () => frames._view.reset()),
    );
    // frames._view is created by _enableImagePanZoom, which runs right after this.
    queueMicrotask(() => frames._view?.onChange(k => { level.textContent = `${Math.round(k * 100)}%`; }));
    return bar;
}

/** Render playable audio/video files into container. `panes` accepts the same
 * shape as renderImagePanes so media diffs can show Before/After players. */
export function renderMediaPanes(container, panes) {
    container.innerHTML = '';
    const wrap = document.createElement('div');
    wrap.className = 'media-panes' + (panes.length > 1 ? ' media-panes-compare' : '');
    for (const pane of panes) {
        const fig = document.createElement('figure');
        fig.className = 'media-pane';
        if (pane.label) {
            const label = document.createElement('div');
            label.className = 'image-pane-label';
            label.textContent = pane.label;
            fig.appendChild(label);
        }
        const frame = document.createElement('div');
        frame.className = 'media-pane-frame';
        // The media URL is an API endpoint, so inspect its filepath query
        // parameter rather than the endpoint path itself.
        let mediaPath = pane.url;
        try { mediaPath = new URL(pane.url, window.location.href).searchParams.get('filepath') || pane.url; } catch { /* use URL text */ }
        const isVideo = /\.(mp4|webm|ogv|mov|m4v)$/i.test(mediaPath);
        const media = document.createElement(isVideo ? 'video' : 'audio');
        media.controls = true;
        media.preload = 'metadata';
        media.setAttribute('playsinline', '');
        media.addEventListener('error', () => {
            frame.innerHTML = '';
            const note = document.createElement('div');
            note.className = 'image-pane-missing';
            note.textContent = pane.missing || 'Media could not be loaded';
            frame.appendChild(note);
        });
        media.src = pane.url;
        frame.appendChild(media);
        fig.appendChild(frame);
        wrap.appendChild(fig);
    }
    container.appendChild(wrap);
}
