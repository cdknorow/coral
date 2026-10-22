// Package serverproof proves, to a tool that is about to send a credential, that the program answering on Coral's port is this user's Coral and not another program that took the port while
// Coral was stopped (#6103; the Windows check for the same threat asks the system who owns the port, #224 B, and does not port to macOS and Linux at a usable cost).
//
// The proof is a challenge only Coral can answer, bound to the TCP connection the credential then travels on, so a program that relays the challenge to the real Coral cannot reuse the answer:
//
//	tool  -> GET /api/system/proof?n=<32 hex nonce>&k=<operator | session&h=sha256(token) hex>     (on the new connection, before anything else)
//	Coral -> {"proof": hex(HMAC-SHA256(key, "coral-proof/2" | port | nonce | client addr:port | server addr:port))}
//
// The addresses are the ones Coral sees on that connection (the client's, and its own); the tool computes the same from its side (conn.LocalAddr is Coral's client address, conn.RemoteAddr its own).
// A relay's connection to the real Coral has other endpoints, so the proof it gets there does not match the one the tool expects on its connection.
//
// The key is derived from the credential the tool already holds (the person's command-line token, or the agent's session token), never sent, and never a master key a sandboxed tool would have
// to read: Coral knows the same derived key (the command-line token it keeps, or the session's key it stored when it minted the token).
package serverproof

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

// Path is the route Coral answers the challenge on.
const Path = "/api/system/proof"

// Kinds of credential a proof key is derived from.
const (
	KindOperator = "operator" // the person's command-line token
	KindSession  = "session"  // an agent's session token
)

// SessionTokenPrefix marks a session token minted with a proof key: a tool holding one requires the proof, a tool holding an older token (an agent started before this existed) does not.
const SessionTokenPrefix = "v2."

const (
	proofLabel = "coral-proof/2\x00"
	keyLabel   = "coral-proof-key/1\x00"
	// NonceHexLen is the length of the challenge: 16 random bytes in hex.
	NonceHexLen = 32
)

// DeriveKey is the proof key for a credential: a one-way function of it, so a proof key never gives the credential back.
func DeriveKey(kind, secret string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(keyLabel + kind))
	return m.Sum(nil)
}

// TokenHash is what a tool names its session token by on the wire: the SHA-256 Coral keeps, never the token.
func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ValidNonce reports whether s is a challenge: exactly NonceHexLen lowercase hex characters.
func ValidNonce(s string) bool {
	if len(s) != NonceHexLen {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidTokenHash reports whether s is a SHA-256 in lowercase hex.
func ValidTokenHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Endpoint writes an address as both ends compute it: IPv4 and its IPv4-mapped IPv6 form are the same address (a dual-stack listener reports one, the dialer the other).
func Endpoint(hostPort string) (string, error) {
	ap, err := netip.ParseAddrPort(hostPort)
	if err != nil {
		return "", fmt.Errorf("address %q: %w", hostPort, err)
	}
	// No zone: "fe80::1%en0" names the interface the address is on, which each end may write its own way; the address and port are what tell connections apart.
	return netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port()).String(), nil
}

// Proof is the answer for a challenge on a connection: port is the port Coral listens on (its end of the connection), client and server are the connection's two endpoints as "addr:port".
func Proof(key []byte, port, nonce, client, server string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(proofLabel))
	for _, p := range []string{port, nonce, client, server} {
		fmt.Fprintf(m, "%d:%s", len(p), p) // length-prefixed: no two field lists hash the same
	}
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compares two proofs in constant time.
func Equal(a, b string) bool {
	return hmac.Equal(bytes.TrimSpace([]byte(strings.ToLower(a))), bytes.TrimSpace([]byte(strings.ToLower(b))))
}
