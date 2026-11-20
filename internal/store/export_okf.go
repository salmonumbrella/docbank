package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/internal/canonical"
)

func validateOKFPlanRequest(r *bundle.PlanRequest) error {
	selected := slices.ContainsFunc(r.Roles, func(p bundle.RolePolicy) bool { return p.Role == "okf_markdown" })
	if selected != (r.OKF != nil) {
		return fmt.Errorf("%w: okf_markdown role and okf selector must be supplied together", bundle.ErrConflict)
	}
	if !selected {
		return nil
	}
	o := r.OKF
	if len(r.Roles) != 1 || r.Roles[0] != (bundle.RolePolicy{Role: "okf_markdown"}) || r.PhotoRender != nil || len(r.Publications) != 0 || r.VolumeLimits != nil || (r.DuplicatePolicy != "" && r.DuplicatePolicy != "preserve") || !canonical.IsSHA256Hex(o.ProfileFingerprint) || !canonical.IsSHA256Hex(o.AttachmentID) || !canonical.IsSHA256Hex(o.BuildID) {
		return fmt.Errorf("%w: OKF requires one okf_markdown role and no other export options", bundle.ErrConflict)
	}
	return nil
}

// OKFTag identifies a committed tag for stable seal-time comparison.
type OKFTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type OKFInput struct {
	Member         bundle.Member        `json:"member"`
	Name           string               `json:"name"`
	MediaType      string               `json:"media_type"`
	ArtifactSHA256 string               `json:"artifact_sha256"`
	ArtifactSize   int64                `json:"artifact_size"`
	Build          RenditionBuildRecord `json:"build"`
	Generated      *bundle.OKFGenerated `json:"generated,omitempty"`
	Tags           []OKFTag             `json:"tags"`
}

func (s *Store) exportOKFInput(ctx context.Context, q metadataQuerier, m bundle.Member, option bundle.OKFExport) (input OKFInput, err error) {
	input.Member = m
	if m.Revision < 1 {
		return input, bundle.ErrConflict
	}
	var rev int64
	var trashed sql.NullString
	var hash string
	var size int64
	err = q.QueryRowContext(ctx, `SELECT n.name,n.revision,n.trashed_at,v.mime_type,v.blob_hash,v.size FROM nodes n JOIN content_versions v ON v.node_id=n.id WHERE n.id=$1 AND v.version_id=$2`, m.NodeID, m.VersionID).Scan(&input.Name, &rev, &trashed, &input.MediaType, &hash, &size)
	if err != nil || trashed.Valid || rev != m.Revision || hash != m.SHA256 || size != m.Size {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return input, err
		}
		return input, bundle.ErrConflict
	}
	var hidden bool
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM photo_files f JOIN photo_assets a ON a.asset_id=f.asset_id WHERE f.node_id=$1 AND a.hidden_at IS NOT NULL)`, m.NodeID).Scan(&hidden); err != nil {
		return input, err
	}
	if hidden {
		if _, err = s.hiddenSession(ctx, q); err != nil {
			return input, err
		}
	}
	var attachmentID, buildID, attachmentVersion, attachmentProfile string
	err = q.QueryRowContext(ctx, `SELECT a.attachment_id,a.build_id,a.content_version_id,a.profile_fingerprint FROM rendition_heads h JOIN rendition_attachments a ON a.attachment_id=h.attachment_id WHERE h.content_version_id=$1 AND h.profile_fingerprint=$2`, m.VersionID, option.ProfileFingerprint).Scan(&attachmentID, &buildID, &attachmentVersion, &attachmentProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return input, ErrRenditionTextStale
	}
	if err != nil {
		return input, err
	}
	if attachmentID != option.AttachmentID || buildID != option.BuildID || attachmentVersion != m.VersionID || attachmentProfile != option.ProfileFingerprint {
		return input, ErrRenditionTextStale
	}
	input.Build, err = loadRenditionBuildView(ctx, q, buildID, true)
	if err != nil {
		return input, err
	}
	if input.Build.SourceSHA256 != m.SHA256 {
		return input, ErrRenditionTextStale
	}
	for _, a := range input.Build.Artifacts {
		if a.Role == catalogArtifactSanitizedMarkdown && a.State == RenditionArtifactVerified && a.BlobHash == a.Checksum && a.BlobHash == input.Build.MarkdownChecksum {
			input.ArtifactSHA256, input.ArtifactSize = a.BlobHash, a.Size
			break
		}
	}
	if input.ArtifactSHA256 == "" {
		return input, ErrRenditionTextUnavailable
	}
	generated, present, err := okfGenerated(ctx, q, input.Build, option.ProfileFingerprint, m.SHA256)
	if err != nil {
		return input, err
	}
	if present {
		input.Generated = &generated
	}
	tagBytes := 0
	err = queryRows(ctx, q, `SELECT t.id,t.name FROM node_tags nt JOIN tags t ON t.id=nt.tag_id WHERE nt.node_id=$1`, []any{m.NodeID}, func(rows *sql.Rows) error {
		var tag OKFTag
		if e := rows.Scan(&tag.ID, &tag.Name); e != nil {
			return e
		}
		tagBytes += len(tag.ID) + len(tag.Name)
		if len(input.Tags) >= 256 || tagBytes > 16<<10 {
			return fmt.Errorf("%w: OKF tags exceed 256 entries or 16 KiB of tag IDs and names", bundle.ErrLimit)
		}
		input.Tags = append(input.Tags, tag)
		return nil
	})
	if err != nil {
		return input, err
	}
	slices.SortFunc(input.Tags, func(a, b OKFTag) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return input, nil
}

func (s *Store) ExportOKFInput(ctx context.Context, owner string, r bundle.PlanRequest) (input OKFInput, err error) {
	if _, _, err = validateExportPlanRequest(owner, &r); err != nil {
		return input, err
	}
	if r.OKF == nil {
		return input, bundle.ErrConflict
	}
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		source, e := loadExportSource(ctx, tx, owner, r.SourceID)
		if e != nil {
			return e
		}
		if exportExpired(source.ExpiresAt) {
			return bundle.ErrExpired
		}
		if source.State != "sealed" || source.MemberHash != r.MemberHash || source.Kind != "explicit" || source.Total != 1 {
			return bundle.ErrConflict
		}
		return walkExportMembers(ctx, tx, r.SourceID, func(m bundle.Member) error {
			input, e = s.exportOKFInput(ctx, tx, m, *r.OKF)
			return okfRenditionError(e)
		})
	})
	if err != nil {
		return input, err
	}
	_, err = s.ResolveRenditionText(ctx, RenditionTextBinding{NodeID: input.Member.NodeID, NodeRevision: input.Member.Revision, ContentVersionID: input.Member.VersionID, SourceSHA256: input.Member.SHA256, SourceSize: input.Member.Size, ProfileFingerprint: r.OKF.ProfileFingerprint, AttachmentID: r.OKF.AttachmentID, BuildID: r.OKF.BuildID})
	if errors.Is(err, ErrNotFound) {
		return input, fmt.Errorf("%w: rendition no longer available", bundle.ErrConflict)
	}
	return input, okfRenditionError(err)
}

func okfRenditionError(err error) error {
	if errors.Is(err, ErrRenditionTextStale) {
		return fmt.Errorf("%w: rendition selection is stale", bundle.ErrConflict)
	}
	if errors.Is(err, ErrRenditionTextUnavailable) || errors.Is(err, ErrRenditionTextFailed) {
		return fmt.Errorf("%w: retained rendition is unavailable", bundle.ErrUnavailable)
	}
	return err
}

type preparedExportRole struct {
	SHA256   string
	Size     int64
	Physical BlobPhysical
	Role     bundle.Role
	embedded bool
	check    func(context.Context, *sql.Tx, bundle.Member) (bundle.Document, error)
}
type PreparedOKFExport struct {
	Input    OKFInput
	Receipt  bundle.OKFReceipt
	SHA256   string
	Size     int64
	Physical BlobPhysical
}

func (s *Store) SealOKFExportPlan(ctx context.Context, owner string, r bundle.PlanRequest, a PreparedOKFExport) (bundle.Plan, bool, error) {
	if r.OKF == nil {
		return bundle.Plan{}, false, bundle.ErrConflict
	}
	raw, err := canonical.Marshal(a.Receipt)
	if err != nil {
		return bundle.Plan{}, false, err
	}
	checked := false
	role := bundle.Role{Role: "okf_markdown", Status: roleAvailable, Path: bundle.OKFPath(a.Input.Member), SHA256: a.SHA256, Size: a.Size, MediaType: "text/markdown; charset=utf-8", Recipe: raw}
	prepared := map[string]preparedExportRole{a.Input.Member.VersionID: {SHA256: a.SHA256, Size: a.Size, Physical: a.Physical, Role: role, check: func(ctx context.Context, tx *sql.Tx, m bundle.Member) (bundle.Document, error) {
		checked = true
		current, e := s.exportOKFInput(ctx, tx, m, *r.OKF)
		if e != nil {
			return bundle.Document{}, okfRenditionError(e)
		}
		frozen, e := canonical.Marshal(a.Input)
		if e != nil {
			return bundle.Document{}, e
		}
		actual, e := canonical.Marshal(current)
		if e != nil || !bytes.Equal(frozen, actual) || a.Receipt.Source != m || a.Receipt.Option != *r.OKF || a.Receipt.ArtifactSHA256 != current.ArtifactSHA256 || a.Receipt.ArtifactSize != current.ArtifactSize || a.Receipt.NodeRevision != m.Revision || !generatedEqual(a.Receipt.Generated, current.Generated) {
			return bundle.Document{}, fmt.Errorf("%w: member %d changed during preparation", bundle.ErrConflict, m.NodeID)
		}
		return bundle.Document{Member: m, Name: current.Name, MediaType: current.MediaType}, nil
	}}}
	plan, err := s.createExportPlan(ctx, owner, r, prepared)
	return plan, checked && err == nil, err
}

// OKFBlobUnreferenced checks cleanup under the caller's exclusive maintenance lease.
func (s *Store) OKFBlobUnreferenced(ctx context.Context, hash string) (bool, error) {
	var free bool
	err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM blobs WHERE hash=$1) AND `+blobUnreferencedSQL("$1", blobRootReferences, blobGCHolds), hash).Scan(&free)
	return free, err
}

func generatedEqual(a, b *bundle.OKFGenerated) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func okfGenerated(ctx context.Context, q metadataQuerier, build RenditionBuildRecord, profileID, source string) (bundle.OKFGenerated, bool, error) {
	var receipt document.RenditionReceipt
	if !decodeOKFReceipt(build.ProviderReceipt, &receipt) {
		return bundle.OKFGenerated{}, false, nil
	}
	if receipt.SourceSHA256 != "" && receipt.SourceSHA256 != source {
		return bundle.OKFGenerated{}, false, bundle.ErrConflict
	}
	var raw sql.NullString
	err := q.QueryRowContext(ctx, `SELECT execution_snapshot_json FROM rendition_jobs WHERE job_id=$1`, build.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return bundle.OKFGenerated{}, false, err
	}
	if !raw.Valid {
		return bundle.OKFGenerated{}, false, nil
	}
	snapshot, valid := parseOKFSnapshot(raw.String)
	if !valid {
		return bundle.OKFGenerated{}, false, nil
	}
	profileRecord, err := loadProcessingProfile(ctx, q, profileID)
	missing := errors.Is(err, ErrNotFound)
	if missing {
		err = nil
	}
	if err != nil {
		return bundle.OKFGenerated{}, false, err
	}
	if missing {
		return bundle.OKFGenerated{}, false, nil
	}
	generated, valid := okfGeneratedProof(receipt, snapshot, profileRecord, build)
	return generated, valid, nil
}

func decodeOKFReceipt(raw []byte, receipt *document.RenditionReceipt) bool {
	return json.Unmarshal(raw, receipt) == nil
}
func parseOKFSnapshot(raw string) (document.RenditionExecutionSnapshotV1, bool) {
	snapshot, err := document.ParseRenditionExecutionSnapshotV1([]byte(raw))
	return snapshot, err == nil
}
func okfGeneratedProof(receipt document.RenditionReceipt, snapshot document.RenditionExecutionSnapshotV1, profileRecord ProcessingProfileRecord, build RenditionBuildRecord) (bundle.OKFGenerated, bool) {
	var profile document.ProcessingProfileV1
	if json.Unmarshal(profileRecord.CanonicalProfile, &profile, json.RejectUnknownMembers(true)) != nil || document.ValidateRenditionExecutionProfileV1(snapshot.Identity, profile) != nil || build.RenditionRequestFingerprint != profileRecord.RenditionRequestFingerprint {
		return bundle.OKFGenerated{}, false
	}
	auth := snapshot.Authorization
	fp, err := auth.Fingerprint()
	if err != nil {
		return bundle.OKFGenerated{}, false
	}
	data, err := json.Marshal(auth, json.Deterministic(true))
	if err != nil {
		return bundle.OKFGenerated{}, false
	}
	hash := sha256.Sum256(data)
	if receipt.ProviderID != auth.ProviderID || receipt.DescriptorFingerprint != auth.DescriptorFingerprint || receipt.PolicyFingerprint != auth.PolicyFingerprint || receipt.RenditionRequestFingerprint != auth.RenditionRequestFingerprint || receipt.SourceSHA256 != auth.SourceSHA256 || receipt.AuthorizationFingerprint != fp || hex.EncodeToString(hash[:]) != build.AuthorizationChecksum || receipt.RenditionRequestFingerprint != build.RenditionRequestFingerprint || receipt.OperationID != build.ProviderOperationID || receipt.CompletedAt != build.CompletedAt || !okfStableToken(receipt.ProviderID) || !okfStableToken(receipt.OperationID) {
		return bundle.OKFGenerated{}, false
	}
	const layout = "2006-01-02T15:04:05.000000000Z"
	start, e1 := time.Parse(layout, receipt.StartedAt)
	end, e2 := time.Parse(layout, receipt.CompletedAt)
	authorized, e3 := time.Parse(layout, auth.AuthorizedAt)
	expires, e4 := time.Parse(layout, auth.ExpiresAt)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || start.Format(layout) != receipt.StartedAt || end.Format(layout) != receipt.CompletedAt || end.Before(start) || start.Before(authorized) || !end.Before(expires) {
		return bundle.OKFGenerated{}, false
	}
	return bundle.OKFGenerated{By: "process:" + receipt.ProviderID, At: end.UTC().Format(time.RFC3339Nano)}, true
}
func okfStableToken(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}
