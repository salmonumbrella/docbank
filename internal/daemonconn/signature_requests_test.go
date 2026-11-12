package daemonconn

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestSignatureCreateForgedSuccessIsUnknown(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"request_id":"11111111-1111-4111-8111-111111111111","state":"sent"}`)
	}))
	defer s.Close()
	c := New(s.URL, "synthetic-key")
	_, err := c.CreateSignatureRequest(t.Context(), "22222222-2222-4222-8222-222222222222", 1, api.SignatureRequestBody{TemplateID: 7})
	require.Error(t, err)
	require.True(t, IsRetryableMutationError(err))
}
