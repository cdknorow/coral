/* Whether a phone's on-screen keyboard is open (#6694): the visible viewport is much shorter than the tallest one seen at this width. Sets `kb-open` on <html> so the Terminal view can give its rows back
 * (the sign-in banner, the chips and the status bar step aside in mobile.css). Only a phone-width touch screen counts: a desktop window or a mouse-driven narrow one being resized is not a keyboard. */
const SHRUNK = 0.75;
let started = false;
let base = 0, baseWidth = 0;

const height = () => (window.visualViewport ? window.visualViewport.height : window.innerHeight);

function update() {
    const w = window.innerWidth;
    const h = height();
    if (w !== baseWidth || h > base) { baseWidth = w; base = h; } // a rotation or a taller view resets what "closed" means
    const phone = w <= 767 && window.matchMedia('(pointer: coarse)').matches;
    document.documentElement.classList.toggle('kb-open', phone && h < base * SHRUNK);
}

export function startKeyboardWatch() {
    if (started) return;
    started = true;
    update();
    window.addEventListener('resize', update);
    if (window.visualViewport) window.visualViewport.addEventListener('resize', update);
}
