//go:build webview

package main

// linkInterceptScript decides what a click on a link does in the app window (#5094). It runs in every page the window shows, before the page's own script.
//
//   - The address is resolved against the page (new URL), and what it is is read from that: the old test was a string prefix ("starts with the page's
//     origin"), which "http://localhost:8420.evil.example/" and "http://localhost:8420@evil.example/" pass, so the window itself navigated there and the
//     page, looking like Coral, had the app's native bindings.
//   - Only an http or https address is ever followed. A file:, a custom scheme, a javascript: or an address that does not parse does nothing (the window
//     is not navigated to a local file or handed to the system's opener).
//   - A web address on another origin, and any link with target _blank, opens in the system browser; a same-origin link without target is the page's own.
const linkInterceptScript = `document.addEventListener('click', function(e) {
	var a = e.target && e.target.closest ? e.target.closest('a') : null;
	if (!a) return;
	var href = a.getAttribute('href');
	if (!href) return;
	var u;
	try { u = new URL(href, location.href); } catch (err) { e.preventDefault(); return; }
	if (u.protocol !== 'http:' && u.protocol !== 'https:') { e.preventDefault(); return; }
	if (u.origin !== location.origin || a.target === '_blank') {
		e.preventDefault();
		if (window._coralOpenExternal) { _coralOpenExternal(u.href); } else { window.open(u.href, '_blank'); }
	}
}, true);`
