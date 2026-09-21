package api

import (
	"net/http"
	"strings"
)

// packagesBrowserRequestAllowed excludes server-root preflight and raw chunk
// PUTs. Browser bytes must use the authenticated upload socket.
func packagesBrowserRequestAllowed(r *http.Request) bool {
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/packages" {
		return true
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/api/v1/packages/")
	if !ok || path == "" || (r.Method != http.MethodGet && r.URL.RawQuery != "") {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 4 {
		return false
	}
	switch parts[0] {
	case "field-catalog", "label-candidates":
		return r.Method == http.MethodGet && len(parts) == 1
	case "by-id":
		return r.Method == http.MethodGet && len(parts) >= 2 && parts[1] != "" &&
			(len(parts) == 2 || len(parts) == 3 && (parts[2] == "members" || parts[2] == "timeline-inputs"))
	case "containers":
		if len(parts) == 1 {
			return r.Method == http.MethodPost
		}
		if parts[1] == "" {
			return false
		}
		if len(parts) == 2 {
			return r.Method == http.MethodGet || r.Method == http.MethodDelete
		}
		return len(parts) == 3 && r.Method == http.MethodPost && (parts[2] == "seal" || parts[2] == "preflight")
	case "imports":
		if len(parts) == 1 {
			return r.Method == http.MethodPost
		}
		if parts[1] == "" {
			return false
		}
		if len(parts) == 2 {
			return r.Method == http.MethodGet
		}
		return len(parts) == 3 && r.Method == http.MethodPost && (parts[2] == "cancel" || parts[2] == "resume")
	}
	return false
}
