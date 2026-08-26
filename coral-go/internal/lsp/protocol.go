package lsp

import (
	"encoding/json"
	"fmt"
)

// Envelope is the deliberately small browser-facing protocol.
type Envelope struct {
	Type         string          `json:"type"`
	ID           any             `json:"id,omitempty"`
	Method       string          `json:"method,omitempty"`
	Params       json.RawMessage `json:"params,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        *BrowserError   `json:"error,omitempty"`
	Status       string          `json:"status,omitempty"`
	Language     string          `json:"language,omitempty"`
	Server       string          `json:"server,omitempty"`
	Workspace    string          `json:"workspace,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

type BrowserError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var allowedMethods = map[string]bool{
	"textDocument/didOpen": true, "textDocument/didChange": true,
	"textDocument/didSave": true, "textDocument/didClose": true,
	"textDocument/hover": true, "textDocument/definition": true,
	"textDocument/references": true, "$/cancelRequest": true,
}

func MethodAllowed(method string) bool { return allowedMethods[method] }

func IsNotification(method string) bool {
	switch method {
	case "textDocument/didOpen", "textDocument/didChange", "textDocument/didSave",
		"textDocument/didClose", "$/cancelRequest":
		return true
	default:
		return false
	}
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func normalizeID(id any) (json.RawMessage, error) {
	b, err := json.Marshal(id)
	if err != nil || string(b) == "null" {
		return nil, fmt.Errorf("invalid request id")
	}
	return b, nil
}
