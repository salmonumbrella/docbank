package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDocumentFactsBrowserAllowlist(t *testing.T) {
	t.Parallel()
	base := "/api/v1/versions/00000000-0000-4000-8000-000000000000/dates"
	for _, suffix := range []string{"", "?disclosure=safe", "?disclosure=full"} {
		if !webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodGet, base+suffix, nil)) {
			t.Errorf("denied %q", suffix)
		}
	}
	for _, suffix := range []string{"?disclosure=full&disclosure=safe", "?other=1", "?disclosure=bad"} {
		if webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodGet, base+suffix, nil)) {
			t.Errorf("allowed %q", suffix)
		}
	}
}

func TestDocumentPeopleWriteBrowserAllowlist(t *testing.T) {
	t.Parallel()
	base := "/api/v1/versions/00000000-0000-4000-8000-000000000000/people/assertions"
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodPut, base, true},
		{http.MethodPost, base, false},
		{http.MethodPut, base + "?disclosure=full", false},
		{http.MethodPut, base + "/extra", false},
		{http.MethodPut, "/api/v1/versions/no/people/assertions", false},
	} {
		if got := webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)); got != tc.allowed {
			t.Errorf("%s %s: got %v", tc.method, tc.path, got)
		}
	}
}
