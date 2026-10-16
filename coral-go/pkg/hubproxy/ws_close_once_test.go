package hubproxy

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type closeFrame struct {
	code   int
	reason string
}

// rawClose opens a WebSocket to the hub by hand, answers the first close frame, and returns
// every close frame the hub sent until it ends the connection: a browser's view of the closing
// handshake, which a library client would hide.
func rawClose(t *testing.T, hubURL, path string) []closeFrame {
	t.Helper()
	host := strings.TrimPrefix(hubURL, "http://")
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	key := make([]byte, 16)
	rand.Read(key)
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n\r\n"
	_, err = conn.Write([]byte(req))
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	var closes []closeFrame
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return closes // the hub ended the connection
		}
		op, n := hdr[0]&0x0f, int(hdr[1]&0x7f)
		switch n {
		case 126:
			var b [2]byte
			io.ReadFull(br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			io.ReadFull(br, b[:])
			n = int(binary.BigEndian.Uint64(b[:]))
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return closes
		}
		if op != 0x8 {
			continue
		}
		f := closeFrame{}
		if len(payload) >= 2 {
			f.code, f.reason = int(binary.BigEndian.Uint16(payload)), string(payload[2:])
		}
		closes = append(closes, f)
		if len(closes) == 1 { // answer it, masked, as a browser does
			mask := []byte{1, 2, 3, 4}
			echo := append([]byte{0x88, 0x80 | byte(len(payload))}, mask...)
			for i, b := range payload {
				echo = append(echo, b^mask[i%4])
			}
			conn.Write(echo)
		}
	}
}

// #3663, coral-hub: a machine that ends a terminal with a code gets exactly one close frame to the
// browser, with the code and a normalised reason, so Chrome sees the code (it reported "Close
// received after close" and 1006 when the bridge's second pump sent another).
func TestARelayedCloseIsSentOnce(t *testing.T) {
	longMulti := strings.Repeat("é", 70) // 140 bytes: over a control frame's limit
	for name, c := range map[string]struct {
		code       websocket.StatusCode
		reason     string
		wantCode   websocket.StatusCode
		wantReason string
	}{
		"busy, the user's share": {StatusTooManyStreams, tunnel.BusyUser, StatusTooManyStreams, busyUserText},
		"busy, free text":        {StatusTooManyStreams, "<b>spoof</b> anything", StatusTooManyStreams, busyMachineText},
		"busy, unknown token":    {StatusTooManyStreams, "hub_busy:other", StatusTooManyStreams, busyMachineText},
		"busy, no reason":        {StatusTooManyStreams, "", StatusTooManyStreams, busyMachineText},
		"busy, hostile reason":   {StatusTooManyStreams, "\u202eYou have\u200b\x1b[2J" + tunnel.BusyUser, StatusTooManyStreams, busyMachineText},
		"a session's end":        {4002, "session\x07ended\u0085now", 4002, "session ended now"},
		// The machine's library won't send a close over 125 bytes and drops the connection: the
		// browser gets one close, 1011 (a byte cut of a long reason is the normaliser's test).
		"an over-long close": {4002, longMulti, websocket.StatusInternalError, "browser connection lost"},
	} {
		t.Run(name, func(t *testing.T) {
			hub, _ := wsHub(t, func(conn *websocket.Conn, r *http.Request) {
				conn.Close(c.code, c.reason)
			}, remoteKey)
			closes := rawClose(t, hub.URL, "/api/remote/a/ws/terminal/x")
			require.Len(t, closes, 1, "exactly one close frame: %+v", closes)
			assert.Equal(t, int(c.wantCode), closes[0].code)
			assert.Equal(t, c.wantReason, closes[0].reason)
			assert.True(t, utf8.ValidString(closes[0].reason))
			assert.LessOrEqual(t, len(closes[0].reason), 123)
		})
	}
}

func TestNormalizeCloseReason(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                           "plain",
		"bad\xffbyte":                     "bad�byte",
		"ctl\x00\x1b[31m":                 "ctl  [31m",
		"c1\u009bx":                       "c1 x",
		"bidi\u202eflip\u200bzw":          "bidiflipzw",
		"line\u2028para\u2029end":         "lineparaend",
		strings.Repeat("é", 70):           strings.Repeat("é", 61),
		strings.Repeat("a", 122) + "é":    strings.Repeat("a", 122),
		strings.Repeat("a", 200) + "\xff": strings.Repeat("a", 123),
	} {
		got := normalizeCloseReason(in)
		assert.Equal(t, want, got, "%q", in)
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), 123)
	}
	assert.Equal(t, busyUserText, closeReasonFor(StatusTooManyStreams, tunnel.BusyUser))
	assert.Equal(t, busyMachineText, closeReasonFor(StatusTooManyStreams, "anything else"))
	assert.Equal(t, "x y", closeReasonFor(4002, "x\ny"))
}
