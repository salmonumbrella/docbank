package api_test

import (
	"encoding/json/v2"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/store"
)

func TestSignatureHistorySurvivesPhysicalBackupRestore(t *testing.T) { //nolint:paralleltest // backup restore locks the per-user target registry
	for _, driver := range collectionBackupDrivers() {
		t.Run(driver.Name(), func(t *testing.T) {
			ts, live := newCollectionBackupServer(t, driver, filepath.Join(t.TempDir(), "repository"))
			ctx := t.Context()
			c, err := live.CreateContract(ctx, store.ContractInput{Title: "Example"})
			require.NoError(t, err)
			req, err := live.BeginSignatureRequest(ctx, c.ContractID, c.Revision, store.SignatureRequestInput{EndpointOrigin: "https://sign.example.com", TemplateID: "7", TemplateName: "Agreement", Signers: []store.SignatureSignerInput{{Role: "Signer", Name: "Example", Email: "signer@example.com"}}})
			require.NoError(t, err)
			resp, body := do(t, ts, http.MethodPost, "/api/v1/backup/init", nil, map[string]any{})
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			resp, body = do(t, ts, http.MethodPost, "/api/v1/backup/snapshots", nil, map[string]any{"tag": "signature-history", "jobs": 1})
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			var snapshot api.BackupSnapshot
			require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
			target := filepath.Join(t.TempDir(), "restored")
			resp, body = do(t, ts, http.MethodPost, "/api/v1/backup/restore/stream", nil, map[string]any{"target": target, "snapshot_id": snapshot.ID, "jobs": 1})
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			events := decodeBackupRestoreEvents(t, body)
			require.NotEmpty(t, events)
			require.Equal(t, "result", events[len(events)-1].Type)
			restored, err := store.Open(ctx, filepath.Join(target, "docbank.db"), driver)
			require.NoError(t, err)
			defer func() { require.NoError(t, restored.Close()) }()
			got, err := restored.GetSignatureRequest(ctx, req.RequestID)
			require.NoError(t, err)
			require.Equal(t, req, got)
		})
	}
}
