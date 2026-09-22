package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/kit/packstore"
)

func TestResolvePassageReturnsExactVerifiedHistoricalWindow(t *testing.T) {
	fixture := newPassageResolutionFixture(t)
	resolution, err := resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
		PassageResolveRequest{Ref: fixture.ref, MaxBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, "available", resolution.Availability)
	assert.Equal(t, "historical", resolution.Freshness)
	assert.Equal(t, "😀 evidence", resolution.Text)
	assert.Equal(t, []string{"Synthetic heading"}, resolution.SectionPath)
	require.NotNil(t, resolution.SourceLocator)
	assert.Equal(t, document.EvidenceLocatorPage, resolution.SourceLocator.Kind)
	assert.Equal(t, "/renamed/source.pdf", resolution.SourcePath)
	assert.NotEmpty(t, resolution.PassageID)
	assert.Equal(t, 1, fixture.catalog.calls)
	assert.Equal(t, 1, fixture.blobs.calls)
}

func TestResolvePassageFailsClosedWithoutOpeningUnauthorizedOrUnavailableContent(t *testing.T) {
	fixture := newPassageResolutionFixture(t)
	foreign := fixture.ref
	foreign.VaultUID = "99999999-9999-4999-8999-999999999999"
	_, err := resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
		PassageResolveRequest{Ref: foreign, MaxBytes: 64})
	require.ErrorIs(t, err, ErrPassageUnauthorized)
	assert.Zero(t, fixture.catalog.calls)
	assert.Zero(t, fixture.blobs.calls)

	fixture = newPassageResolutionFixture(t)
	fixture.catalog.err = store.ErrPassageAuthorityUnavailable
	fixture.ref.QuoteSHA256 = passageProcessingHash("tampered but undisclosed")
	_, err = resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
		PassageResolveRequest{Ref: fixture.ref, MaxBytes: 64})
	require.ErrorIs(t, err, ErrPassageUnavailable)
	assert.Equal(t, 1, fixture.catalog.calls)
	assert.Zero(t, fixture.blobs.calls, "revoked or pruned authority must be rejected before content access")
}

func TestResolvePassageRejectsTamperedHashesBuildsAndBudgets(t *testing.T) {
	for name, mutate := range map[string]func(*passageResolutionFixture){
		"quote hash": func(f *passageResolutionFixture) {
			f.ref.QuoteSHA256 = passageProcessingHash("wrong quote")
		},
		"body hash": func(f *passageResolutionFixture) {
			f.ref.BodySHA256 = passageProcessingHash("wrong body")
		},
		"build identity": func(f *passageResolutionFixture) {
			f.ref.RenditionBuildID = passageProcessingHash("wrong build")
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPassageResolutionFixture(t)
			mutate(&fixture)
			_, err := resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
				PassageResolveRequest{Ref: fixture.ref, MaxBytes: 64})
			require.ErrorIs(t, err, ErrPassageCorrupt)
		})
	}
	fixture := newPassageResolutionFixture(t)
	_, err := resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
		PassageResolveRequest{Ref: fixture.ref, MaxBytes: len("😀 evidence") - 1})
	require.ErrorIs(t, err, ErrPassageInvalid)
	assert.Zero(t, fixture.catalog.calls)
	assert.Zero(t, fixture.blobs.calls)
}

func TestResolvePassageRejectsBlobPayloadThatDoesNotMatchCatalogDigest(t *testing.T) {
	fixture := newPassageResolutionFixture(t)
	fixture.blobs.payload[len(fixture.blobs.payload)-2] ^= 1
	_, err := resolvePassage(t.Context(), fixture.catalog, fixture.blobs,
		PassageResolveRequest{Ref: fixture.ref, MaxBytes: 64})
	require.ErrorIs(t, err, ErrPassageCorrupt)
}

type passageResolutionFixture struct {
	ref     document.PassageRefV1
	catalog *passageCatalogStub
	blobs   *passageBlobStub
}

func newPassageResolutionFixture(t *testing.T) passageResolutionFixture {
	t.Helper()
	body := []byte("# Synthetic heading\r\n\r\n😀 evidence and e\u0301\r\n")
	buildID := passageProcessingHash("passage build")
	sourceHash := passageProcessingHash("passage source")
	rendered, frontmatter, err := document.EnvelopeRenditionV1(document.RenditionV1{
		ContractVersion: document.RenditionContractV1, Completeness: document.EvidenceComplete,
		EvidenceChecksum: passageProcessingHash("evidence"), Markdown: body,
		MarkdownChecksum: passageProcessingHashBytes(body),
		Units: []document.NormalizedUnitV1{{EvidenceUnitID: "page:000000", Order: 0,
			Text: string(body), HeadingPath: []string{"Synthetic heading"},
			Locator: document.EvidenceLocatorV1{Kind: document.EvidenceLocatorPage,
				IndexOrigin: document.EvidenceIndexOriginOne, Start: 1, End: 1}}},
	}, document.RenditionEnvelopeV1{BuildID: buildID, SourceSHA256: sourceHash,
		SourceFormat: "pdf", SourceMediaType: "application/pdf",
		RenditionRequestFingerprint: passageProcessingHash("request"),
		EvidenceLexicalFingerprint:  passageProcessingHash("lexical"),
		NormalizedEvidenceContract:  document.NormalizedEvidenceContractV1,
		UnitKind:                    document.EvidenceUnitPage})
	require.NoError(t, err)
	start := bytes.Index(body, []byte("😀 evidence"))
	end := start + len("😀 evidence")
	ref, err := document.NewPassageRefV1(document.PassageRefV1{
		VaultUID:         "11111111-1111-4111-8111-111111111111",
		DocumentUID:      "22222222-2222-4222-8222-222222222222",
		ContentVersionID: "33333333-3333-4333-8333-333333333333",
		SourceSHA256:     sourceHash, RenditionBuildID: buildID,
		AttachmentID: passageProcessingHash("attachment"),
	}, body, start, end)
	require.NoError(t, err)
	unit := store.RenditionUnitRecord{EvidenceUnitID: frontmatter.Navigation.Entries[0].Key,
		HeadingPath: []string{"Synthetic heading"}, Locator: document.EvidenceLocatorV1{
			Kind: document.EvidenceLocatorPage, IndexOrigin: document.EvidenceIndexOriginOne,
			Start: 1, End: 1}}
	artifactHash := passageProcessingHashBytes(rendered.Markdown)
	authority := store.PassageAuthority{Path: "/renamed/source.pdf", Fresh: false,
		Build: store.RenditionBuildRecord{ID: buildID, SourceSHA256: sourceHash,
			Units: []store.RenditionUnitRecord{unit}},
		Artifact: store.RenditionArtifactRecord{ID: "artifact", Role: "sanitized_markdown",
			BlobHash: artifactHash, Checksum: artifactHash, Size: int64(len(rendered.Markdown)),
			State: store.RenditionArtifactVerified}}
	return passageResolutionFixture{ref: ref,
		catalog: &passageCatalogStub{vaultID: ref.VaultUID, authority: authority},
		blobs:   &passageBlobStub{payload: rendered.Markdown}}
}

type passageCatalogStub struct {
	vaultID   string
	authority store.PassageAuthority
	err       error
	calls     int
}

func (stub *passageCatalogStub) VaultID() string { return stub.vaultID }
func (stub *passageCatalogStub) ResolvePassageAuthority(
	context.Context, document.PassageRefV1,
) (store.PassageAuthority, error) {
	stub.calls++
	return stub.authority, stub.err
}

type passageBlobStub struct {
	payload []byte
	err     error
	calls   int
}

func (stub *passageBlobStub) OpenStreamContext(
	context.Context, string,
) (packstore.VerifiedReadCloser, int64, error) {
	stub.calls++
	if stub.err != nil {
		return nil, 0, stub.err
	}
	return &passageVerifiedReader{Reader: bytes.NewReader(stub.payload)}, int64(len(stub.payload)), nil
}

type passageVerifiedReader struct {
	*bytes.Reader

	verified bool
}

func (reader *passageVerifiedReader) Read(value []byte) (int, error) {
	n, err := reader.Reader.Read(value)
	if errors.Is(err, io.EOF) {
		reader.verified = true
	}
	return n, err //nolint:wrapcheck // Preserve io.EOF for the io.Reader contract.
}
func (reader *passageVerifiedReader) Close() error   { return nil }
func (reader *passageVerifiedReader) Verified() bool { return reader.verified }
func (reader *passageVerifiedReader) Verify() error  { return nil }

func passageProcessingHash(value string) string { return passageProcessingHashBytes([]byte(value)) }
func passageProcessingHashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
