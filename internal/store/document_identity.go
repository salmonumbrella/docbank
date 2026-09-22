package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/docbank/document"
)

var (
	// ErrDocumentIdentityUnavailable means the persistent identity authority
	// has not been installed or does not contain the requested mapping.
	ErrDocumentIdentityUnavailable = errors.New("document identity is unavailable")
	// ErrPassageAuthorityUnavailable means an exact historical tuple is absent
	// or no longer retained. Callers must never replace it with a current head.
	ErrPassageAuthorityUnavailable = errors.New("passage authority is unavailable")
)

// DocumentIdentity binds one stable public UID to one vault-local node.
type DocumentIdentity struct {
	DocumentUID string
	NodeID      int64
}

// PassageAuthority is the complete retained catalog tuple needed before any
// passage bytes may be opened.
type PassageAuthority struct {
	Identity   DocumentIdentity
	Node       Node
	Path       string
	Version    ContentVersion
	Attachment RenditionAttachmentRecord
	Build      RenditionBuildRecord
	Artifact   RenditionArtifactRecord
	Fresh      bool
}

// EnsureDocumentIdentity returns a node's stable standalone identity,
// allocating it exactly once. The central schema owns the backing tables.
func (s *Store) EnsureDocumentIdentity(ctx context.Context, nodeID int64) (DocumentIdentity, error) {
	var identity DocumentIdentity
	err := s.withStorageTx(ctx, func(tx *sql.Tx) error {
		node, err := nodeByIDTx(tx, nodeID)
		if err != nil || node.IsDir() || node.TrashedAt != nil {
			return ErrDocumentIdentityUnavailable
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT document_uid,node_id FROM document_identities WHERE node_id=?`, nodeID,
		).Scan(&identity.DocumentUID, &identity.NodeID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return documentIdentitySchemaError(err)
		}
		uid, err := newUUIDv4()
		if err != nil {
			return fmt.Errorf("allocating document identity: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO document_identities(
			document_uid,node_id,created_at) VALUES(?,?,?)`, uid, nodeID, nowRFC3339()); err != nil {
			return documentIdentitySchemaError(err)
		}
		if err = tx.QueryRowContext(ctx,
			`SELECT document_uid,node_id FROM document_identities WHERE node_id=?`, nodeID,
		).Scan(&identity.DocumentUID, &identity.NodeID); err != nil {
			return documentIdentitySchemaError(err)
		}
		return nil
	})
	if err != nil {
		return DocumentIdentity{}, err
	}
	return identity, nil
}

// PutDocumentIdentityAlias maps an adopted federation identity to an existing
// standalone identity. Repeating an exact mapping is idempotent; retargeting
// an existing alias fails.
func (s *Store) PutDocumentIdentityAlias(
	ctx context.Context, domainUID, sourceVaultUID, sourceDocumentUID, localDocumentUID string,
) error {
	for name, value := range map[string]string{
		"domain UID": domainUID, "source vault UID": sourceVaultUID,
		"source document UID": sourceDocumentUID, "local document UID": localDocumentUID,
	} {
		if validateUUIDv4(value) != nil {
			return fmt.Errorf("document identity alias %s is invalid", name)
		}
	}
	return s.withStorageTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM document_identities WHERE document_uid=?)`, localDocumentUID).Scan(&exists); err != nil {
			return documentIdentitySchemaError(err)
		}
		if !exists {
			return ErrDocumentIdentityUnavailable
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO document_identity_aliases(
			domain_uid,source_vault_uid,source_document_uid,local_document_uid,mapped_at
		) VALUES(?,?,?,?,?)`, domainUID, sourceVaultUID, sourceDocumentUID, localDocumentUID, nowRFC3339())
		if err != nil {
			return documentIdentitySchemaError(err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 1 {
			return nil
		}
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT local_document_uid
			FROM document_identity_aliases WHERE domain_uid=? AND source_vault_uid=? AND source_document_uid=?`,
			domainUID, sourceVaultUID, sourceDocumentUID).Scan(&stored); err != nil {
			return documentIdentitySchemaError(err)
		}
		if stored != localDocumentUID {
			return errors.New("document identity alias already maps to another document")
		}
		return nil
	})
}

// ResolvePassageAuthority resolves only the exact retained tuple named by ref.
// It deliberately ignores rendition heads and a node's current version.
func (s *Store) ResolvePassageAuthority(
	ctx context.Context, ref document.PassageRefV1,
) (PassageAuthority, error) {
	if err := document.ValidatePassageAddressV1(ref); err != nil {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PassageAuthority{}, fmt.Errorf("starting passage authority snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var result PassageAuthority
	if ref.FederationDomainUID == "" {
		if ref.VaultUID != s.vaultID {
			return PassageAuthority{}, ErrPassageAuthorityUnavailable
		}
		err = tx.QueryRowContext(ctx, `SELECT document_uid,node_id FROM document_identities
			WHERE document_uid=?`, ref.DocumentUID).
			Scan(&result.Identity.DocumentUID, &result.Identity.NodeID)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT i.document_uid,i.node_id FROM document_identity_aliases a
			JOIN document_identities i ON i.document_uid=a.local_document_uid
			WHERE a.domain_uid=? AND a.source_vault_uid=? AND a.source_document_uid=?`,
			ref.FederationDomainUID, ref.VaultUID, ref.DocumentUID).
			Scan(&result.Identity.DocumentUID, &result.Identity.NodeID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	if err != nil {
		return PassageAuthority{}, documentIdentitySchemaError(err)
	}
	result.Node, err = nodeByIDTx(tx, result.Identity.NodeID)
	if err != nil || result.Node.IsDir() || result.Node.TrashedAt != nil {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	if err := document.ValidatePassageIdentityV1(ref); err != nil {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	result.Path, err = pathOf(ctx, tx, result.Node.ID)
	if err != nil {
		return PassageAuthority{}, passageAuthorityError(err)
	}
	result.Version, err = scanContentVersion(tx.QueryRowContext(ctx, `SELECT `+contentVersionCols+`
		FROM content_versions WHERE version_id=? AND node_id=?`, ref.ContentVersionID, result.Node.ID))
	if err != nil || result.Version.BlobHash != ref.SourceSHA256 {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	result.Attachment, err = loadRenditionAttachment(ctx, tx, ref.AttachmentID)
	if err != nil || result.Attachment.VaultID != s.vaultID ||
		result.Attachment.ContentVersionID != ref.ContentVersionID ||
		result.Attachment.BuildID != ref.RenditionBuildID {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	result.Build, err = loadRenditionBuild(ctx, tx, ref.RenditionBuildID)
	if err != nil || result.Build.VaultID != s.vaultID ||
		result.Build.VaultID != result.Attachment.VaultID ||
		result.Build.SourceSHA256 != ref.SourceSHA256 {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	for _, artifact := range result.Build.Artifacts {
		if artifact.Role == catalogArtifactSanitizedMarkdown &&
			artifact.State == RenditionArtifactVerified && artifact.BlobHash == artifact.Checksum &&
			artifact.BlobHash == result.Build.MarkdownChecksum {
			result.Artifact = artifact
			break
		}
	}
	if result.Artifact.ID == "" {
		return PassageAuthority{}, ErrPassageAuthorityUnavailable
	}
	if result.Node.CurrentVersionID == ref.ContentVersionID {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rendition_heads
			WHERE content_version_id=? AND attachment_id=?)`,
			ref.ContentVersionID, ref.AttachmentID).Scan(&result.Fresh); err != nil {
			return PassageAuthority{}, passageAuthorityError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PassageAuthority{}, fmt.Errorf("closing passage authority snapshot: %w", err)
	}
	return result, nil
}

func documentIdentitySchemaError(err error) error {
	return fmt.Errorf("%w: persistent document identity schema: %w", ErrDocumentIdentityUnavailable, err)
}

func passageAuthorityError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return ErrPassageAuthorityUnavailable
	}
	return fmt.Errorf("reading passage authority: %w", err)
}
