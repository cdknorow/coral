//go:build webview

// Command coral-app opens the Coral dashboard in a native webview window.
//
// This is a lightweight wrapper around webview/webview_go that displays
// the Coral web dashboard without browser chrome (no URL bar, no tabs).
// The server must be running separately (via coral or coral-tray).
//
// Usage:
//
//	coral-app                             # Open dashboard at default URL
//	coral-app --url http://localhost:9000  # Custom server URL
//	coral-app --width 1400 --height 900   # Custom window size
//	coral-app --debug                     # Enable debug logging to app.log in the data folder
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"

	"github.com/subgentic/coral/internal/apptoken"
	"github.com/subgentic/coral/internal/config"
	_ "github.com/subgentic/coral/internal/dllpreload" // the webview's bare-name DLL loads find the system folder's modules (#5724)
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/loginlink"
	"github.com/subgentic/coral/internal/logrotate"
)

var debugMode bool

// webviewInstance holds the webview reference for signal-triggered shutdown.
// Set after webview creation so the signal handler can call Terminate()
// instead of os.Exit, allowing deferred cleanup (webview.Destroy) to run.
var webviewInstance webview.WebView

// init locks the main goroutine to OS thread 0 before any scheduling.
// This must happen in init(), not main(), because Go may reschedule the
// main goroutine to a different OS thread before main() runs. macOS
// Cocoa/WKWebView requires thread 0.
func init() {
	runtime.LockOSThread()
}

func main() {

	url := flag.String("url", "http://localhost:8420", "Coral server URL")
	title := flag.String("title", "Coral", "Window title")
	width := flag.Int("width", 1200, "Window width")
	height := flag.Int("height", 800, "Window height")
	wait := flag.Bool("wait", false, "Wait for server to be ready before opening")
	debugFlag := flag.Bool("debug", false, "Enable debug logging to app.log in the data folder")
	dataDir := flag.String("data-dir", "", "Coral's data folder, to ask it for a sign-in link (default: the usual one)")
	flag.Parse()
	// A sign-in link handed over by the tray (#3394), in the environment so no other user can
	// read it with ps. Used once: drop it so nothing this window starts inherits it.
	handedLink := os.Getenv("CORAL_APP_LOGIN_URL")
	os.Unsetenv("CORAL_APP_LOGIN_URL")

	debugMode = *debugFlag || os.Getenv("CORAL_DEBUG") == "1"

	// Always log to file so we catch crashes — debug mode adds JS console redirect
	setupDebugLogging(*dataDir)

	// Check EULA acceptance before proceeding. On macOS, this shows a native
	// Cocoa dialog on first launch. If the user declines, the app exits.
	if !checkAndShowEULA() {
		fmt.Fprintln(os.Stderr, "Terms of Service must be accepted to use Coral.")
		os.Exit(0)
	}

	// Global panic recovery — log the stack trace before crashing
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic recovered: %v", r)
			log.Printf("[FATAL] stack trace:\n%s", debug.Stack())
			os.Exit(1)
		}
	}()

	// Ignore SIGHUP — macOS sends it during sleep/wake transitions
	signal.Ignore(syscall.SIGHUP)

	// Catch SIGTERM/SIGINT for clean shutdown — terminate the webview event
	// loop so deferred Destroy() runs, preventing leaked WKWebView processes.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("[SHUTDOWN] received signal: %v", sig)
		log.Printf("[SHUTDOWN] goroutines: %d", runtime.NumGoroutine())
		if w := webviewInstance; w != nil {
			log.Println("[SHUTDOWN] terminating webview event loop")
			w.Terminate()
		} else {
			log.Println("[SHUTDOWN] no webview yet, exiting")
			os.Exit(1)
		}
	}()

	log.Printf("[STARTUP] coral-app starting pid=%d", os.Getpid())
	log.Printf("[STARTUP] args=%v", os.Args)
	log.Printf("[STARTUP] go=%s os=%s arch=%s goroutines=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumGoroutine())
	log.Printf("[STARTUP] url=%s title=%s size=%dx%d wait=%v debug=%v", *url, *title, *width, *height, *wait, debugMode)

	// Optionally wait for the server to be ready
	if *wait {
		log.Printf("[STARTUP] waiting for server at %s...", *url)
		if !waitForServer(*url, 15*time.Second) {
			log.Printf("[STARTUP] server not responding after 15s — exiting")
			fmt.Fprintf(os.Stderr, "Server at %s is not responding. Is Coral running?\n", *url)
			os.Exit(1)
		}
		log.Println("[STARTUP] server is ready")
	}

	// Sign in without the API key or a prompt (#3394): the link the tray handed over, or a
	// fresh one from this computer's Coral. Without one (an older Coral) the URL itself opens.
	startURL := handedLink
	if startURL == "" || !loginlink.HasToken(startURL) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		link, err := loginlink.FromCLI(ctx, *url, config.ResolveCoralDir(*dataDir))
		cancel()
		if err != nil {
			log.Printf("[STARTUP] no sign-in link (%v); opening %s", err, *url)
			startURL = *url
		} else {
			startURL = link
		}
	}
	// The session this link makes is named "Coral app on ..." on the Sessions page (#6100), so a person can tell it from their own browser.
	startURL = loginlink.MarkApp(startURL)

	log.Println("[WEBVIEW] calling showInDock()")
	showInDock()
	log.Println("[WEBVIEW] showInDock() dispatched")

	log.Println("[WEBVIEW] installing Edit menu")
	installEditMenu()
	log.Println("[WEBVIEW] Edit menu dispatched")

	// DevTools give script access to the signed-in dashboard: only a dev build opens them, whatever --debug or CORAL_DEBUG
	// say (they switch the debug LOG on in every build, #78 W2).
	devTools := debugMode && config.TierDevTools
	// WebView2's profile (the dashboard's sign-in cookie) belongs on this computer, not in the Roaming folder: set before the window exists.
	if useLocalWebViewProfile() != "" {
		log.Println("[WEBVIEW] the profile is in a local folder (not the Roaming folder)") // no path: the log carries no user name
	}
	log.Printf("[WEBVIEW] creating webview (devtools=%v)", devTools)
	w := webview.New(devTools)
	webviewInstance = w
	log.Println("[WEBVIEW] webview created successfully")
	defer func() {
		log.Println("[WEBVIEW] destroying webview")
		w.Destroy()
		log.Println("[WEBVIEW] webview destroyed")
	}()

	log.Printf("[WEBVIEW] setting title=%q size=%dx%d", *title, *width, *height)
	installFilePanel(w.Window()) // a file input opens the system chooser (#5743)
	w.SetTitle(*title)
	workW, workH := workArea()
	fw, fh := fitSize(*width, *height, workW, workH)
	if fw != *width || fh != *height {
		log.Printf("[WEBVIEW] the screen's work area cannot hold %dx%d: opening at %dx%d", *width, *height, fw, fh)
	}
	w.SetSize(fw, fh, webview.HintNone)
	placeInWorkArea(w.Window()) // the cascade can put the window partly off a small screen (#6004 W5)

	// Configure native title bar (macOS: transparent, content extends into title bar)
	log.Println("[WEBVIEW] setting up native titlebar")
	setupNativeTitlebar()
	log.Println("[WEBVIEW] titlebar setup complete")

	// Inject native app flag and apply classes SYNCHRONOUSLY on <html> element.
	// documentElement exists before CSS is evaluated, ensuring -webkit-app-region: drag
	// and WKWebView layout fixes are active from the first layout pass.
	// Also applies on <body> at DOMContentLoaded as a safety net.
	log.Println("[WEBVIEW] injecting native app JS (class flags, external link handler)")
	w.Init(logHintScript(config.ResolveCoralDir(*dataDir)))
	// documentElement and body can be null where the script runs (a blank first page): each is marked only when it exists, and again at DOMContentLoaded.
	w.Init(`window.__CORAL_APP__ = true;
	(function() {
		var mac = navigator.platform && navigator.platform.indexOf('Mac') !== -1;
		var win = navigator.platform && navigator.platform.indexOf('Win') !== -1;
		function mark(el) {
			if (!el || !el.classList) return;
			el.classList.add('native-app');
			if (mac) el.classList.add('native-macos');
			if (win) el.classList.add('native-windows');
		}
		mark(document.documentElement);
		document.addEventListener('DOMContentLoaded', function() { mark(document.documentElement); mark(document.body); });
	})();

	// Server health check — detect when the backend crashes
	(function() {
		var wasConnected = false;
		var overlayEl = null;

		function getOverlay() {
			if (overlayEl) return overlayEl;
			overlayEl = document.getElementById('coral-app-disconnect-overlay');
			if (overlayEl) return overlayEl;
			// Create overlay if it doesn't exist yet (injected before DOM loads)
			var el = document.createElement('div');
			el.id = 'coral-app-disconnect-overlay';
			el.style.cssText = 'display:none;position:fixed;inset:0;z-index:99999;background:rgba(0,0,0,0.75);align-items:center;justify-content:center;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,sans-serif;color:#e0e0e0;';
			el.innerHTML = '<div style="text-align:center;max-width:400px;padding:32px">'
				+ '<div style="font-size:48px;margin-bottom:16px">⚠️</div>'
				+ '<h2 style="margin:0 0 8px;font-size:20px;color:#fff">Server Disconnected</h2>'
				+ '<p style="margin:0 0 16px;font-size:14px;color:#aaa">Coral may have crashed. Check <code style="background:rgba(255,255,255,0.1);padding:2px 6px;border-radius:3px;font-size:12px">~/.coral/coral.log</code> for details.</p>'
				+ '<div style="display:flex;align-items:center;gap:8px;justify-content:center">'
				+ '<span class="coral-reconnect-spinner" style="display:inline-block;width:16px;height:16px;border:2px solid rgba(255,255,255,0.2);border-top-color:#fff;border-radius:50%;animation:coral-reconnect-spin 0.8s linear infinite"></span>'
				+ '<span style="font-size:13px;color:#888">Reconnecting...</span>'
				+ '</div></div>';
			// The log this server writes, in its own data folder (#4151), as text.
			var code = el.querySelector('code');
			if (code && window.__CORAL_LOG_PATH__) code.textContent = window.__CORAL_LOG_PATH__;
			// Add spinner animation
			var style = document.createElement('style');
			style.textContent = '@keyframes coral-reconnect-spin { to { transform: rotate(360deg); } }';
			document.head.appendChild(style);
			document.body.appendChild(el);
			overlayEl = el;
			return el;
		}

		function checkHealth() {
			fetch('/api/health', { method: 'GET', cache: 'no-store' })
				.then(function(r) {
					if (r.ok) {
						if (!wasConnected) wasConnected = true;
						var ov = getOverlay();
						if (ov.style.display !== 'none') {
							ov.style.display = 'none';
							// Reload to restore full state after reconnect
							location.reload();
						}
					} else {
						showDisconnect();
					}
				})
				.catch(function() {
					showDisconnect();
				});
		}

		function showDisconnect() {
			if (!wasConnected) return; // Don't show overlay during initial load
			var ov = getOverlay();
			ov.style.display = 'flex';
		}

		// Start polling after page loads
		document.addEventListener('DOMContentLoaded', function() {
			wasConnected = true;
			setInterval(checkHealth, 5000);
		});
	})();`)

	// What a link may do in this window (#5094): see linkInterceptScript.
	w.Init(linkInterceptScript)
	w.Init(titlebarFollowScript)

	// The native functions are bound on every page the window shows and the Go side is not told the page's origin, so each takes a
	// per-launch token first and refuses a call without it. Only an init script that runs on the dashboard's origin hands the
	// token to the page-facing shims (apptoken, #78 W1): a page of another origin cannot call them, even through
	// window.chrome.webview.postMessage. The check here, not the deleting of shims off-origin, is the boundary.
	guard, gerr := apptoken.New()
	if gerr != nil {
		log.Fatalf("[FATAL] cannot make the window's call token: %v", gerr)
	}
	refused := func(name string) {
		log.Printf("[WEBVIEW] refused a call to %s without this window's token (a page that is not the dashboard)", name)
	}
	natives := []apptoken.Native{
		{Page: "_coralOpenExternal", Native: "__coralNativeOpenExternal"},
		{Page: "_coralAppSignIn", Native: "__coralNativeAppSignIn"},
		{Page: "_coralTitlebar", Native: "__coralNativeTitlebar"},
	}

	// Bind native URL opener — WKWebView doesn't support window.open(),
	// so the link interceptor calls this to open external URLs in the system browser.
	// The binding is callable by whatever page the window shows, and the system's opener runs files and starts custom
	// schemes: only a plain http or https address goes through (executil.WebURL).
	w.Bind("__coralNativeOpenExternal", func(token, raw string) {
		if !guard.Allowed(token) {
			refused("openExternal")
			return
		}
		if !executil.OpenWebURL(raw) {
			log.Printf("[WEBVIEW] refused to open an address that is not a plain http or https URL (%d bytes)", len(raw))
			return
		}
		log.Printf("[WEBVIEW] opening external URL: %s", loginlink.Redact(raw))
	})

	// The title bar follows the page's theme: the page reports the colours of --bg-primary and --border (see titlebarFollowScript) and the native side
	// checks that they are colours before it paints the caption with them.
	w.Bind("__coralNativeTitlebar", func(token, bg, border string) {
		if !guard.Allowed(token) {
			refused("titlebar")
			return
		}
		w.Dispatch(func() { setTitlebarColors(bg, border) })
	})

	// Bind JS console.log to Go logger in debug mode
	log.Println("[WEBVIEW] setting up JS console redirect and WS monitoring")
	if debugMode {
		w.Bind("__coralNativeLog", func(token, level, msg string) {
			if !guard.Allowed(token) {
				refused("log")
				return
			}
			log.Printf("[JS %s] %s", level, msg)
		})
		natives = append(natives, apptoken.Native{Page: "_coralLog", Native: "__coralNativeLog"})

		// Inject JS that redirects console.log/warn/error to Go
		w.Init(`
			(function() {
				const origLog = console.log;
				const origWarn = console.warn;
				const origError = console.error;
				console.log = function() {
					origLog.apply(console, arguments);
					try { _coralLog('LOG', Array.from(arguments).join(' ')); } catch(e) {}
				};
				console.warn = function() {
					origWarn.apply(console, arguments);
					try { _coralLog('WARN', Array.from(arguments).join(' ')); } catch(e) {}
				};
				console.error = function() {
					origError.apply(console, arguments);
					try { _coralLog('ERROR', Array.from(arguments).join(' ')); } catch(e) {}
				};

				// Log navigation and WebSocket events
				window.addEventListener('hashchange', function(e) {
					_coralLog('NAV', 'hashchange: ' + location.hash);
				});
				window.addEventListener('popstate', function(e) {
					_coralLog('NAV', 'popstate: ' + location.href);
				});

				// Monitor WebSocket connections
				const origWS = window.WebSocket;
				window.WebSocket = function(url, protocols) {
					_coralLog('WS', 'connecting: ' + url);
					const ws = new origWS(url, protocols);
					ws.addEventListener('open', function() { _coralLog('WS', 'connected: ' + url); });
					ws.addEventListener('close', function(e) { _coralLog('WS', 'closed: ' + url + ' code=' + e.code); });
					ws.addEventListener('error', function() { _coralLog('WS', 'error: ' + url); });
					return ws;
				};
				window.WebSocket.prototype = origWS.prototype;
				window.WebSocket.CONNECTING = origWS.CONNECTING;
				window.WebSocket.OPEN = origWS.OPEN;
				window.WebSocket.CLOSING = origWS.CLOSING;
				window.WebSocket.CLOSED = origWS.CLOSED;
			})();
		`)

		log.Printf("Navigating to %s", loginlink.Redact(startURL))
	}

	// The session ended (or the server restarted with a new one) and the dashboard shows its
	// sign-in page: ask Coral for a fresh link and sign in again, without a prompt. Only for
	// this Coral's own sign-in page, and at most once every 30 s, so a page that keeps
	// answering 403 can't loop. The link never reaches the page's script. It lands on the page
	// the sign-in page was asked for (its return_to), when that is a page of this dashboard (#3532).
	baseOrigin := originOf(*url)
	var lastSignIn time.Time
	var signInMu sync.Mutex
	w.Bind("__coralNativeAppSignIn", func(token, page string) {
		if !guard.Allowed(token) {
			refused("appSignIn")
			return
		}
		signInMu.Lock()
		if time.Since(lastSignIn) < 30*time.Second {
			signInMu.Unlock()
			return
		}
		lastSignIn = time.Now()
		signInMu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			link, err := loginlink.FromCLI(ctx, *url, config.ResolveCoralDir(*dataDir))
			if err != nil {
				log.Printf("[WEBVIEW] sign-in page shown; no new sign-in link: %v", err)
				return
			}
			link = loginlink.MarkApp(loginlink.OnPage(link, page))
			log.Printf("[WEBVIEW] sign-in page shown; signing in again at %s", loginlink.Redact(link))
			w.Dispatch(func() { w.Navigate(link) })
		}()
	})
	// The page-facing functions (_coralOpenExternal, _coralAppSignIn, _coralLog), which add the token, exist only on the
	// dashboard's origin (apptoken.ShimScript). Registered after the Binds and BEFORE the sign-in call below, so the shim is there when
	// that script runs. A native navigation allowlist (WebView2 NavigationStarting) and a dashboard CSP are still wanted (#78 W1):
	// webview_go does not expose the first.
	w.Init(guard.ShimScript(baseOrigin, natives))
	w.Init(fmt.Sprintf(`(function(){ if (location.origin === %q && location.pathname === '/auth' && window._coralAppSignIn) window._coralAppSignIn(new URLSearchParams(location.search).get('return_to') || '/'); })();`, baseOrigin))

	log.Printf("[WEBVIEW] navigating to %s", loginlink.Redact(startURL))
	w.Navigate(startURL)

	log.Printf("[WEBVIEW] starting event loop (goroutines=%d)", runtime.NumGoroutine())
	w.Run()
	log.Printf("[WEBVIEW] event loop exited (goroutines=%d)", runtime.NumGoroutine())
	log.Println("[SHUTDOWN] coral-app exiting normally")
}

// setupDebugLogging redirects log output to app.log in Coral's data folder (--data-dir,
// else the usual one: CORAL_DATA_DIR, CORAL_DIR or ~/.coral), so a window for another
// Coral logs with that Coral (#3463).
func setupDebugLogging(dataDir string) {
	logDir := config.ResolveCoralDir(dataDir)
	os.MkdirAll(logDir, 0o700)
	logFile := filepath.Join(logDir, "app.log")
	// Rotates as coral.log does (#4112): 0600, no symlink followed, 20 MB x 5.
	f, err := logrotate.Open(logFile, logrotate.DefaultMaxSize, logrotate.DefaultKeep, logrotate.DefaultTrim)
	if err != nil {
		return
	}
	log.SetOutput(logsafe.Writer(f)) // no home folder, user or computer name in app.log (#6053)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	fmt.Fprintf(os.Stderr, "Debug logging to %s\n", logFile)
}

// waitForServer polls the server URL until it responds or timeout is reached.
func waitForServer(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// originOf returns the scheme://host[:port] of raw, as the page sees location.origin.
func originOf(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// titlebarFollowScript reports the page's theme colours to the native title bar (the page-facing _coralTitlebar exists only on the dashboard's origin): once the page is
// ready, and again whenever the theme changes (data-theme, a custom theme's inline variables, or the system's mode when the choice is System).
const titlebarFollowScript = `(function() {
	var timer = null;
	function push() {
		if (typeof window._coralTitlebar !== 'function' || !document.body) return;
		var cs = getComputedStyle(document.body);
		window._coralTitlebar(cs.getPropertyValue('--bg-primary').trim(), cs.getPropertyValue('--border').trim());
	}
	function later() { clearTimeout(timer); timer = setTimeout(push, 60); }
	document.addEventListener('DOMContentLoaded', function() {
		push();
		var opts = { attributes: true, attributeFilter: ['data-theme', 'style', 'class'] };
		new MutationObserver(later).observe(document.documentElement, opts);
		new MutationObserver(later).observe(document.body, opts);
		try { window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', later); } catch (e) {}
	});
})();`
