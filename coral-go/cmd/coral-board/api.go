package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/clitext"
	"github.com/subgentic/coral/internal/hooks"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// readServer reads a reply from the Coral server, cleaned of terminal control unless --raw.
func readServer(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(r)
	if rawOutput {
		return b, err
	}
	return clitext.CleanJSON(b), err
}

// endpointFromEnv resolves the client endpoint without allowing a legacy port
// variable to overwrite an explicit URL. CORAL_HOST/CORAL_PORT are only the
// fallback for launch environments that do not provide CORAL_URL.
func endpointFromEnv(explicitURL, host, port string) string {
	if explicitURL = strings.TrimRight(strings.TrimSpace(explicitURL), "/"); explicitURL != "" {
		return explicitURL
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	} else if host == "0.0.0.0" || host == "::" {
		// Wildcard bind addresses are not dialable client endpoints.
		host = "127.0.0.1"
	}
	port = strings.TrimSpace(port)
	if port == "" {
		port = "8420"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func apiCall(method, path string, body any) (map[string]any, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, serverURL+"/api/board"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", redactedServerURL(), err)
	}
	defer resp.Body.Close()

	respData, _ := readServer(resp.Body)
	var result map[string]any
	json.Unmarshal(respData, &result)
	return result, resp.StatusCode, nil
}

// serverError turns a non-2xx reply into an error naming the status and the server's error code
// or message (never the token), or nil for 2xx (#3589). A silent 401 on a key-protected Coral
// otherwise looks like success and loses the message. For a 401 from a non-loopback CORAL_URL it
// adds a hint, since an agent should reach its own Coral on the loopback address.
func serverError(status int, result map[string]any) error {
	if status >= 200 && status < 300 {
		return nil
	}
	detail, _ := result["message"].(string)
	if detail == "" {
		if code, _ := result["code"].(string); code != "" {
			detail = code
		} else if e, _ := result["error"].(string); e != "" {
			detail = e
		} else {
			detail = http.StatusText(status)
		}
	}
	out := fmt.Sprintf("Error: Coral at %s answered %d: %s", redactedServerURL(), status, detail)
	if status == http.StatusUnauthorized && !isLoopbackServer() {
		out += "\nthis Coral needs auth from non-local addresses; agents should use the loopback address"
	}
	return errors.New(out)
}

// redactedServerURL is serverURL with any userinfo (user:password@) hidden, so an error message
// never carries a credential embedded in CORAL_URL (#3589).
func redactedServerURL() string { return hooks.RedactedURL(serverURL) }

// isLoopbackServer reports whether serverURL points at this machine's loopback interface. It uses
// net.ParseIP so a hostname like "127.0.0.1.example.com" (a real DNS name, not an IP) is not
// treated as loopback (#3589).
func isLoopbackServer() bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// boardClient is the client for reads: the same 10 s as apiCall. http.Get has no timeout at all, so read and check waited for ever on a server
// that accepted the connection and never answered.
var boardClient = &http.Client{Timeout: 10 * time.Second}

// getBoard does a GET to a /api/board path and returns the cleaned body, erroring on a transport
// failure or a non-2xx status (#3589), so a refused read is never printed as "no messages".
func getBoard(path string) ([]byte, error) {
	resp, err := boardClient.Get(serverURL + "/api/board" + path)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Coral server at %s: %v", redactedServerURL(), err)
	}
	defer resp.Body.Close()
	data, _ := readServer(resp.Body)
	var result map[string]any
	json.Unmarshal(data, &result) // a list reply leaves result empty, but then the status is 2xx
	if e := serverError(resp.StatusCode, result); e != nil {
		return nil, e
	}
	return data, nil
}

// apiCallRaw performs an HTTP request and returns the response body, status code, and error.
// Unlike apiCall, it preserves the status code for conflict/not-found handling.
func apiCallRaw(method, path string, body any) ([]byte, int, error) {
	return apiCallRawTimeout(method, path, body, 10*time.Second)
}

// apiCallRawTimeout is apiCallRaw with its own timeout (a message relayed to another
// Coral can take up to about 30 s: the hub waits up to 20 s for the target, #3489).
func apiCallRawTimeout(method, path string, body any, timeout time.Duration) ([]byte, int, error) {
	client := &http.Client{Timeout: timeout}
	var lastErr error
	for i, endpoint := range requestURLs(serverURL + "/api/board" + path) {
		if i > 0 && !retryLoopbackTransport(method, lastErr) {
			break
		}
		var bodyReader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, endpoint, bodyReader)
		if err != nil {
			return nil, 0, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		respData, _ := readServer(resp.Body)
		return respData, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", redactedServerURL(), lastErr)
}

// retryLoopbackTransport permits fallback only for idempotent GET requests and
// non-permission transport failures. Permission errors remain actionable
// sandbox-policy errors, and mutating requests are never replayed after an
// ambiguous transmission.
func retryLoopbackTransport(method string, err error) bool {
	if !strings.EqualFold(method, http.MethodGet) {
		return false
	}
	return !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES)
}

// requestURLs adds a narrowly scoped IPv4 retry for localhost. Managed macOS
// sandboxes may reject IPv6 loopback (::1) while allowing the IPv4 listener;
// explicit hosts and HTTP-level failures are left unchanged.
func requestURLs(endpoint string) []string {
	urls := []string{endpoint}
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Hostname(), "localhost") {
		return urls
	}
	u.Host = net.JoinHostPort("127.0.0.1", u.Port())
	return append(urls, u.String())
}

// failFromServer prints a request's failure (the server's error, else its status and body) and exits 1.
func failFromServer(data []byte, status int, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var errResp map[string]string
	if json.Unmarshal(data, &errResp) == nil && errResp["error"] != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
	} else {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d: %s\n", status, string(data))
	}
	os.Exit(1)
}

// failFromRefusal is failFromServer for a refusal whose reply carries numbers and flags beside its words (the 413
// and 409 of the bounds, #4646): its message, else its error, else the status and body.
func failFromRefusal(data []byte, status int, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var errResp map[string]any
	if json.Unmarshal(data, &errResp) == nil {
		if text, _ := errResp["message"].(string); text != "" {
			fmt.Fprintf(os.Stderr, "Error: %s\n", text)
			os.Exit(1)
		}
		if text, _ := errResp["error"].(string); text != "" {
			fmt.Fprintf(os.Stderr, "Error: %s\n", text)
			os.Exit(1)
		}
	}
	fmt.Fprintf(os.Stderr, "Error: HTTP %d: %s\n", status, string(data))
	os.Exit(1)
}
