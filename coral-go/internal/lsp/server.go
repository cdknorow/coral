package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxMessage = 16 << 20
const maxHeaderLine = 8 << 10
const maxHeaders = 64

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for headers := 0; ; headers++ {
		if headers >= maxHeaders {
			return nil, fmt.Errorf("too many headers")
		}
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(line) > maxHeaderLine {
			return nil, fmt.Errorf("header line too long")
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length")
			}
		}
	}
	if length < 0 || length > maxMessage {
		return nil, fmt.Errorf("invalid Content-Length %d", length)
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func writeFrame(w io.Writer, body []byte) error {
	if len(body) > maxMessage {
		return fmt.Errorf("message too large")
	}
	_, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body))
	if err == nil {
		_, err = w.Write(body)
	}
	return err
}

type pendingResponse struct {
	result json.RawMessage
	err    error
}

type Server struct {
	def          ServerDefinition
	workspace    string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	mu           sync.Mutex
	writeMu      sync.Mutex
	pending      map[int64]chan pendingResponse
	nextID       atomic.Int64
	done         chan struct{}
	exitErr      error
	stderr       boundedBuffer
	capabilities map[string]bool
}

type boundedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b = append(b.b, p...)
	if len(b.b) > 64<<10 {
		b.b = append([]byte(nil), b.b[len(b.b)-(64<<10):]...)
	}
	return len(p), nil
}
func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.b) }

func Start(ctx context.Context, def ServerDefinition, workspace string, timeout time.Duration) (*Server, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("startup timeout must be positive")
	}
	cmd := exec.Command(def.Command, def.Args...)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	s := &Server{def: def, workspace: workspace, cmd: cmd, stdin: stdin, pending: map[int64]chan pendingResponse{}, done: make(chan struct{})}
	cmd.Stderr = &s.stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdin.Close()
		return nil, err
	}
	slog.Info("language server started", "server", def.ID, "workspace", workspace)
	go s.readLoop(stdout)
	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	uri, _ := PathToURI(workspace)
	params := map[string]any{"processId": os.Getpid(), "rootUri": uri, "capabilities": map[string]any{}}
	if def.InitializationOpts != nil {
		params["initializationOptions"] = def.InitializationOpts(workspace)
	}
	result, err := s.Request(initCtx, "initialize", params)
	if err != nil {
		s.Kill()
		slog.Warn("language server initialization failed", "server", def.ID, "error", err)
		return nil, fmt.Errorf("initialize: %w: %s", err, s.stderr.String())
	}
	var initialized struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(result, &initialized)
	s.capabilities = map[string]bool{
		"hover":      supported(initialized.Capabilities["hoverProvider"]),
		"definition": supported(initialized.Capabilities["definitionProvider"]),
		"references": supported(initialized.Capabilities["referencesProvider"]),
	}
	_ = s.Notify("initialized", map[string]any{})
	slog.Info("language server ready", "server", def.ID)
	return s, nil
}

func supported(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "false" && string(raw) != "null"
}

func (s *Server) readLoop(stdout io.ReadCloser) {
	defer func() {
		_ = s.cmd.Wait()
		close(s.done)
		slog.Info("language server exited", "server", s.def.ID)
	}()
	r := bufio.NewReader(stdout)
	for {
		body, err := readFrame(r)
		if err != nil {
			s.fail(err)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			s.fail(err)
			return
		}
		if msg.ID != nil && msg.Method == "" {
			var id int64
			if json.Unmarshal(*msg.ID, &id) == nil {
				s.mu.Lock()
				ch := s.pending[id]
				delete(s.pending, id)
				s.mu.Unlock()
				if ch != nil {
					if msg.Error != nil {
						ch <- pendingResponse{err: fmt.Errorf("rpc %d: %s", msg.Error.Code, msg.Error.Message)}
					} else {
						ch <- pendingResponse{result: msg.Result}
					}
				}
			}
		} else if msg.ID != nil { // LSP server requests are rejected, never forwarded to browsers.
			_ = s.write(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not supported"}})
		}
	}
}

func (s *Server) fail(err error) {
	s.mu.Lock()
	s.exitErr = err
	for id, ch := range s.pending {
		ch <- pendingResponse{err: err}
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

func (s *Server) write(msg rpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.done:
		return fmt.Errorf("language server exited")
	default:
	}
	return writeFrame(s.stdin, body)
}

func (s *Server) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.RequestMapped(ctx, method, params, nil)
}

// RequestMapped exposes the generated subprocess ID to a broker callback.
// The callback runs immediately after the request is written.
func (s *Server) RequestMapped(ctx context.Context, method string, params any, registered func(int64)) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	raw, _ := json.Marshal(params)
	rawIDBytes, _ := json.Marshal(id)
	rawID := json.RawMessage(rawIDBytes)
	ch := make(chan pendingResponse, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	started := time.Now()
	if err := s.write(rpcMessage{JSONRPC: "2.0", ID: &rawID, Method: method, Params: raw}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	if registered != nil {
		registered(id)
	}
	defer func() {
		slog.Debug("language server request completed", "server", s.def.ID, "method", method, "duration", time.Since(started))
	}()
	select {
	case response := <-ch:
		return response.result, response.err
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		_ = s.Notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	}
}

func (s *Server) Cancel(id int64) {
	s.mu.Lock()
	_, exists := s.pending[id]
	s.mu.Unlock()
	if exists {
		_ = s.Notify("$/cancelRequest", map[string]any{"id": id})
	}
}

func (s *Server) Alive() bool {
	s.mu.Lock()
	failed := s.exitErr != nil
	s.mu.Unlock()
	if failed {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *Server) Notify(method string, params any) error {
	raw, _ := json.Marshal(params)
	return s.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

func (s *Server) Capabilities() map[string]bool {
	out := map[string]bool{}
	for k, v := range s.capabilities {
		out[k] = v
	}
	return out
}
func (s *Server) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		slog.Warn("language server force-kill wait timed out", "server", s.def.ID)
	}
}
func (s *Server) Shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = s.Request(ctx, "shutdown", nil)
	_ = s.Notify("exit", nil)
	select {
	case <-s.done:
	case <-ctx.Done():
		s.Kill()
	}
}

var _ io.Writer = (*boundedBuffer)(nil)
