package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
)

// cliSign does what `coral cloud` does before a request: the hello against h
// (as a loopback command-line caller), then the X-Coral-CLI-Auth value that
// signs this one request with token. Each value is good for one request.
func cliSign(t *testing.T, h http.Handler, token, method, path, body string, authorization ...string) string {
	t.Helper()
	cn, err := cloud.NewCLINonce()
	require.NoError(t, err)
	req := httptest.NewRequest("POST", cloud.CLIHelloPath, strings.NewReader(`{"nonce":"`+cn+`"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8420"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var hello struct {
		Nonce string `json:"nonce"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hello))
	auth := ""
	if len(authorization) > 0 {
		auth = authorization[0]
	}
	sig, err := cloud.SignCLIRequest(token, cn, hello.Nonce, method, path, []byte(body), auth)
	require.NoError(t, err)
	return sig
}
