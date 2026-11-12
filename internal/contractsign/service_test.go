package contractsign

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/docusealclient"
	"go.kenn.io/docbank/internal/store"
)

// Cancellation after the invitation starts must still persist its unknown outcome.
func TestCancellationSettlesAndReopenBlocksSecondInvitation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Signer"}]}`))
			return
		}
		posts.Add(1)
		cancel()
	}))
	defer provider.Close()
	connection := config.DocuSealConnection{Endpoint: provider.URL, APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	connect := func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		client, err := docusealclient.New(connection)
		return connection, client, err
	}
	path := filepath.Join(t.TempDir(), "docbank.db")
	s, err := store.Open(t.Context(), path)
	require.NoError(t, err)
	contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
	require.NoError(t, err)
	admit := func(_ context.Context, f func() error) error { return f() }
	settled := false
	settle := func(ctx context.Context, f func() error) error {
		settled = true
		require.NoError(t, ctx.Err())
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		return f()
	}
	svc := Service{Store: s, Connect: connect, Admit: admit, Settle: settle}
	signers := []store.SignatureSignerInput{{Role: "Signer", Name: "Example", Email: "signer@example.com"}}
	_, err = svc.Request(ctx, contract.ContractID, contract.Revision, 7, signers)
	var creation *CreationError
	require.ErrorAs(t, err, &creation)
	require.ErrorIs(t, err, docusealclient.ErrCreationUnknown)
	require.True(t, settled)
	require.NoError(t, s.Close())
	s, err = store.Open(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	request, err := s.GetSignatureRequest(t.Context(), creation.RequestID)
	require.NoError(t, err)
	require.Equal(t, "creating", request.State)
	require.Equal(t, "docuseal_creation_unknown", request.LastErrorCode)
	svc.Store = s
	_, err = svc.Request(t.Context(), contract.ContractID, contract.Revision, 7, signers)
	require.True(t, errors.Is(err, store.ErrSignatureRequestOpen))
	require.EqualValues(t, 1, posts.Load())
}
