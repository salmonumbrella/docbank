package contractsign

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/docusealclient"
	"go.kenn.io/docbank/internal/store"
)

// Cancellation after the invitation starts must still persist its unknown outcome.
func TestSignatureCancellationSettlesAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var posts atomic.Int32
	var s *store.Store
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Tenant"},{"name":"Owner"}]}`))
			return
		}
		posts.Add(1)
		var body map[string]any
		if !assert.NoError(t, json.UnmarshalRead(r.Body, &body)) {
			return
		}
		assert.Len(t, body, 3)
		templateID, ok := body["template_id"].(float64)
		assert.True(t, ok && templateID == 7, "template ID must be exactly 7")
		assert.Equal(t, true, body["send_email"])
		items, ok := body["submitters"].([]any)
		if !assert.True(t, ok) || !assert.Len(t, items, 2) {
			return
		}
		for i, role := range []string{"Tenant", "Owner"} {
			signer, valid := items[i].(map[string]any)
			if !assert.True(t, valid) {
				return
			}
			assert.Len(t, signer, 4)
			assert.Equal(t, role, signer["role"])
			key, valid := signer["external_id"].(string)
			if !assert.True(t, valid) {
				return
			}
			parts := strings.Split(key, ":")
			if !assert.Len(t, parts, 3) {
				return
			}
			assert.Equal(t, "docbank", parts[0])
			assert.Equal(t, []string{"0", "1"}[i], parts[2])
			request, err := s.GetSignatureRequest(r.Context(), parts[1])
			assert.NoError(t, err)
			assert.Equal(t, "creating", request.State)
			assert.Len(t, request.Signers, 2)
		}
		cancel()
	}))
	defer provider.Close()
	connection := config.DocuSealConnection{Endpoint: provider.URL, APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	connect := func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		client, err := docusealclient.New(connection)
		return connection, client, err
	}
	path := filepath.Join(t.TempDir(), "docbank.db")
	var err error
	s, err = store.Open(t.Context(), path)
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
	signers := []store.SignatureSignerInput{{Role: "Owner", Name: "Owner", Email: "owner@example.com"}, {Role: "Tenant", Name: "Tenant", Email: "tenant@example.com"}}
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
	require.ErrorIs(t, err, store.ErrSignatureRequestOpen)
	require.EqualValues(t, 1, posts.Load())
}

// The provider resolves role names case-insensitively before sending invitations.
func TestSignatureRequestRejectsCaseCollidingTemplateRoles(t *testing.T) {
	var posts, admissions atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Signer"},{"name":"signer"}]}`))
	}))
	defer provider.Close()
	connection := config.DocuSealConnection{Endpoint: provider.URL, APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	connect := func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
		client, err := docusealclient.New(connection)
		return connection, client, err
	}
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "docbank.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	contract, err := s.CreateContract(t.Context(), store.ContractInput{Title: "Example"})
	require.NoError(t, err)
	svc := Service{Store: s, Connect: connect, Admit: func(_ context.Context, f func() error) error {
		admissions.Add(1)
		return f()
	}, Settle: func(_ context.Context, f func() error) error { return f() }}
	_, err = svc.Request(t.Context(), contract.ContractID, contract.Revision, 7, []store.SignatureSignerInput{
		{Role: "Signer", Name: "First", Email: "first@example.com"},
		{Role: "signer", Name: "Second", Email: "second@example.com"},
	})
	require.ErrorIs(t, err, store.ErrInvalidSignatureRequest)
	assert.Zero(t, admissions.Load())
	assert.Zero(t, posts.Load())
	requests, err := s.ListSignatureRequests(t.Context(), contract.ContractID, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, requests)
	after, err := s.GetContract(t.Context(), contract.ContractID, "2026-01-01")
	require.NoError(t, err)
	assert.Equal(t, contract, after)
}

func TestSignatureRequestContractRevisionExhaustion(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(strconv.FormatBool(concurrent), func(t *testing.T) {
			ctx := t.Context()
			source, err := store.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, source.Close()) })
			contract, err := source.CreateContract(ctx, store.ContractInput{Title: "Example"})
			require.NoError(t, err)
			var exported bytes.Buffer
			require.NoError(t, source.ExportMetadata(ctx, &exported))
			rev := int64(math.MaxInt64)
			if concurrent {
				rev--
			}
			lines := strings.Split(exported.String(), "\n")
			found := false
			for i, line := range lines {
				if strings.Contains(line, `"type":"contract"`) && strings.Contains(line, contract.ContractID) {
					lines[i] = strings.Replace(line, `"revision":1`, `"revision":`+strconv.FormatInt(rev, 10), 1)
					require.NotEqual(t, line, lines[i])
					found = true
				}
			}
			require.True(t, found)
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "target.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			require.NoError(t, s.ImportMetadata(ctx, strings.NewReader(strings.Join(lines, "\n"))))
			contract, err = s.GetContract(ctx, contract.ContractID, "2026-01-01")
			require.NoError(t, err)
			require.Equal(t, rev, contract.Revision)
			var snapshot bytes.Buffer
			require.NoError(t, s.ExportMetadata(ctx, &snapshot))
			var posts atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"id":7,"name":"Template","submitters":[{"name":"Signer"}]}`))
					return
				}
				posts.Add(1)
				if concurrent {
					_, e := s.UpdateContract(r.Context(), contract.ContractID, rev, store.ContractInput{Title: "Changed", Status: "draft"})
					if !assert.NoError(t, e) {
						return
					}
				}
				var body struct {
					Submitters []struct {
						ExternalID string `json:"external_id"`
					} `json:"submitters"`
				}
				if !assert.NoError(t, json.UnmarshalRead(r.Body, &body)) || !assert.Len(t, body.Submitters, 1) {
					return
				}
				_, _ = fmt.Fprintf(w, `[{"id":11,"submission_id":10,"role":"Signer","external_id":%q}]`, body.Submitters[0].ExternalID)
			}))
			defer provider.Close()
			connection := config.DocuSealConnection{Endpoint: provider.URL, APIKey: "synthetic-key", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
			svc := Service{Store: s, Connect: func(context.Context) (config.DocuSealConnection, *docusealclient.Client, error) {
				client, e := docusealclient.New(connection)
				return connection, client, e
			}, Admit: func(_ context.Context, f func() error) error { return f() }, Settle: func(_ context.Context, f func() error) error { return f() }}
			signers := []store.SignatureSignerInput{{Role: "Signer", Email: "signer@example.com"}}
			_, err = svc.Request(ctx, contract.ContractID, rev, 7, signers)
			if !concurrent {
				require.ErrorIs(t, err, store.ErrInvalidContract)
				require.Zero(t, posts.Load())
				return
			}
			var creation *CreationError
			require.ErrorAs(t, err, &creation)
			require.ErrorIs(t, err, docusealclient.ErrCreationUnknown)
			request, err := s.GetSignatureRequest(ctx, creation.RequestID)
			require.NoError(t, err)
			require.Equal(t, "creating", request.State)
			require.Nil(t, request.ProviderSubmissionID)
			require.Nil(t, request.Signers[0].ProviderSubmitterID)
			_, err = svc.Request(ctx, contract.ContractID, int64(math.MaxInt64), 7, signers)
			require.ErrorIs(t, err, store.ErrSignatureRequestOpen)
			require.EqualValues(t, 1, posts.Load())
		})
	}
}
