package apiclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"uuid"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"github.com/stretchr/testify/require"
)

type responseTransport struct {
	runtime.APIClient

	response *runtime.Response
}

func (t responseTransport) ExecuteRequest(context.Context, *http.Request, string) (*runtime.Response, error) {
	return t.response, nil
}

func TestGeneratedErrorDecoding(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		malformed  bool
	}{
		{"problem", `{"status":503,"code":"unavailable","detail":"synthetic failure"}`, false},
		{"empty", "", false},
		{"malformed", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builder, err := runtime.NewAPIClient("http://example.invalid")
			require.NoError(t, err)
			client := NewClient(responseTransport{APIClient: builder, response: &runtime.Response{
				StatusCode: http.StatusServiceUnavailable,
				Headers:    http.Header{"Content-Type": {"application/problem+json"}},
				Content:    []byte(tc.body),
			}})
			result, err := client.VaultInfo(t.Context())
			require.Nil(t, result)
			require.Error(t, err)
			if tc.malformed {
				decode, ok := errors.AsType[*runtime.ResponseDecodeError](err)
				require.True(t, ok)
				require.Equal(t, http.StatusServiceUnavailable, decode.StatusCode)
				require.Equal(t, []byte(tc.body), decode.Body)
			} else {
				problem, ok := errors.AsType[*runtime.ClientAPIError](err)
				require.True(t, ok)
				require.Equal(t, http.StatusServiceUnavailable, problem.StatusCode())
				if tc.body != "" {
					require.Contains(t, err.Error(), "synthetic failure")
				}
			}
		})
	}
}

// Mutation caught: generated metadata clients decoding JSON values as base64
// byte slices rather than preserving their declared scalar and array shapes.
func TestGeneratedMetadataValueDecoding(t *testing.T) {
	builder, err := runtime.NewAPIClient("http://example.invalid")
	require.NoError(t, err)
	client := NewClient(responseTransport{APIClient: builder, response: &runtime.Response{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Content: []byte(`{"document_uid":"11111111-1111-4111-8111-111111111111","values":[{
			"document_uid":"11111111-1111-4111-8111-111111111111","content_version_id":"22222222-2222-4222-8222-222222222222",
			"schema_uid":"33333333-3333-4333-8333-333333333333","schema_version":1,"field_key":"approved","value":true,
			"lane":"source_extracted","accepted":true,"producer":"synthetic","source_pointer":"frontmatter.approved",
			"captured_at":"2026-09-22T00:00:00Z","revision":1},{
			"document_uid":"11111111-1111-4111-8111-111111111111","content_version_id":"22222222-2222-4222-8222-222222222222",
			"schema_uid":"33333333-3333-4333-8333-333333333333","schema_version":1,"field_key":"priority","value":42,
			"lane":"source_extracted","accepted":true,"producer":"synthetic","source_pointer":"frontmatter.priority",
			"captured_at":"2026-09-22T00:00:00Z","revision":1},{
			"document_uid":"11111111-1111-4111-8111-111111111111","content_version_id":"22222222-2222-4222-8222-222222222222",
			"schema_uid":"33333333-3333-4333-8333-333333333333","schema_version":1,"field_key":"title","value":"Synthetic title",
			"lane":"source_extracted","accepted":true,"producer":"synthetic","source_pointer":"frontmatter.title",
			"captured_at":"2026-09-22T00:00:00Z","revision":1},{
			"document_uid":"11111111-1111-4111-8111-111111111111","content_version_id":"22222222-2222-4222-8222-222222222222",
			"schema_uid":"33333333-3333-4333-8333-333333333333","schema_version":1,"field_key":"topics","value":["alpha","beta"],
			"lane":"source_extracted","accepted":true,"producer":"synthetic","source_pointer":"frontmatter.topics",
			"captured_at":"2026-09-22T00:00:00Z","revision":1}]}`),
	}})
	documentID, err := uuid.Parse("11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	result, err := client.GetDocumentMetadata(t.Context(), &GetDocumentMetadataRequestOptions{
		PathParams: &GetDocumentMetadataPath{ID: documentID},
	})
	require.NoError(t, err)
	require.Len(t, result.Values, 4)
	gotValues := make(map[string]string, len(result.Values))
	for _, value := range result.Values {
		gotValues[value.FieldKey] = string(value.Value)
	}
	require.Equal(t, map[string]string{
		"approved": "true", "priority": "42",
		"title": `"Synthetic title"`, "topics": `["alpha","beta"]`,
	}, gotValues)
}
