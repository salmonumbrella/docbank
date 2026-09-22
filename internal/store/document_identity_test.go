package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
)

func TestDocumentIdentityIsStableAcrossMoveReplaceAndPathReuse(t *testing.T) {
	s, versions := newRenditionCatalogFixture(t)
	node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
	require.NoError(t, err)
	identity, err := s.EnsureDocumentIdentity(t.Context(), node.ID)
	require.NoError(t, err)
	require.NoError(t, validateUUIDv4(identity.DocumentUID))

	moved, _, err := s.MoveToPath(t.Context(), node.ID, node.Revision, "/renamed.pdf")
	require.NoError(t, err)
	_, _, err = s.ReplaceContent(t.Context(), moved.ID, moved.Revision,
		testSHA256([]byte("replacement")), 11, "application/pdf")
	require.NoError(t, err)
	after, err := s.EnsureDocumentIdentity(t.Context(), node.ID)
	require.NoError(t, err)
	assert.Equal(t, identity, after)

	run, err := s.BeginIngest(t.Context(), "test", "path reuse")
	require.NoError(t, err)
	reused, err := s.IngestFileExact(t.Context(), run, s.RootID(), "synthetic-source-a.pdf",
		testSHA256([]byte("reused")), 6, "application/pdf", "synthetic-source-a.pdf", "")
	require.NoError(t, err)
	reusedIdentity, err := s.EnsureDocumentIdentity(t.Context(), reused.ID)
	require.NoError(t, err)
	assert.NotEqual(t, identity.DocumentUID, reusedIdentity.DocumentUID)
	assert.NotEmpty(t, versions)
}

func TestResolvePassageAuthorityUsesHistoricalTupleAndAliases(t *testing.T) {
	s, versions := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(),
		ContentVersionID: versions[0], BuildID: build.ID, Profile: profile,
		AttachedAt: "2026-08-22T10:00:00.000000000Z"}
	require.NoError(t, publishRenditionForTest(t, s, attachment,
		"2026-08-22T10:01:00.000000000Z", testSHA256([]byte("passage-generation"))))
	node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
	require.NoError(t, err)
	identity, err := s.EnsureDocumentIdentity(t.Context(), node.ID)
	require.NoError(t, err)
	ref := document.PassageRefV1{Version: 1, VaultUID: s.VaultID(), DocumentUID: identity.DocumentUID,
		ContentVersionID: versions[0], SourceSHA256: build.SourceSHA256,
		RenditionBuildID: build.ID, AttachmentID: attachment.ID,
		BodySHA256: testSHA256([]byte("body")), ByteStart: 0, ByteEnd: 1,
		QuoteSHA256: testSHA256([]byte("b"))}

	resolved, err := s.ResolvePassageAuthority(t.Context(), ref)
	require.NoError(t, err)
	assert.Equal(t, versions[0], resolved.Version.ID)
	assert.Equal(t, build.ID, resolved.Build.ID)
	assert.Equal(t, attachment.ID, resolved.Attachment.ID)
	assert.True(t, resolved.Fresh)

	// A changed current version must not retarget the historical tuple.
	_, _, err = s.ReplaceContent(t.Context(), node.ID, node.Revision,
		testSHA256([]byte("new current")), 11, "application/pdf")
	require.NoError(t, err)
	resolved, err = s.ResolvePassageAuthority(t.Context(), ref)
	require.NoError(t, err)
	assert.Equal(t, versions[0], resolved.Version.ID)
	assert.False(t, resolved.Fresh)

	domain := "99999999-9999-4999-8999-999999999999"
	sourceVault := "88888888-8888-4888-8888-888888888888"
	sourceDocument := "77777777-7777-4777-8777-777777777777"
	require.NoError(t, s.PutDocumentIdentityAlias(t.Context(), domain, sourceVault,
		sourceDocument, identity.DocumentUID))
	aliased := ref
	aliased.FederationDomainUID, aliased.VaultUID, aliased.DocumentUID = domain, sourceVault, sourceDocument
	resolved, err = s.ResolvePassageAuthority(t.Context(), aliased)
	require.NoError(t, err)
	assert.Equal(t, identity, resolved.Identity,
		"an adopted source identity must resolve to the exact local document identity")

	for name, mutate := range map[string]func(*document.PassageRefV1){
		"vault": func(value *document.PassageRefV1) {
			value.VaultUID = "66666666-6666-4666-8666-666666666666"
		},
		"document": func(value *document.PassageRefV1) {
			value.DocumentUID = "55555555-5555-4555-8555-555555555555"
		},
		"content version": func(value *document.PassageRefV1) {
			value.ContentVersionID = "44444444-4444-4444-8444-444444444444"
		},
		"source": func(value *document.PassageRefV1) {
			value.SourceSHA256 = testSHA256([]byte("wrong source"))
		},
		"attachment": func(value *document.PassageRefV1) {
			value.AttachmentID = testSHA256([]byte("wrong attachment"))
		},
		"build": func(value *document.PassageRefV1) {
			value.RenditionBuildID = testSHA256([]byte("wrong build"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := ref
			mutate(&changed)
			_, err := s.ResolvePassageAuthority(t.Context(), changed)
			require.ErrorIs(t, err, ErrPassageAuthorityUnavailable)
		})
	}

	trashed, _, err := s.Trash(t.Context(), node.ID, UnconditionalRev)
	require.NoError(t, err)
	_, err = s.ResolvePassageAuthority(t.Context(), ref)
	require.ErrorIs(t, err, ErrPassageAuthorityUnavailable,
		"revoked source visibility must deny historical passage resolution")
	_, _, err = s.Restore(t.Context(), trashed.ID, UnconditionalRev)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM rendition_attachments WHERE attachment_id=?`, attachment.ID)
	require.NoError(t, err)
	_, err = s.ResolvePassageAuthority(t.Context(), ref)
	require.ErrorIs(t, err, ErrPassageAuthorityUnavailable,
		"pruned historical rendition authority must not fall back to the current head")
}
