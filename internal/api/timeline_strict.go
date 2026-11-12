package api

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
)

// timelineStrictParameters rejects extra parameters before Huma decodes the
// body, so safe-only routes cannot silently accept a disclosure request.
func timelineStrictParameters(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/api/v1/timeline/day" && r.URL.Path != "/api/v1/timeline/days") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			writeError(w, NewError(http.StatusBadRequest, "unknown_parameter", "query parameters are not supported"))
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxWorkspaceQueryRequestBytes+1))
		if err != nil {
			writeError(w, NewError(http.StatusBadRequest, "validation", "could not read request"))
			return
		}
		if len(raw) > maxWorkspaceQueryRequestBytes {
			writeError(w, NewError(http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var fields map[string]any
		if json.Unmarshal(raw, &fields) == nil {
			allowed := map[string]bool{"query": true, "timezone": true, "date_kinds": true}
			if r.URL.Path == "/api/v1/timeline/day" {
				allowed["date"] = true
				allowed["cursor"] = true
				allowed["limit"] = true
			} else {
				allowed["from"] = true
				allowed["to"] = true
			}
			for field := range fields {
				if !allowed[field] {
					writeError(w, NewError(http.StatusBadRequest, "unknown_parameter", "unknown timeline request member"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
