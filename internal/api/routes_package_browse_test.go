package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/canonical"
	"go.kenn.io/docbank/internal/loadfile"
	"go.kenn.io/docbank/internal/store"
)

func TestPackageBrowseRoutesPreserveScopedAuthority(t *testing.T) {
	srv, catalog := newPackageTestServer(t)
	received, rowID, raw := seedBrowseReceivedPackage(t, catalog)
	produced := seedBrowseProducedPackage(t, catalog)

	list := srv.get(t, "/api/v1/packages?limit=1")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var first api.PackagePage
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &first))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextAfter)
	second := srv.get(t, "/api/v1/packages?limit=1&after="+first.NextAfter)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	var remaining api.PackagePage
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &remaining))
	require.Len(t, remaining.Items, 1)
	assert.ElementsMatch(t, []string{received.PackageID, produced.PackageID},
		[]string{first.Items[0].PackageID, remaining.Items[0].PackageID})

	detail := srv.get(t, "/api/v1/packages/by-id/"+received.PackageID)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	var packageDetail store.Package
	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &packageDetail))
	assert.Equal(t, received.PackageID, packageDetail.PackageID)
	assert.Equal(t, "received", packageDetail.Direction)

	members := srv.get(t, "/api/v1/packages/by-id/"+produced.PackageID+"/members?limit=1")
	require.Equal(t, http.StatusOK, members.Code, members.Body.String())
	var memberPage api.PackageMemberPage
	require.NoError(t, json.Unmarshal(members.Body.Bytes(), &memberPage))
	require.Len(t, memberPage.Items, 1)
	assert.Equal(t, 1, memberPage.Items[0].Ordinal)
	assert.Equal(t, "produced.txt", memberPage.Items[0].DisplayName)

	labels := srv.get(t, "/api/v1/packages/label-candidates?label=EXT000001&package_id="+
		received.PackageID+"&label_set=sender&provenance=received&limit=1")
	require.Equal(t, http.StatusOK, labels.Code, labels.Body.String())
	var labelPage api.PackageLabelCandidatePage
	require.NoError(t, json.Unmarshal(labels.Body.Bytes(), &labelPage))
	require.Len(t, labelPage.Items, 1)
	assert.Equal(t, received.PackageID, labelPage.Items[0].PackageID)
	assert.Equal(t, "EXT000001", labelPage.Items[0].Label)
	assert.Empty(t, labelPage.NextCursor)

	timeline := srv.get(t, "/api/v1/packages/by-id/"+received.PackageID+"/timeline-inputs?limit=1")
	require.Equal(t, http.StatusOK, timeline.Code, timeline.Body.String())
	var timelinePage api.PackageTimelineInputPage
	require.NoError(t, json.Unmarshal(timeline.Body.Bytes(), &timelinePage))
	require.Len(t, timelinePage.Items, 1)
	assert.Equal(t, rowID, timelinePage.Items[0].RowID)
	assert.Equal(t, raw, timelinePage.Items[0].RawJSON)
	assert.Equal(t, "UTC", timelinePage.Items[0].DeclaredTimezone)

	catalogResponse := srv.get(t, "/api/v1/packages/field-catalog")
	require.Equal(t, http.StatusOK, catalogResponse.Code, catalogResponse.Body.String())
	var fieldCatalog api.PackageFieldCatalog
	require.NoError(t, json.Unmarshal(catalogResponse.Body.Bytes(), &fieldCatalog))
	assert.NotEmpty(t, fieldCatalog.Fields)
	assert.Contains(t, fieldCatalog.Fields, loadfile.CatalogEntry{
		Canonical: "loadfile.label.begin", Aliases: []string{"BEGBATES", "BEGDOC", "Begin Doc"},
	})
}

func TestPackageBrowseRoutesEnforceBoundsAndIdentity(t *testing.T) {
	srv, _ := newPackageTestServer(t)
	for _, path := range []string{
		"/api/v1/packages?limit=251",
		"/api/v1/packages/by-id/" + uuid.NewString() + "/members?after_ordinal=-1",
		"/api/v1/packages/label-candidates",
		"/api/v1/packages/label-candidates?label=EXT000001&limit=0",
		"/api/v1/packages/by-id/" + uuid.NewString() + "/timeline-inputs?limit=251",
	} {
		response := srv.get(t, path)
		assert.Equal(t, http.StatusUnprocessableEntity, response.Code, "%s: %s", path, response.Body.String())
	}
	missing := srv.get(t, "/api/v1/packages/by-id/"+uuid.NewString())
	assert.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
}

func seedBrowseReceivedPackage(t *testing.T, catalog *testStore) (store.Package, string, []byte) {
	t.Helper()
	profile, err := loadfile.ReadProfile("dat-concordance-v1")
	require.NoError(t, err)
	profile.DeclaredTimezone = "UTC"
	profileJSON, err := canonical.Marshal(profile)
	require.NoError(t, err)
	profileDigest := sha256.Sum256(profileJSON)
	mappingJSON := []byte("{}")
	mappingDigest := sha256.Sum256(mappingJSON)
	manifestHash, manifestSize, err := catalog.Blobs.Write(strings.NewReader("synthetic received manifest"))
	require.NoError(t, err)
	require.NoError(t, catalog.RecordBlob(t.Context(), manifestHash, manifestSize,
		store.BlobPhysical{Encoding: "raw", StoredBytes: manifestSize}))
	run, err := catalog.BeginIngest(t.Context(), "package:loadfile", "synthetic received package")
	require.NoError(t, err)
	body := []byte("synthetic received document")
	bodyDigest := sha256.Sum256(body)
	node, err := catalog.IngestFileExact(t.Context(), run, catalog.RootID(), "received.txt",
		hex.EncodeToString(bodyDigest[:]), int64(len(body)), "text/plain", "received.txt", "")
	require.NoError(t, err)
	request := store.PackageRequest{
		PackageID: uuid.NewString(), Direction: "received", PackageName: "received-001",
		PartyLabel: "Synthetic sender", ProfileJSON: string(profileJSON),
		ProfileSHA256: hex.EncodeToString(profileDigest[:]), MappingJSON: string(mappingJSON),
		MappingSHA256: hex.EncodeToString(mappingDigest[:]), ManifestSHA256: manifestHash,
		ManifestBlobSHA256: manifestHash, IngestID: run.ID(), ProducedOn: "2026-09-21T00:00:00Z",
		State: "importing", Volumes: []store.PackageVolume{{Ordinal: 1, VolumeName: "VOL001",
			DeclaredRoot: "VOL001", MappedRoot: "VOL001", ResolvedRootSHA256: testHash("received-root")}},
	}
	pkg, err := catalog.CreatePackage(t.Context(), request)
	require.NoError(t, err)
	rowID, err := store.PackageRecordKey("VOL001/DATA/received.dat", 1, "DOC-A")
	require.NoError(t, err)
	occurrenceID := store.PackageOccurrenceID(pkg.PackageID, rowID)
	raw := []byte(`{"BEGBATES":"EXT000001","DOCDATE":"2026-09-21"}`)
	rawDigest := sha256.Sum256(raw)
	_, err = catalog.CommitPackageRecord(t.Context(), store.PackageRecordRow{
		PackageID: pkg.PackageID, RowID: rowID, LoadFile: "VOL001/DATA/received.dat", RowOrdinal: 1,
		OccurrenceID: occurrenceID, RawJSON: raw, RawSHA256: hex.EncodeToString(rawDigest[:]),
	}, []store.PackageLabelRow{{
		PackageID: pkg.PackageID, Provenance: "received", LabelSet: "sender", Label: "EXT000001",
		LabelSortKey: store.LabelSortKey("EXT000001"), OccurrenceID: occurrenceID,
		ContentVersionID: node.CurrentVersionID, PageState: "unknown", Endpoint: "begin",
	}}, store.PackageImportReceipt{
		ReceiptID: uuid.NewString(), PackageID: pkg.PackageID, RecordKey: rowID, OccurrenceID: occurrenceID,
		ContentVersionID: node.CurrentVersionID, State: "committed", ReceiptJSON: []byte("{}"),
	})
	require.NoError(t, err)
	return pkg, rowID, raw
}

func seedBrowseProducedPackage(t *testing.T, catalog *testStore) store.Package {
	t.Helper()
	body := "synthetic produced document"
	hash, size, err := catalog.Blobs.Write(strings.NewReader(body))
	require.NoError(t, err)
	node, err := catalog.CreateFile(t.Context(), catalog.RootID(), "produced.txt", hash, size, "text/plain")
	require.NoError(t, err)
	snapshotID := uuid.NewString()
	occurrenceID := strings.Repeat("a", 32)
	_, err = catalog.SealCollectionSnapshot(t.Context(), store.SnapshotSealRequest{
		SnapshotID: snapshotID, Members: []store.CollectionSnapshotMember{{
			Ordinal: 1, OccurrenceID: occurrenceID, NodeID: node.ID, ContentVersionID: node.CurrentVersionID,
			BlobSHA256: hash, Size: size, FamilyID: occurrenceID, FamilyOrder: 1,
			DisplayName: "produced.txt", FrozenFieldsJSON: "{}", DocumentKind: "other",
		}},
	})
	require.NoError(t, err)
	manifestHash, manifestSize, err := catalog.Blobs.Write(strings.NewReader("synthetic produced manifest"))
	require.NoError(t, err)
	require.NoError(t, catalog.RecordBlob(t.Context(), manifestHash, manifestSize,
		store.BlobPhysical{Encoding: "raw", StoredBytes: manifestSize}))
	profileJSON := []byte("{}")
	profileDigest := sha256.Sum256(profileJSON)
	request := store.PackageRequest{
		PackageID: uuid.NewString(), SnapshotID: snapshotID, Direction: "produced", PackageName: "produced-001",
		PartyLabel: "Synthetic recipient", ProfileJSON: string(profileJSON),
		ProfileSHA256: hex.EncodeToString(profileDigest[:]), MappingJSON: string(profileJSON),
		MappingSHA256: hex.EncodeToString(profileDigest[:]), ManifestSHA256: manifestHash,
		ManifestBlobSHA256: manifestHash, State: "sealed", Volumes: []store.PackageVolume{{Ordinal: 1,
			VolumeName: "VOL001", DeclaredRoot: "VOL001", MappedRoot: "VOL001",
			ResolvedRootSHA256: testHash("produced-root")}},
	}
	pkg, err := catalog.CreatePackage(t.Context(), request)
	require.NoError(t, err)
	return pkg
}
