package store

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
)

func TestMetadataAuthorityRoundTripsThroughMetadataJSONL(t *testing.T) {
	source := newTestStore(t)
	file, err := source.CreateFile(t.Context(), source.RootID(), "source.txt", fakeHash("d1"), 9, "text/plain",
		BlobPhysical{Encoding: "raw", StoredBytes: 9, PackEligible: true, Created: true})
	require.NoError(t, err)
	identity, err := source.EnsureDocumentIdentity(t.Context(), file.ID)
	require.NoError(t, err)
	versions, total, err := source.ContentVersions(t.Context(), file.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)

	schema := document.MetadataSchema{
		UID: "11111111-1111-4111-8111-111111111111", Version: 1, Name: "Research",
		Scope: document.MetadataScope{Kind: "vault", ID: "synthetic"},
		Fields: []document.MetadataField{{
			Key: "title", Kind: document.MetadataKindString,
			ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneSourceExtracted},
		}},
	}
	require.NoError(t, source.AppendMetadataSchemaVersion(t.Context(), schema))
	value, err := json.Marshal("Synthetic title")
	require.NoError(t, err)
	storedValue, err := source.PutMetadataValue(t.Context(), MetadataValueRecord{
		DocumentUID: identity.DocumentUID, ContentVersionID: versions[0].ID,
		SchemaUID: schema.UID, SchemaVersion: schema.Version, FieldKey: "title",
		ValueJSON: jsontext.Value(value), Lane: document.MetadataLaneSourceExtracted,
		Accepted: true, Producer: "synthetic-extractor", SourcePointer: "frontmatter.title",
		CapturedAt: "2026-09-22T00:00:00Z", Revision: 1,
	})
	require.NoError(t, err)
	frontmatter, err := NewImportedFrontmatterRecord(identity.DocumentUID, versions[0].ID,
		[]byte("---\ntitle: harmless\n---\n"), "2026-09-22T00:00:00Z")
	require.NoError(t, err)
	storedFrontmatter, err := source.PutImportedFrontmatter(t.Context(), frontmatter)
	require.NoError(t, err)

	var exported bytes.Buffer
	require.NoError(t, source.ExportMetadata(t.Context(), &exported))
	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(t.Context(), bytes.NewReader(exported.Bytes())))
	schemas, err := target.MetadataSchemaVersions(t.Context(), schema.UID)
	require.NoError(t, err)
	require.Equal(t, []document.MetadataSchema{schema}, schemas)
	values, err := target.MetadataValues(t.Context(), identity.DocumentUID)
	require.NoError(t, err)
	require.Equal(t, []MetadataValueRecord{storedValue}, values)
	restoredFrontmatter, err := target.ImportedFrontmatter(t.Context(), identity.DocumentUID, versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, storedFrontmatter, restoredFrontmatter)
	var replayed bytes.Buffer
	require.NoError(t, target.ExportMetadata(t.Context(), &replayed))
	require.Equal(t, exported.Bytes(), replayed.Bytes())
}

// Mutation caught: dropping sidecar authority from an audited backup or
// rebuilding it outside the same deterministic metadata stream as audit data.
func TestMetadataAuthoritySurvivesAuditedBackupRoundTrip(t *testing.T) {
	source := newTestStore(t)
	file, err := source.CreateFile(t.Context(), source.RootID(), "audited.txt", fakeHash("d2"), 9, "text/plain",
		BlobPhysical{Encoding: "raw", StoredBytes: 9, PackEligible: true, Created: true})
	require.NoError(t, err)
	identity, err := source.EnsureDocumentIdentity(t.Context(), file.ID)
	require.NoError(t, err)
	versions, total, err := source.ContentVersions(t.Context(), file.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	schema := document.MetadataSchema{
		UID: "22222222-2222-4222-8222-222222222222", Version: 1, Name: "Audited",
		Scope: document.MetadataScope{Kind: "vault", ID: "synthetic"},
		Fields: []document.MetadataField{{
			Key: "title", Kind: document.MetadataKindString,
			ProvenanceLanes: []document.MetadataProvenanceLane{document.MetadataLaneUserOverride},
		}},
	}
	require.NoError(t, source.AppendMetadataSchemaVersion(t.Context(), schema))
	value, err := json.Marshal("Audited title")
	require.NoError(t, err)
	stored, err := source.PutMetadataValue(t.Context(), MetadataValueRecord{
		DocumentUID: identity.DocumentUID, ContentVersionID: versions[0].ID,
		SchemaUID: schema.UID, SchemaVersion: schema.Version, FieldKey: "title",
		ValueJSON: jsontext.Value(value), Lane: document.MetadataLaneUserOverride,
		Accepted: true, Producer: "synthetic-operator", SourcePointer: "operator.title",
		CapturedAt: "2026-09-22T00:00:00Z", Revision: 1,
	})
	require.NoError(t, err)
	plan, err := source.PreviewInitialAudit(t.Context(), source.RootID(), "api", nil)
	require.NoError(t, err)
	_, err = source.EnableInitialAudit(t.Context(), plan)
	require.NoError(t, err)

	var exported bytes.Buffer
	require.NoError(t, source.ExportMetadata(t.Context(), &exported))
	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(t.Context(), bytes.NewReader(exported.Bytes())))
	require.NoError(t, target.ValidateMetadata(t.Context()))
	values, err := target.MetadataValues(t.Context(), identity.DocumentUID)
	require.NoError(t, err)
	require.Equal(t, []MetadataValueRecord{stored}, values)
	var replayed bytes.Buffer
	require.NoError(t, target.ExportMetadata(t.Context(), &replayed))
	require.Equal(t, exported.Bytes(), replayed.Bytes())
}
