package routes

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestUrlParam_DecodesSpecialChars(t *testing.T) {
	tests := []struct {
		name     string
		rawPath  string
		want     string
	}{
		{"apostrophe encoded", "/api/board/The%20WorkerB%27s/messages", "The WorkerB's"},
		{"apostrophe raw", "/api/board/The%20WorkerB's/messages", "The WorkerB's"},
		{"exclamation encoded", "/api/board/The%20CLI-Team%21/messages", "The CLI-Team!"},
		{"exclamation raw", "/api/board/The%20CLI-Team!/messages", "The CLI-Team!"},
		{"plain name", "/api/board/WorkerBS/messages", "WorkerBS"},
		{"spaces only", "/api/board/My%20Team/messages", "My Team"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			var got string
			r.Get("/api/board/{project}/messages", func(w http.ResponseWriter, r *http.Request) {
				got = urlParam(r, "project")
			})

			req, _ := http.NewRequest("GET", tt.rawPath, nil)
			w := &discardResponseWriter{}
			r.ServeHTTP(w, req)

			if got != tt.want {
				t.Errorf("urlParam() = %q, want %q", got, tt.want)
			}
		})
	}
}

type discardResponseWriter struct {
	code int
}

func (d *discardResponseWriter) Header() http.Header        { return http.Header{} }
func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponseWriter) WriteHeader(code int)        { d.code = code }
