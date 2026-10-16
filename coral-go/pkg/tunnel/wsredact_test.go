package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3562: Flush settles a quiet stream's tail without ever showing a character of a token's
// value. The first letters of a prefix are shown and kept as context; a whole prefix is held,
// and redacted on a forced flush, with the rest of its run dropped.
func TestRedactorFlush(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	run := func(steps ...any) string {
		var out bytes.Buffer
		r := NewRedactor(&out)
		for _, s := range steps {
			switch v := s.(type) {
			case string:
				_, _ = r.Write([]byte(v))
			case bool:
				require.NoError(t, r.Flush(v))
			}
		}
		require.NoError(t, r.Close())
		return out.String()
	}
	steps := func(steps ...any) []any { return steps }

	// The first letters of a prefix are shown; the token they start is still caught.
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, _ = r.Write([]byte("echo g"))
	assert.Equal(t, "echo ", out.String(), "'g' could start a prefix: held")
	require.NoError(t, r.Flush(false))
	assert.Equal(t, "echo g", out.String(), "shown when the stream goes quiet")
	_, _ = r.Write([]byte(tok[1:] + " x"))
	require.NoError(t, r.Close())
	assert.Equal(t, "echo g"+RedactMarker+" x", out.String())

	// A whole prefix with part of its value: held, never shown; forced, the marker, and the
	// rest of its run is dropped.
	out.Reset()
	r = NewRedactor(&out)
	_, _ = r.Write([]byte("x " + tok[:12]))
	require.NoError(t, r.Flush(false))
	assert.Equal(t, "x ", out.String(), "a value is never shown on a flush")
	require.NoError(t, r.Flush(true))
	assert.Equal(t, "x "+RedactMarker, out.String())
	_, _ = r.Write([]byte(tok[12:] + " more"))
	require.NoError(t, r.Close())
	assert.Equal(t, "x "+RedactMarker+" more", out.String())

	// A whole prefix, or a scheme, with no value yet: shown (nothing secret), then caught.
	out.Reset()
	r = NewRedactor(&out)
	_, _ = r.Write([]byte("x ghp_"))
	require.NoError(t, r.Flush(false))
	assert.Equal(t, "x ghp_", out.String())
	_, _ = r.Write([]byte(tok[4:] + " y"))
	require.NoError(t, r.Close())
	assert.Equal(t, "x ghp_"+RedactMarker+" y", out.String())
	assert.Equal(t, "Authorization: Bearer "+RedactMarker+" z",
		run(steps("Authorization: Bearer", false, " "+fake("", 40, alnum)+" z")...))

	// A value already long enough is redacted the same way.
	assert.Equal(t, RedactMarker+" ok", run(steps(tok, true, "abc ok")...))
	// A near miss shown after a flush comes out as it would whole.
	assert.Equal(t, "pip install sk-learn now", run(steps("pip install s", false, "k", false, "-learn", false, " now")...))
	// The kept part of a Bearer value: the scheme shows, the value doesn't.
	val := fake("", 40, alnum)
	assert.Equal(t, "Bearer "+RedactMarker+" z", run(steps("Bea", false, "rer "+val+" z")...))
	// A private key: nothing of it shows, flushed or not, until its END line.
	key := pemKey("RSA ", 3, "\n")
	assert.Equal(t, "k "+RedactMarker+" after", run(steps("k "+key[:90], true, false, key[90:]+" after")...))
	// A BEGIN line still arriving has no secret in it: shown, and the key is still caught.
	assert.Equal(t, key[:18]+RedactMarker+" ok", run(steps(key[:18], false, key[18:]+" ok")...))
}

// No flush, wherever it falls and forced or not, shows any part of a token's value, and with
// no flush the stream is still what redacting the whole gives (TestRedactStreamEqualsWhole).
func TestRedactorFlushNeverShowsAValue(t *testing.T) {
	values := map[string]string{
		"ghp_":                   fake("", 40, alnum),
		"sk-":                    "ant-" + fake("", 30, alnum+"-"),
		"Authorization: Bearer ": fake("", 60, alnum+"."),
		"AKIA":                   fake("", 16, upper),
		"xoxb-":                  fake("", 15, alnum+"-"),
	}
	key := pemKey("EC ", 4, "\n")
	var body strings.Builder
	for _, pre := range []string{"ghp_", "sk-", "Authorization: Bearer ", "AKIA", "xoxb-"} {
		body.WriteString("line of output, then ")
		body.WriteString(pre + values[pre])
		body.WriteString("\r\n\x1b[32mok\x1b[0m ")
	}
	body.WriteString(key + "\n$ ")
	in := []byte(body.String())
	rng := rand.New(rand.NewSource(3562))
	for trial := 0; trial < 2000; trial++ {
		var out bytes.Buffer
		r := NewRedactor(&out)
		for rest := in; len(rest) > 0; {
			n := 1 + rng.Intn(min(len(rest), 24))
			_, _ = r.Write(rest[:n])
			rest = rest[n:]
			if rng.Intn(3) == 0 {
				require.NoError(t, r.Flush(rng.Intn(2) == 0))
			}
		}
		require.NoError(t, r.Close())
		got := out.String()
		for pre, v := range values {
			require.NotContains(t, got, v[:5], "trial %d: part of the %s value shown", trial, pre)
		}
		require.NotContains(t, got, key[30:40], "trial %d: part of the key shown", trial)
		require.Contains(t, got, "$ ", "trial %d: what follows is shown", trial)
	}
}

// wsRecorder is the hub's end of a socket: it records what reaches it.
type wsRecorder struct {
	mu     sync.Mutex
	frames []wsFrame
	closed websocket.StatusCode
}

type wsFrame struct {
	typ  websocket.MessageType
	data string
	at   time.Time
}

func (w *wsRecorder) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (w *wsRecorder) Write(_ context.Context, typ websocket.MessageType, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, wsFrame{typ, string(p), time.Now()})
	return nil
}
func (w *wsRecorder) Close(code websocket.StatusCode, _ string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = code
	return nil
}
func (w *wsRecorder) got() []wsFrame {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wsFrame(nil), w.frames...)
}
func (w *wsRecorder) binary() string {
	var b strings.Builder
	for _, f := range w.got() {
		if f.typ == websocket.MessageBinary {
			b.WriteString(f.data)
		}
	}
	return b.String()
}

// The terminal stream: a token split across frames is caught, settled output goes out with
// its own frame, a held tail waits no longer than its budget, the terminal's control messages
// are redacted whole, and the setting is read per frame.
func TestWSRedactTerminal(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	on := atomic.Bool{}
	on.Store(true)
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, on.Load, true)
	bin := func(s string) { require.NoError(t, w.write(websocket.MessageBinary, []byte(s))) }

	bin("out: " + tok[:10])
	assert.Equal(t, "out: ", dst.binary(), "what can't be a token goes out with its frame")
	bin(tok[10:] + "\r\n")
	assert.Equal(t, "out: "+RedactMarker+"\r\n", dst.binary(), "a token split across two frames")

	// The feed's and the terminal's JSON: each text frame redacted whole, still JSON.
	msg, _ := json.Marshal(map[string]string{"type": "terminal_input_failed", "error": "bad " + tok})
	require.NoError(t, w.write(websocket.MessageText, msg))
	f := dst.got()
	last := f[len(f)-1]
	assert.Equal(t, websocket.MessageText, last.typ)
	assert.NotContains(t, last.data, tok)
	assert.True(t, json.Valid([]byte(last.data)), last.data)

	// Latency: a frame ending in "g" (it could start "ghp_") shows its "g" within the idle
	// budget; a whole prefix is held, then redacted within the hold budget.
	n := len(dst.got())
	start := time.Now()
	bin("$ g")
	require.Eventually(t, func() bool { return strings.HasSuffix(dst.binary(), "$ g") }, 2*time.Second, 5*time.Millisecond)
	assert.Less(t, time.Since(start), wsIdleFlush+300*time.Millisecond, "the idle budget")
	assert.Greater(t, len(dst.got()), n)

	start = time.Now()
	bin("h")
	bin("p_" + tok[4:20])
	time.Sleep(wsIdleFlush + 50*time.Millisecond)
	assert.NotContains(t, dst.binary(), tok[4:9], "a value never shows on an idle flush")
	require.Eventually(t, func() bool { return strings.HasSuffix(dst.binary(), RedactMarker) }, 3*time.Second, 5*time.Millisecond)
	assert.Less(t, time.Since(start), wsHoldMax+400*time.Millisecond, "the hold budget")
	bin(tok[20:] + " done")
	assert.True(t, strings.HasSuffix(dst.binary(), "$ g"+RedactMarker+" done"), dst.binary())
	assert.NotContains(t, dst.binary(), tok[20:25], "the rest of the token is dropped")

	// The terminal ends: what was held goes out before terminal_closed.
	bin("bye s")
	require.NoError(t, w.write(websocket.MessageText, []byte(`{"type":"terminal_closed"}`)))
	f = dst.got()
	assert.Equal(t, "s", f[len(f)-2].data)
	assert.Equal(t, `{"type":"terminal_closed"}`, f[len(f)-1].data)

	// The setting off: frames pass as they are, and what was held goes out first.
	bin("a x")
	on.Store(false)
	bin(tok + "\n")
	assert.True(t, strings.HasSuffix(dst.binary(), "a x"+tok+"\n"), "off: unchanged")
	on.Store(true)
	bin(tok + "\n")
	assert.True(t, strings.HasSuffix(dst.binary(), RedactMarker+"\n"), "on again")
	w.close()
}

// /ws/coral: every frame is one JSON reply, redacted on its own.
func TestWSRedactFeed(t *testing.T) {
	tok := fake("sk-", 30, alnum)
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, func() bool { return true }, false)
	msg, _ := json.Marshal(map[string]any{"type": "coral_update", "sessions": []map[string]string{{"summary": "set OPENAI_API_KEY=" + tok}}})
	require.NoError(t, w.write(websocket.MessageText, msg))
	require.NoError(t, w.write(websocket.MessageText, []byte(`{"type":"coral_heartbeat"}`)))
	f := dst.got()
	require.Len(t, f, 2)
	assert.NotContains(t, f[0].data, tok)
	assert.Contains(t, f[0].data, RedactMarker)
	assert.True(t, json.Valid([]byte(f[0].data)))
	assert.Equal(t, `{"type":"coral_heartbeat"}`, f[1].data)
	w.close()
}

// The cost a terminal frame pays: redacting typical output (colour codes, no token) stays far
// under a frame's interval. The bound is loose for -race and slow machines; the benchmark
// gives the real figure.
func TestWSRedactFrameCost(t *testing.T) {
	frame := []byte(strings.Repeat("\x1b[32m+\x1b[0m added line of code with some text 0123456789\r\n", 64)) // ~4 KiB
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, func() bool { return true }, true)
	const n = 500
	start := time.Now()
	for i := 0; i < n; i++ {
		require.NoError(t, w.write(websocket.MessageBinary, frame))
	}
	per := time.Since(start) / n
	t.Logf("%v per %d-byte frame", per, len(frame))
	assert.Less(t, per, 5*time.Millisecond)
	assert.Equal(t, strings.Repeat(string(frame), n), dst.binary())
	w.close()
}

func BenchmarkWSRedactTerminalFrame(b *testing.B) {
	frame := []byte(strings.Repeat("\x1b[32m+\x1b[0m added line of code with some text 0123456789\r\n", 64))
	w := newWSRedact(context.Background(), &wsDiscard{}, func() bool { return true }, true)
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		_ = w.write(websocket.MessageBinary, frame)
	}
}

type wsDiscard struct{}

func (wsDiscard) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (wsDiscard) Write(context.Context, websocket.MessageType, []byte) error { return nil }
func (wsDiscard) Close(websocket.StatusCode, string) error                   { return nil }

// Through a real hub session: what /ws/terminal and /ws/coral send reaches the hub redacted
// (a token split over terminal frames included); the hub's own frames reach Coral untouched;
// the setting off turns it off.
func TestWSRedactThroughTheTunnel(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	feed, _ := json.Marshal(map[string]string{"type": "coral_update", "output": "export GITHUB_TOKEN=" + tok})
	inputs := make(chan string, 4)
	capture, _ := json.Marshal(map[string]string{"output": "$ gh auth token\r\n\x1b[1;32m" + tok + "\x1b[0m\r\n$ "})
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/live/a/capture" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(capture)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		switch {
		case strings.HasPrefix(r.URL.Path, "/ws/terminal/"):
			_ = c.Write(ctx, websocket.MessageBinary, []byte("\x1b[2J$ cat .env\r\nGITHUB_TOKEN="+tok[:15]))
			_ = c.Write(ctx, websocket.MessageBinary, []byte(tok[15:]+"\r\n$ "))
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"terminal_hello","input_acks":true}`))
		case r.URL.Path == "/ws/coral":
			_ = c.Write(ctx, websocket.MessageText, feed)
		}
		_, b, err := c.Read(ctx)
		if err == nil {
			inputs <- string(b)
		}
		c.Close(websocket.StatusNormalClosure, "")
	})
	redactOn := atomic.Bool{}
	redactOn.Store(true)
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.RedactTokens = redactOn.Load
		l.MaxMode = func() Mode { return TierInteract }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	readAll := func(path string, want int) (bin, text string) {
		t.Helper()
		c, err := sess.DialWS(ctx, path, "", nil)
		require.NoError(t, err, path)
		defer c.CloseNow()
		var bb, tb strings.Builder
		for got := 0; got < want; got++ {
			typ, b, err := c.Read(ctx)
			require.NoError(t, err, path)
			if typ == websocket.MessageBinary {
				bb.Write(b)
			} else {
				tb.Write(b)
			}
		}
		require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"type":"terminal_input","data":"echo `+tok+`"}`)))
		select {
		case in := <-inputs:
			assert.Contains(t, in, tok, "the hub's own frames reach Coral as they are")
		case <-ctx.Done():
			t.Fatal("no input reached Coral")
		}
		return bb.String(), tb.String()
	}

	bin, text := readAll("/ws/terminal/a", 3)
	assert.Equal(t, "\x1b[2J$ cat .env\r\nGITHUB_TOKEN="+RedactMarker+"\r\n$ ", bin)
	assert.Equal(t, `{"type":"terminal_hello","input_acks":true}`, text)

	_, text = readAll("/ws/coral", 1)
	assert.NotContains(t, text, tok)
	assert.Contains(t, text, RedactMarker)
	assert.True(t, json.Valid([]byte(text)))

	// #3560's capture, already shipped: a token in colour is redacted too (QA's #3562 finding).
	resp := do(t, sess, "GET", "/api/sessions/live/a/capture", "", strings.NewReader(""), nil)
	var cap map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cap))
	resp.Body.Close()
	assert.Equal(t, "$ gh auth token\r\n\x1b[1;32m"+RedactMarker+"\x1b[0m\r\n$ ", cap["output"])

	redactOn.Store(false)
	_, text = readAll("/ws/coral", 1)
	assert.Contains(t, text, tok, "the setting off: unchanged")
}

// QA's and Security's #3562 finding: a token right after a terminal colour code was missed
// (the code's final 'm' read as a word character before the prefix). Every colour form, before
// and after every kind of token, is redacted: as raw terminal bytes, as JSON writes them
// (capture and poll, "\u001b[..."), whole and split at every byte, and through the terminal
// stream's frames.
func TestRedactAfterColourCodes(t *testing.T) {
	forms := []string{
		"\x1b[1;32m", "\x1b[32m", "\x1b[38;5;208m", "\x1b[38;2;255;100;0m", "\x1b[1m", "\x1b[0m\x1b[1m",
		"\x1b[2K\r", "\x1b]0;a title\x07", "\x1b]8;;https://example.test\x1b\\", "\x1b[4:3m", "\x1b[?25l",
	}
	val := fake("", 40, alnum)
	jwt := fake("eyJ", 60, alnum+".")
	type tc struct{ in, want string }
	var cases []tc
	for _, f := range forms {
		for _, tok := range []string{fake("ghp_", 36, alnum), fake("github_pat_", 30, alnum), fake("sk-", 30, alnum), fake("AKIA", 16, upper), fake("xoxb-", 20, alnum)} {
			cases = append(cases, tc{"key: " + f + tok + "\x1b[0m end", "key: " + f + RedactMarker + "\x1b[0m end"})
		}
		cases = append(cases, tc{"x " + f + pemKey("RSA ", 2, "\r\n") + "\x1b[0m y", "x " + f + RedactMarker + "\x1b[0m y"})
		if strings.HasPrefix(f, "\x1b[") && strings.Count(f, "\x1b") == len(f)-len(strings.ReplaceAll(f, "\x1b", "")) && !strings.ContainsAny(f, "\r\a]") {
			// Colour codes between a scheme and its value (the glue takes spaces and CSI codes).
			cases = append(cases,
				tc{"Authorization: " + f + "Bearer" + f + " " + jwt + "\x1b[0m", "Authorization: " + f + "Bearer" + f + " " + RedactMarker + "\x1b[0m"},
				tc{"curl -H " + f + "Bearer " + f + val + "\x1b[0m", "curl -H " + f + "Bearer " + f + RedactMarker + "\x1b[0m"},
				// Only the Authorization glue can catch these (no Bearer).
				tc{"Authorization:" + f + " " + val + "\x1b[0m", "Authorization:" + f + " " + RedactMarker + "\x1b[0m"},
				tc{"Authorization: " + f + "Basic" + f + " " + val + "\x1b[0m", "Authorization: " + f + "Basic" + f + " " + RedactMarker + "\x1b[0m"},
			)
		}
	}
	for _, c := range cases {
		// Raw terminal bytes, whole and split at every byte.
		require.Equal(t, c.want, string(RedactTokens([]byte(c.in))), "%q", c.in)
		in := []byte(c.in)
		for split := 0; split <= len(in); split++ {
			var out bytes.Buffer
			r := NewRedactor(&out)
			_, _ = r.Write(in[:split])
			_, _ = r.Write(in[split:])
			require.NoError(t, r.Close())
			require.Equal(t, c.want, out.String(), "%q split at %d", c.in, split)
		}
		// As JSON writes it (capture, poll): ESC is "\u001b".
		j, _ := json.Marshal(map[string]string{"output": c.in})
		var back map[string]string
		got := RedactTokens(j)
		require.True(t, json.Valid(got), "%s", got)
		require.NoError(t, json.Unmarshal(got, &back))
		require.Equal(t, c.want, back["output"], "JSON %s", j)
		for split := 0; split <= len(j); split += 3 {
			var out bytes.Buffer
			r := NewRedactor(&out)
			_, _ = r.Write(j[:split])
			_, _ = r.Write(j[split:])
			require.NoError(t, r.Close())
			require.Equal(t, string(got), out.String(), "JSON %s split at %d", j, split)
		}
	}
	// Through the terminal stream, a frame boundary at every byte.
	for _, c := range cases[:12] {
		in := []byte(c.in)
		for split := 1; split < len(in); split++ {
			dst := &wsRecorder{}
			w := newWSRedact(context.Background(), dst, func() bool { return true }, true)
			require.NoError(t, w.write(websocket.MessageBinary, in[:split]))
			require.NoError(t, w.write(websocket.MessageBinary, in[split:]))
			w.close()
			require.Equal(t, c.want, dst.binary(), "%q frames split at %d", c.in, split)
		}
	}
	// Still not caught, as the docs say: an escape code inside a token.
	tok := fake("ghp_", 36, alnum)
	inside := "key: " + tok[:10] + "\x1b[1m" + tok[10:]
	assert.Equal(t, inside, string(RedactTokens([]byte(inside))))
}

// Security's #3562 SHOULD 1: a prefix's first letters are held only where a token could start,
// so typing through the hub echoes letters in the middle of a word at once.
func TestRedactHoldsOnlyWordStarts(t *testing.T) {
	held := func(typed string) string {
		var out bytes.Buffer
		r := NewRedactor(&out)
		var h strings.Builder
		for i := 0; i < len(typed); i++ {
			_, _ = r.Write([]byte{typed[i]})
			if out.Len() <= i {
				h.WriteByte(typed[i])
			} else {
				h.WriteByte('.')
			}
		}
		require.NoError(t, r.Close())
		require.Equal(t, typed, out.String())
		return h.String()
	}
	// Each '.' echoed at once; a letter is held while it could start a token.
	assert.Equal(t, "......b....a.", held("class bash ax"), "only word-initial b (bearer) and a (authorization) wait; mid-word s, a, h and x echo at once")
	assert.Equal(t, "g.", held("go"), "a word-initial 'g' could start ghp_")
	assert.Equal(t, ".....s", held("\x1b[32ms"), "after a colour code a letter starts a word")
}
