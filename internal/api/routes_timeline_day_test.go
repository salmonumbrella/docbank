package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTimelineBrowserAllowlist(t *testing.T) {
	for _, path := range []string{"/api/v1/timeline/day", "/api/v1/timeline/days"} {
		require.True(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodPost, path, nil)))
		require.False(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodPost, path+"?disclosure=full", nil)))
		require.False(t, webSessionRequestAllowed(httptest.NewRequest(http.MethodGet, path, nil)))
	}
}
