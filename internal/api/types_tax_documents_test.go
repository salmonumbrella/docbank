package api

import (
	"encoding/json/v2"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTaxDocumentRequestStrict(t *testing.T) {
	for _, raw := range []string{`{"node_id":1,"role":"return","content_sha256":"invented"}`, `{"node_id":1,"role":"return","event_id":""}`, `{"node_id":1,"role":null}`} {
		var v LinkTaxDocumentRequest
		require.Error(t, json.Unmarshal([]byte(raw), &v))
	}
}
