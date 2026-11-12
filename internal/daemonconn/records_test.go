package daemonconn

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"testing"
)

func TestRecordRequestPreservesDynamicFields(t *testing.T) {
	request := api.RecordCreateRequest{TypeID: "contract", Title: "Example", Fields: map[string]any{"term": map[string]any{"end": "2027-01-01"}}}
	data, err := json.Marshal(request)
	require.NoError(t, err)
	require.JSONEq(t, `{"type_id":"contract","title":"Example","fields":{"term":{"end":"2027-01-01"}}}`, string(data))
}
