package routes

import (
	"encoding/json"
	"net/http"
)

// decodeJSON decodes JSON from the request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
