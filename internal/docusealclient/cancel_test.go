package docusealclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/config"
)

func TestArchiveSubmissionReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"confirmed", 200, `{"id":10,"archived_at":"2026-01-01T00:00:00Z"}`, nil},
		{"not found", 404, `private provider detail`, ErrCancellationFailed},
		{"refused", 422, `private provider detail`, ErrCancellationFailed},
		{"server error", 500, `private provider detail`, ErrCancellationUnknown},
		{"wrong ID", 200, `{"id":11,"archived_at":"2026-01-01T00:00:00Z"}`, ErrCancellationUnknown},
		{"null archive", 200, `{"id":10,"archived_at":null}`, ErrCancellationUnknown},
		{"bad archive", 200, `{"id":10,"archived_at":"bad"}`, ErrCancellationUnknown},
		{"oversize", 200, strings.Repeat("x", maxResponse+1), ErrCancellationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, "/api/submissions/10", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := New(config.DocuSealConnection{Endpoint: server.URL, APIPath: "/api", APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
			require.NoError(t, err)
			defer c.CloseIdleConnections()
			err = c.ArchiveSubmission(t.Context(), 10)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
				require.NotContains(t, err.Error(), "private")
			}
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
