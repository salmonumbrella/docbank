package daemonconn

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestSignatureCreateForgedSuccessIsUnknown(t *testing.T) {
	const contractID = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name, state, contract, etag string
		status                      int
	}{
		{"unconfirmed", "creating", contractID, `"1"`, http.StatusCreated},
		{"wrong contract", "sent", "33333333-3333-4333-8333-333333333333", `"1"`, http.StatusCreated},
		{"wrong status", "sent", contractID, `"1"`, http.StatusOK},
		{"wrong etag", "sent", contractID, `"2"`, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", tc.etag)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"request_id":"11111111-1111-4111-8111-111111111111","contract_id":%q,"revision":1,"state":%q,"signers":[{"position":0,"role":"Signer","name":"Example","email":"signer@example.com","state":"awaiting"}]}`, tc.contract, tc.state)
			}))
			defer s.Close()
			c := New(s.URL, "synthetic-key")
			_, err := c.CreateSignatureRequest(t.Context(), contractID, 1, api.SignatureRequestBody{TemplateID: 7})
			require.Error(t, err)
			require.True(t, IsRetryableMutationError(err))
		})
	}
}

func TestSignatureCancelForgedSuccessIsUnknown(t *testing.T) {
	const requestID = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct{ state, etag string }{{"sent", `"3"`}, {"cancelled", `"4"`}} {
		t.Run(tc.state+tc.etag, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/api/v1/signature-requests/"+requestID+"/cancel", r.URL.Path)
				assert.Equal(t, `"2"`, r.Header.Get("If-Match"))
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", tc.etag)
				_, _ = fmt.Fprintf(w, `{"request_id":%q,"contract_id":"22222222-2222-4222-8222-222222222222","revision":3,"state":%q,"signers":[{"position":0,"role":"Signer","email":"signer@example.com","state":"awaiting"}]}`, requestID, tc.state)
			}))
			defer s.Close()
			c := New(s.URL, "synthetic-key")
			_, err := c.CancelSignatureRequest(t.Context(), requestID, 2)
			require.Error(t, err)
			require.True(t, IsRetryableMutationError(err))
		})
	}
}
