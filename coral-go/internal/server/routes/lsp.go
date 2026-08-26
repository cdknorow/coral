package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/lsp"
	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type languageCapabilities struct {
	Language     string          `json:"language,omitempty"`
	Server       string          `json:"server,omitempty"`
	Status       lsp.Status      `json:"status"`
	Capabilities map[string]bool `json:"capabilities"`
	Message      string          `json:"message,omitempty"`
	Workspace    string          `json:"workspace,omitempty"`
}

const (
	maxBrowserLSPMessage = 8 << 20
	maxOpenDocument      = 5 << 20
	maxOutstandingLSP    = 64
	maxNavigationResults = 1000
)

func (h *SessionsHandler) resolveLSP(r *http.Request) (lsp.ServerDefinition, string, string, string, error) {
	fp := r.URL.Query().Get("filepath")
	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsRune(fp, 0) {
		return lsp.ServerDefinition{}, "", "", "", fmt.Errorf("filepath is required")
	}
	worktree := h.resolveGitRoot(r.Context(), chi.URLParam(r, "name"), "", r.URL.Query().Get("session_id"))
	if worktree == "" {
		return lsp.ServerDefinition{}, "", "", "", fmt.Errorf("could not determine working directory")
	}
	navigationRoot, err := lsp.Canonical(worktree)
	if err != nil {
		return lsp.ServerDefinition{}, "", "", "", err
	}
	file, err := lsp.ResolveFile(worktree, fp)
	if err != nil {
		return lsp.ServerDefinition{}, "", "", "", err
	}
	def, ok := h.lspRegistry.ForFile(file)
	if !ok {
		return lsp.ServerDefinition{}, "", file, navigationRoot, nil
	}
	workspace, err := lsp.ResolveWorkspace(worktree, file, def.RootMarkers)
	return def, workspace, file, navigationRoot, err
}

// LanguageCapabilities reports support without starting a subprocess.
func (h *SessionsHandler) LanguageCapabilities(w http.ResponseWriter, r *http.Request) {
	def, _, _, navigationRoot, err := h.resolveLSP(r)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	if def.ID == "" {
		writeJSON(w, http.StatusOK, languageCapabilities{Status: lsp.StatusUnsupported, Capabilities: map[string]bool{}})
		return
	}
	status := h.lspManager.Availability(def)
	response := languageCapabilities{Language: def.Language(), Server: def.ID, Status: status, Workspace: navigationRoot, Capabilities: map[string]bool{}}
	if status == lsp.StatusUnavailable {
		response.Message = def.Command + " is not installed"
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// WSLSP brokers an allowlisted subset of LSP to one resolved workspace.
func (h *SessionsHandler) WSLSP(w http.ResponseWriter, r *http.Request) {
	def, workspace, _, navigationRoot, err := h.resolveLSP(r)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	if def.ID == "" {
		errBadRequest(w, "file type is unsupported")
		return
	}
	client := fmt.Sprintf("%p-%d", r, time.Now().UnixNano())
	instance, status, err := h.lspManager.Connect(r.Context(), def, workspace, client)
	if err != nil {
		code := http.StatusServiceUnavailable
		writeJSON(w, code, languageCapabilities{Language: def.Language(), Server: def.ID, Status: status, Capabilities: map[string]bool{}, Message: err.Error()})
		return
	}
	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		h.lspManager.Disconnect(instance, client)
		return
	}
	defer func() { h.lspManager.Disconnect(instance, client); conn.CloseNow() }()
	conn.SetReadLimit(maxBrowserLSPMessage)
	ctx, cancelConn := context.WithCancel(r.Context())
	defer cancelConn()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsjson.Write(ctx, conn, value)
	}
	_ = write(map[string]any{"type": "status", "language": def.Language(), "server": def.ID, "status": "ready", "workspace": navigationRoot, "capabilities": instance.Server.Capabilities()})

	var pendingMu sync.Mutex
	type pendingRequest struct {
		serverID  int64
		cancelled bool
	}
	pending := make(map[string]pendingRequest)
	defer func() {
		pendingMu.Lock()
		for _, request := range pending {
			if request.serverID > 0 {
				instance.Server.Cancel(request.serverID)
			}
		}
		pending = nil
		pendingMu.Unlock()
	}()
	for {
		var envelope lsp.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			return
		}
		if envelope.Type != "request" || !lsp.MethodAllowed(envelope.Method) {
			h.writeLSPError(write, envelope.ID, "method_not_allowed", "LSP method is not allowed")
			continue
		}
		if envelope.Method == "$/cancelRequest" {
			var cancellation struct {
				ID any `json:"id"`
			}
			if json.Unmarshal(envelope.Params, &cancellation) != nil {
				h.writeLSPError(write, envelope.ID, "invalid_params", "invalid cancellation id")
				continue
			}
			pendingMu.Lock()
			key := browserRequestKey(cancellation.ID)
			request, exists := pending[key]
			if exists {
				if request.serverID == 0 {
					request.cancelled = true
					pending[key] = request
				} else {
					delete(pending, key)
				}
			}
			pendingMu.Unlock()
			if exists && request.serverID > 0 {
				instance.Server.Cancel(request.serverID)
			}
			continue
		}
		if err := validateRequestURI(instance.Workspace, envelope.Params); err != nil {
			h.writeLSPError(write, envelope.ID, "invalid_path", err.Error())
			continue
		}
		normalizedParams, err := normalizeDocumentURI(instance.Workspace, envelope.Params)
		if err != nil {
			h.writeLSPError(write, envelope.ID, "invalid_path", err.Error())
			continue
		}
		envelope.Params = normalizedParams
		uri := documentURI(envelope.Params)
		if envelope.Method == "textDocument/didOpen" || envelope.Method == "textDocument/didChange" {
			if documentContentSize(envelope.Params) > maxOpenDocument {
				h.writeLSPError(write, envelope.ID, "document_too_large", "document exceeds the 5 MiB language-intelligence limit")
				continue
			}
		}
		if envelope.Method == "textDocument/didOpen" {
			if err := h.lspManager.Lease(instance, uri, client); err != nil {
				h.writeLSPError(write, envelope.ID, "document_conflict", err.Error())
				continue
			}
		}
		if strings.HasPrefix(envelope.Method, "textDocument/") && envelope.Method != "textDocument/didOpen" &&
			uri != "" && !h.lspManager.Owns(instance, uri, client) {
			h.writeLSPError(write, envelope.ID, "document_conflict", "client does not own the document lease")
			continue
		}
		var params any
		if len(envelope.Params) > 0 {
			if json.Unmarshal(envelope.Params, &params) != nil {
				h.writeLSPError(write, envelope.ID, "invalid_params", "invalid params")
				continue
			}
		}
		if lsp.IsNotification(envelope.Method) {
			if err := instance.Server.Notify(envelope.Method, params); err != nil {
				h.writeLSPError(write, envelope.ID, "server_unavailable", err.Error())
			} else if envelope.Method == "textDocument/didClose" {
				h.lspManager.Release(instance, uri, client)
			}
			continue
		}
		browserKey := browserRequestKey(envelope.ID)
		pendingMu.Lock()
		if browserKey == "" {
			pendingMu.Unlock()
			h.writeLSPError(write, envelope.ID, "invalid_request_id", "request id is required")
			continue
		}
		if len(pending) >= maxOutstandingLSP {
			pendingMu.Unlock()
			h.writeLSPError(write, envelope.ID, "too_many_requests", "too many outstanding language-server requests")
			continue
		}
		if _, duplicate := pending[browserKey]; duplicate {
			pendingMu.Unlock()
			h.writeLSPError(write, envelope.ID, "duplicate_request_id", "request id is already outstanding")
			continue
		}
		pending[browserKey] = pendingRequest{}
		pendingMu.Unlock()
		go func(envelope lsp.Envelope, params any, browserKey string) {
			requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			result, requestErr := instance.Server.RequestMapped(requestCtx, envelope.Method, params, func(serverID int64) {
				pendingMu.Lock()
				request, exists := pending[browserKey]
				cancelled := exists && request.cancelled
				if exists {
					if cancelled {
						delete(pending, browserKey)
					} else {
						request.serverID = serverID
						pending[browserKey] = request
					}
				}
				pendingMu.Unlock()
				if cancelled {
					instance.Server.Cancel(serverID)
				}
			})
			pendingMu.Lock()
			if pending != nil {
				delete(pending, browserKey)
			}
			pendingMu.Unlock()
			if requestErr != nil {
				h.writeLSPError(write, envelope.ID, "server_error", requestErr.Error())
				return
			}
			if err := validateResultLocations(instance.Workspace, result); err != nil {
				h.writeLSPError(write, envelope.ID, "outside_workspace", err.Error())
				return
			}
			_ = write(lsp.Envelope{Type: "response", ID: envelope.ID, Result: result})
		}(envelope, params, browserKey)
	}
}

func (h *SessionsHandler) writeLSPError(write func(any) error, id any, code, message string) {
	_ = write(lsp.Envelope{Type: "error", ID: id, Error: &lsp.BrowserError{Code: code, Message: message}})
}

func browserRequestKey(id any) string {
	if id == nil {
		return ""
	}
	encoded, err := json.Marshal(id)
	if err != nil || string(encoded) == "null" {
		return ""
	}
	return string(encoded)
}

func documentContentSize(params json.RawMessage) int {
	var value struct {
		TextDocument struct {
			Text string `json:"text"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}
	if json.Unmarshal(params, &value) != nil {
		return 0
	}
	size := len(value.TextDocument.Text)
	for _, change := range value.ContentChanges {
		if len(change.Text) > size {
			size = len(change.Text)
		}
	}
	return size
}
func documentURI(params json.RawMessage) string {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(params, &p)
	return p.TextDocument.URI
}
func validateRequestURI(workspace string, params json.RawMessage) error {
	uri := documentURI(params)
	if uri == "" {
		return nil
	}
	_, err := lsp.URIToPath(workspace, uri)
	return err
}

func normalizeDocumentURI(workspace string, params json.RawMessage) (json.RawMessage, error) {
	uri := documentURI(params)
	if uri == "" {
		return params, nil
	}
	path, err := lsp.URIToPath(workspace, uri)
	if err != nil {
		return nil, err
	}
	normalized, err := lsp.PathToURI(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(params, &value); err != nil {
		return nil, err
	}
	document, ok := value["textDocument"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid textDocument")
	}
	document["uri"] = normalized
	return json.Marshal(value)
}
func validateResultLocations(workspace string, result json.RawMessage) error {
	locations := 0
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			if uri, ok := x["uri"].(string); ok {
				locations++
				if locations > maxNavigationResults {
					return fmt.Errorf("too many navigation results")
				}
				if _, err := lsp.URIToPath(workspace, uri); err != nil {
					return err
				}
			}
			if uri, ok := x["targetUri"].(string); ok {
				locations++
				if locations > maxNavigationResults {
					return fmt.Errorf("too many navigation results")
				}
				if _, err := lsp.URIToPath(workspace, uri); err != nil {
					return err
				}
			}
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var decoded any
	if len(result) == 0 {
		return nil
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return err
	}
	return walk(decoded)
}
