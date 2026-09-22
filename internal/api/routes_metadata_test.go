package api_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
)

// Mutation caught: omitting route registration, silently dropping a schema
// version, or serializing typed JSON values as strings.
func TestMetadataRoutesListSchemasAndDocumentValues(t *testing.T) {
	ts, catalog := newTestServer(t, nil)
	file := createFileWithContent(t, ts, catalog, "report.txt", "synthetic report")
	identity, err := catalog.EnsureDocumentIdentity(t.Context(), file.ID)
	require.NoError(t, err)
	versions, total, err := catalog.ContentVersions(t.Context(), file.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)

	schema := document.MetadataSchema{
		UID: "11111111-1111-4111-8111-111111111111", Version: 1, Name: "Research",
		Scope: document.MetadataScope{Kind: "vault", ID: "synthetic"},
		Fields: []document.MetadataField{
			{Key: "approved", Kind: document.MetadataKindBoolean,
				ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted}},
			{Key: "priority", Kind: document.MetadataKindNumber,
				ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted}},
			{Key: "title", Kind: document.MetadataKindString,
				ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted}},
			{Key: "topics", Kind: document.MetadataKindStringSet,
				ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted}},
		},
	}
	require.NoError(t, catalog.AppendMetadataSchemaVersion(t.Context(), schema))
	for _, value := range []struct {
		field string
		json  jsontext.Value
	}{
		{field: "approved", json: jsontext.Value("true")},
		{field: "priority", json: jsontext.Value("42")},
		{field: "title", json: jsontext.Value(`"Synthetic title"`)},
		{field: "topics", json: jsontext.Value(`["alpha","beta"]`)},
	} {
		_, err = catalog.PutMetadataValue(t.Context(), store.MetadataValueRecord{
			DocumentUID: identity.DocumentUID, ContentVersionID: versions[0].ID,
			SchemaUID: schema.UID, SchemaVersion: schema.Version, FieldKey: value.field,
			ValueJSON: value.json, Lane: document.MetadataLaneSourceExtracted,
			Accepted: true, Producer: "synthetic-extractor", SourcePointer: "frontmatter." + value.field,
			CapturedAt: "2026-09-22T00:00:00Z", Revision: 1,
		})
		require.NoError(t, err)
	}

	response, body := get(t, ts, "/api/v1/metadata/schemas", nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var schemas struct {
		Schemas []document.MetadataSchema `json:"schemas"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &schemas))
	require.Equal(t, []document.MetadataSchema{schema}, schemas.Schemas)

	response, body = get(t, ts, "/api/v1/documents/"+identity.DocumentUID+"/metadata", nil)
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var values struct {
		DocumentUID string `json:"document_uid"`
		Values      []struct {
			FieldKey string         `json:"field_key"`
			Value    jsontext.Value `json:"value"`
		} `json:"values"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &values))
	require.Equal(t, identity.DocumentUID, values.DocumentUID)
	require.Len(t, values.Values, 4)
	gotValues := make(map[string]jsontext.Value, len(values.Values))
	for _, value := range values.Values {
		gotValues[value.FieldKey] = value.Value
	}
	require.Equal(t, map[string]jsontext.Value{
		"approved": jsontext.Value("true"), "priority": jsontext.Value("42"),
		"title": jsontext.Value(`"Synthetic title"`), "topics": jsontext.Value(`["alpha","beta"]`),
	}, gotValues)

	response, body = get(t, ts, "/api/v1/documents/22222222-2222-4222-8222-222222222222/metadata", nil)
	require.Equal(t, http.StatusNotFound, response.StatusCode, body)
}
