package peercred

import (
	"net/http"
	"net/http/httptest"
)

func httptestRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/cloud/connect", nil)
	r.RemoteAddr = "127.0.0.1:51234"
	return r
}
