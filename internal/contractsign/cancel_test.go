package contractsign

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/docusealclient"
	"go.kenn.io/docbank/internal/store"
)

func TestCancelDestinationFenceAndDisabledNoop(t *testing.T) {
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deletes.Add(1)
		assert.Equal(t, http.MethodDelete, r.Method)
		_, _ = fmt.Fprint(w, `{"id":10,"archived_at":"2026-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "vault.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
	require.NoError(t, err)
	input := store.SignatureRequestInput{EndpointOrigin: server.URL, APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}}
	request, err := s.BeginSignatureRequest(t.Context(), contract.ContractID, contract.Revision, input)
	require.NoError(t, err)
	gate := func(_ context.Context, f func() error) error { return f() }
	connection := config.DocuSealConnection{Endpoint: server.URL, APIPath: "/other", APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	connects := 0
	svc := Service{Store: s, Admit: gate, Settle: gate, Connect: func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		connects++
		client, err := docusealclient.New(connection)
		return connection, client, err
	}}
	_, err = svc.Cancel(t.Context(), request.RequestID, request.Revision)
	require.ErrorIs(t, err, store.ErrSignatureRequestUnconfirmed)
	request, err = s.FinishSignatureCreation(t.Context(), request.RequestID, request.Revision, store.SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
	require.NoError(t, err)
	_, err = svc.Cancel(t.Context(), request.RequestID, request.Revision-1)
	require.ErrorIs(t, err, store.ErrStaleRevision)
	require.Zero(t, connects)
	_, err = svc.Cancel(t.Context(), request.RequestID, request.Revision)
	require.ErrorIs(t, err, ErrEndpointMismatch)
	require.Zero(t, deletes.Load())
	connection.APIPath = "/api"
	cancelled, err := svc.Cancel(t.Context(), request.RequestID, request.Revision)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	require.EqualValues(t, 1, deletes.Load())
	svc.Connect = func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		t.Fatal("cancelled no-op resolved connection")
		return config.DocuSealConnection{}, nil, nil
	}
	again, err := svc.Cancel(t.Context(), request.RequestID, cancelled.Revision)
	require.NoError(t, err)
	require.Equal(t, cancelled, again)
}

func TestCancelConcurrentFenceKeepsNewerState(t *testing.T) {
	var s *store.Store
	var request store.SignatureRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := s.FinishSignatureCancellation(r.Context(), request.RequestID, request.Revision, "docuseal_cancellation_failed")
		assert.NoError(t, err)
		_, _ = fmt.Fprint(w, `{"id":10,"archived_at":"2026-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	var err error
	s, err = store.Open(t.Context(), filepath.Join(t.TempDir(), "vault.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
	require.NoError(t, err)
	request, err = s.BeginSignatureRequest(t.Context(), contract.ContractID, contract.Revision, store.SignatureRequestInput{EndpointOrigin: server.URL, APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}})
	require.NoError(t, err)
	request, err = s.FinishSignatureCreation(t.Context(), request.RequestID, request.Revision, store.SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
	require.NoError(t, err)
	connection := config.DocuSealConnection{Endpoint: server.URL, APIPath: "/api", APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	gate := func(_ context.Context, f func() error) error { return f() }
	svc := Service{Store: s, Admit: gate, Settle: gate, Connect: func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		client, err := docusealclient.New(connection)
		return connection, client, err
	}}
	_, err = svc.Cancel(t.Context(), request.RequestID, request.Revision)
	require.ErrorIs(t, err, docusealclient.ErrCancellationUnknown)
	current, err := s.GetSignatureRequest(t.Context(), request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "sent", current.State)
	require.Equal(t, "docuseal_cancellation_failed", current.LastErrorCode)
}

func TestCancelProviderFailureIsStoredAndSanitized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{"not-found", http.StatusNotFound, "docuseal_cancellation_failed", docusealclient.ErrCancellationFailed},
		{"server-error", http.StatusBadGateway, "docuseal_cancellation_unknown", docusealclient.ErrCancellationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, "private synthetic provider detail")
			}))
			defer server.Close()
			s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "vault.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
			require.NoError(t, err)
			request, err := s.BeginSignatureRequest(t.Context(), contract.ContractID, contract.Revision, store.SignatureRequestInput{EndpointOrigin: server.URL, APIPath: "/api", TemplateID: "7", TemplateName: "Example", Signers: []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}})
			require.NoError(t, err)
			request, err = s.FinishSignatureCreation(t.Context(), request.RequestID, request.Revision, store.SignatureCreationResult{SubmissionID: "10", SubmitterIDs: []string{"11"}})
			require.NoError(t, err)
			connection := config.DocuSealConnection{Endpoint: server.URL, APIPath: "/api", APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
			gate := func(_ context.Context, f func() error) error { return f() }
			svc := Service{Store: s, Admit: gate, Settle: gate, Connect: func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
				client, err := docusealclient.New(connection)
				return connection, client, err
			}}
			_, err = svc.Cancel(t.Context(), request.RequestID, request.Revision)
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), "private")
			current, err := s.GetSignatureRequest(t.Context(), request.RequestID)
			require.NoError(t, err)
			require.Equal(t, "sent", current.State)
			require.Equal(t, tc.code, current.LastErrorCode)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
