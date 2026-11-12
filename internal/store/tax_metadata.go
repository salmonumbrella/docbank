package store

import (
	"context"
	"errors"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/taxrecord"
)

type metadataTaxAccount struct {
	Type             string  `json:"type"`
	AccountID        string  `json:"account_id" db:"account_id"`
	FilerPartyID     string  `json:"filer_party_id" db:"filer_party_id"`
	AuthorityPartyID *string `json:"authority_party_id" db:"authority_party_id"`
	Country          string  `json:"country" db:"country"`
	Subdivision      string  `json:"subdivision" db:"subdivision"`
	TaxKind          string  `json:"tax_kind" db:"tax_kind"`
	TaxKindFolded    string  `json:"tax_kind_folded" db:"tax_kind_folded"`
	Identifier       string  `json:"identifier" db:"identifier"`
	Cadence          string  `json:"cadence" db:"cadence"`
	PeriodNote       string  `json:"period_note" db:"period_note"`
	DueNote          string  `json:"due_note" db:"due_note"`
	LeadDays         int64   `json:"lead_days" db:"lead_days"`
	Currency         string  `json:"currency" db:"currency"`
	EffectiveStart   *string `json:"effective_start" db:"effective_start"`
	EffectiveEnd     *string `json:"effective_end" db:"effective_end"`
	Note             string  `json:"note" db:"note"`
	Revision         int64   `json:"revision" db:"revision"`
	CreatedAt        string  `json:"created_at" db:"created_at"`
	UpdatedAt        string  `json:"updated_at" db:"updated_at"`
	RetiredAt        *string `json:"retired_at" db:"retired_at"`
}

var taxAccountMetadata = newMetadataTable(metadataTable[metadataTaxAccount]{record: metadataTaxAccount{Type: "tax_account"}, table: "tax_accounts", suffix: "ORDER BY account_id", validate: validateTaxAccountMetadata})

func validateTaxAccountMetadata(m metadataTaxAccount) error {
	if m.Type != "tax_account" || validateUUIDv4(m.AccountID) != nil || m.Revision < 1 {
		return errors.New("invalid tax account identity")
	}
	if validateUUIDv4(m.FilerPartyID) != nil {
		return errors.New("invalid tax reference")
	}
	if m.AuthorityPartyID != nil && validateUUIDv4(*m.AuthorityPartyID) != nil {
		return errors.New("invalid tax reference")
	}
	v := taxrecord.AccountValues{FilerPartyID: m.FilerPartyID, AuthorityPartyID: m.AuthorityPartyID, Country: m.Country, Subdivision: m.Subdivision, TaxKind: m.TaxKind, Identifier: m.Identifier, Cadence: m.Cadence, PeriodNote: m.PeriodNote, DueNote: m.DueNote, LeadDays: m.LeadDays, Currency: m.Currency, EffectiveStart: m.EffectiveStart, EffectiveEnd: m.EffectiveEnd, Note: m.Note}
	if err := taxrecord.ValidateAccount(v); err != nil {
		return err
	}
	if m.TaxKindFolded != document.FoldPersonName(m.TaxKind) {
		return errors.New("invalid tax folded kind")
	}
	return validateTaxMetadataTimes(m.CreatedAt, m.UpdatedAt, m.RetiredAt)
}

type metadataTaxRecord struct {
	Type             string  `json:"type"`
	RecordID         string  `json:"record_id" db:"record_id"`
	AccountID        *string `json:"account_id" db:"account_id"`
	FilerPartyID     string  `json:"filer_party_id" db:"filer_party_id"`
	AuthorityPartyID *string `json:"authority_party_id" db:"authority_party_id"`
	Kind             string  `json:"kind" db:"kind"`
	Title            string  `json:"title" db:"title"`
	Country          string  `json:"country" db:"country"`
	Subdivision      string  `json:"subdivision" db:"subdivision"`
	TaxKind          string  `json:"tax_kind" db:"tax_kind"`
	TaxKindFolded    string  `json:"tax_kind_folded" db:"tax_kind_folded"`
	FormCode         string  `json:"form_code" db:"form_code"`
	TaxYear          *int64  `json:"tax_year" db:"tax_year"`
	PeriodStart      *string `json:"period_start" db:"period_start"`
	PeriodEnd        *string `json:"period_end" db:"period_end"`
	DueDate          *string `json:"due_date" db:"due_date"`
	LeadDays         *int64  `json:"lead_days" db:"lead_days"`
	State            string  `json:"state" db:"state"`
	Direction        string  `json:"direction" db:"direction"`
	AmountMinor      *int64  `json:"amount_minor" db:"amount_minor"`
	Currency         string  `json:"currency" db:"currency"`
	Verification     string  `json:"verification" db:"verification"`
	VerifiedOn       *string `json:"verified_on" db:"verified_on"`
	VerifiedRevision *int64  `json:"verified_revision" db:"verified_revision"`
	VerificationNote string  `json:"verification_note" db:"verification_note"`
	Note             string  `json:"note" db:"note"`
	Revision         int64   `json:"revision" db:"revision"`
	CreatedAt        string  `json:"created_at" db:"created_at"`
	UpdatedAt        string  `json:"updated_at" db:"updated_at"`
	RetiredAt        *string `json:"retired_at" db:"retired_at"`
}

var taxRecordMetadata = newMetadataTable(metadataTable[metadataTaxRecord]{record: metadataTaxRecord{Type: "tax_record"}, table: "tax_records", suffix: "ORDER BY record_id", validate: validateTaxRecordMetadata})

func validateTaxRecordMetadata(m metadataTaxRecord) error {
	if m.Type != "tax_record" || validateUUIDv4(m.RecordID) != nil || m.Revision < 1 {
		return errors.New("invalid tax record identity")
	}
	if validateUUIDv4(m.FilerPartyID) != nil {
		return errors.New("invalid tax reference")
	}
	if m.AuthorityPartyID != nil && validateUUIDv4(*m.AuthorityPartyID) != nil {
		return errors.New("invalid tax reference")
	}
	if m.AccountID != nil && validateUUIDv4(*m.AccountID) != nil {
		return errors.New("invalid tax reference")
	}
	v := taxrecord.RecordValues{AccountID: m.AccountID, FilerPartyID: m.FilerPartyID, AuthorityPartyID: m.AuthorityPartyID, Kind: m.Kind, Title: m.Title, Country: m.Country, Subdivision: m.Subdivision, TaxKind: m.TaxKind, FormCode: m.FormCode, TaxYear: m.TaxYear, PeriodStart: m.PeriodStart, PeriodEnd: m.PeriodEnd, DueDate: m.DueDate, LeadDays: m.LeadDays, State: m.State, Direction: m.Direction, AmountMinor: m.AmountMinor, Currency: m.Currency, Verification: m.Verification, VerifiedOn: m.VerifiedOn, VerifiedRevision: m.VerifiedRevision, VerificationNote: m.VerificationNote, Note: m.Note}
	if err := taxrecord.ValidateRecord(v); err != nil {
		return err
	}
	if m.TaxKindFolded != document.FoldPersonName(m.TaxKind) {
		return errors.New("invalid tax folded kind")
	}
	return validateTaxMetadataTimes(m.CreatedAt, m.UpdatedAt, m.RetiredAt)
}

var taxMetadataTables = []metadataRecordCodec{taxAccountMetadata, taxRecordMetadata, taxEventMetadata, taxDocumentMetadata}

func validateTaxMetadataTimes(created, updated string, retired *string) error {
	if err := validateMetadataTime("tax created_at", created); err != nil {
		return err
	}
	if err := validateMetadataTime("tax updated_at", updated); err != nil {
		return err
	}
	if updated < created {
		return errors.New("tax update precedes creation")
	}
	if retired != nil {
		if err := validateMetadataTime("tax retired_at", *retired); err != nil {
			return err
		}
		if *retired < created || *retired > updated {
			return errors.New("invalid tax retirement time")
		}
	}
	return nil
}
func validateTaxMetadataState(ctx context.Context, q metadataQuerier, versions ...int) error {
	version := currentStorageSchemaVersion
	if len(versions) > 0 {
		version = versions[0]
	}
	codecs := taxMetadataTables
	if version < 52 {
		codecs = codecs[:len(codecs)-1]
	}
	for _, codec := range codecs {
		if err := codec.validateRows(ctx, q); err != nil {
			return err
		}
	}
	var invalid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tax_records r JOIN tax_accounts a ON a.account_id=r.account_id WHERE r.filer_party_id<>a.filer_party_id OR (r.authority_party_id IS NULL AND a.authority_party_id IS NOT NULL) OR (r.authority_party_id IS NOT NULL AND a.authority_party_id IS NULL) OR r.authority_party_id<>a.authority_party_id OR r.country<>a.country OR r.subdivision<>a.subdivision OR r.tax_kind<>a.tax_kind)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("tax record account identity mismatch")
	}
	// Validate retained authority even if an already-open database has lost
	// its unique index; startup checks columns, not index presence.
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tax_records WHERE account_id IS NOT NULL AND period_start IS NOT NULL AND period_end IS NOT NULL AND retired_at IS NULL GROUP BY account_id,period_start,period_end,kind HAVING COUNT(*)>1)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("duplicate tax record period")
	}
	if err := validateTaxEventAuthority(ctx, q); err != nil {
		return err
	}
	if version >= 52 {
		return validateTaxDocumentAuthority(ctx, q)
	}
	return nil
}

type metadataTaxEvent struct {
	Type           string  `json:"type"`
	EventID        string  `json:"event_id" db:"event_id"`
	RecordID       string  `json:"record_id" db:"record_id"`
	Kind           string  `json:"kind" db:"kind"`
	OccurredOn     string  `json:"occurred_on" db:"occurred_on"`
	SetsState      string  `json:"sets_state" db:"sets_state"`
	AmountMinor    *int64  `json:"amount_minor" db:"amount_minor"`
	Currency       string  `json:"currency" db:"currency"`
	Reference      string  `json:"reference" db:"reference"`
	Note           string  `json:"note" db:"note"`
	RecordedAt     string  `json:"recorded_at" db:"recorded_at"`
	VoidedAt       *string `json:"voided_at" db:"voided_at"`
	VoidReason     string  `json:"void_reason" db:"void_reason"`
	RecordRevision int64   `json:"record_revision" db:"record_revision"`
}

var taxEventMetadata = newMetadataTable(metadataTable[metadataTaxEvent]{record: metadataTaxEvent{Type: "tax_event"}, table: "tax_events", suffix: "ORDER BY record_id,record_revision", validate: validateTaxEventMetadata})

func validateTaxEventMetadata(m metadataTaxEvent) error {
	if m.Type != "tax_event" || validateUUIDv4(m.EventID) != nil || validateUUIDv4(m.RecordID) != nil || m.RecordRevision < 2 {
		return errors.New("invalid tax event identity")
	}
	if err := validateMetadataTime("tax event recorded_at", m.RecordedAt); err != nil {
		return err
	}
	if m.VoidedAt == nil {
		if m.VoidReason != "" {
			return errors.New("invalid tax event void")
		}
		return nil
	}
	if err := validateMetadataTime("tax event voided_at", *m.VoidedAt); err != nil {
		return err
	}
	if *m.VoidedAt < m.RecordedAt {
		return errors.New("tax event void precedes recording")
	}
	reason, err := taxrecord.NormalizeVoidReason(m.VoidReason)
	if err != nil || reason != m.VoidReason {
		return errors.New("invalid tax event void")
	}
	return nil
}

func validateTaxEventAuthority(ctx context.Context, q metadataQuerier) error {
	// Read each catalog once; state follows append revision, not event time.
	type authority struct {
		state    string
		revision int64
		derived  taxrecord.EventState
		record   taxrecord.RecordValues
	}
	parents := map[string]*authority{}
	parentRows, err := q.QueryContext(ctx, `SELECT record_id,kind,state,revision,direction,amount_minor,currency FROM tax_records`)
	if err != nil {
		return err
	}
	defer func() { _ = parentRows.Close() }()
	for parentRows.Next() {
		var id, kind string
		var p authority
		if err = parentRows.Scan(&id, &kind, &p.state, &p.revision, &p.record.Direction, &p.record.AmountMinor, &p.record.Currency); err != nil {
			return err
		}
		p.derived, err = taxrecord.NewEventState(kind)
		if err != nil {
			return err
		}
		parents[id] = &p
	}
	err = parentRows.Err()
	_ = parentRows.Close()
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT `+taxEventColumns+` FROM tax_events ORDER BY record_id,record_revision`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var previousID string
	var previousRevision int64
	var balanceFacts []taxrecord.EventFact
	for rows.Next() {
		event, err := scanTaxEvent(rows)
		if err != nil {
			return err
		}
		if previousID != "" && event.RecordID != previousID {
			if _, err = taxrecord.CalculateBalance(parents[previousID].record, balanceFacts); err != nil {
				return err
			}
			balanceFacts = nil
		}
		p := parents[event.RecordID]
		if p == nil || event.RecordRevision > p.revision {
			return errors.New("invalid tax event parent or revision")
		}
		// Protect export if an open SQLite table was rebuilt without its unique constraint.
		if event.RecordID == previousID && event.RecordRevision == previousRevision {
			return errors.New("duplicate tax event revision")
		}
		previousID, previousRevision = event.RecordID, event.RecordRevision
		fact := taxrecord.EventFact{EventValues: event.EventValues, RecordRevision: event.RecordRevision, Voided: event.VoidedAt != nil}
		balanceFacts = append(balanceFacts, taxrecord.EventFact{Kind: event.Kind, Currency: event.Currency, AmountMinor: event.AmountMinor, Voided: fact.Voided})
		if err = p.derived.Apply(fact); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if previousID != "" {
		if _, err = taxrecord.CalculateBalance(parents[previousID].record, balanceFacts); err != nil {
			return err
		}
	}
	for _, p := range parents {
		if p.state != p.derived.State {
			return &taxrecord.FieldError{Field: "state", Reason: "unsupported state"}
		}
	}
	return nil
}

type metadataTaxDocument struct {
	Type             string  `json:"type"`
	LinkID           string  `json:"link_id" db:"link_id"`
	RecordID         string  `json:"record_id" db:"record_id"`
	EventID          *string `json:"event_id" db:"event_id"`
	NodeID           *int64  `json:"node_id" db:"node_id"`
	ContentVersionID *string `json:"content_version_id" db:"content_version_id"`
	ContentSHA256    string  `json:"content_sha256" db:"content_sha256"`
	Role             string  `json:"role" db:"role"`
	FormCode         string  `json:"form_code" db:"form_code"`
	Note             string  `json:"note" db:"note"`
	RecordedAt       string  `json:"recorded_at" db:"recorded_at"`
	RetiredAt        *string `json:"retired_at" db:"retired_at"`
}

var taxDocumentMetadata = newMetadataTable(metadataTable[metadataTaxDocument]{record: metadataTaxDocument{Type: "tax_record_document"}, table: "tax_record_documents", suffix: "ORDER BY link_id", validate: validateTaxDocumentMetadata})

func validateTaxDocumentMetadata(m metadataTaxDocument) error {
	if m.Type != "tax_record_document" || validateUUIDv4(m.LinkID) != nil || validateUUIDv4(m.RecordID) != nil || m.NodeID != nil && *m.NodeID <= 0 {
		return errors.New("invalid tax document identity")
	}
	if err := taxrecord.ValidateDocument(taxrecord.DocumentValues{EventID: m.EventID, ContentVersionID: m.ContentVersionID, ContentSHA256: m.ContentSHA256, Role: m.Role, FormCode: m.FormCode, Note: m.Note}); err != nil {
		return err
	}
	if err := validateMetadataTime("tax document recorded_at", m.RecordedAt); err != nil {
		return err
	}
	if m.RetiredAt != nil {
		if err := validateMetadataTime("tax document retired_at", *m.RetiredAt); err != nil {
			return err
		}
		if *m.RetiredAt < m.RecordedAt {
			return errors.New("tax document retirement precedes recording")
		}
	}
	return nil
}
func validateTaxDocumentAuthority(ctx context.Context, q metadataQuerier) error {
	var bad bool
	for _, query := range []string{
		`SELECT EXISTS(SELECT 1 FROM tax_record_documents d LEFT JOIN tax_events e ON e.event_id=d.event_id WHERE d.event_id IS NOT NULL AND (e.event_id IS NULL OR e.record_id<>d.record_id))`,
		`SELECT EXISTS(SELECT 1 FROM tax_record_documents d LEFT JOIN nodes n ON n.id=d.node_id WHERE d.node_id IS NOT NULL AND (n.id IS NULL OR n.kind<>'file'))`,
		`SELECT EXISTS(SELECT 1 FROM tax_record_documents d JOIN content_versions v ON v.version_id=d.content_version_id WHERE d.node_id IS NULL OR v.node_id<>d.node_id OR v.blob_hash<>d.content_sha256)`,
		`SELECT EXISTS(SELECT 1 FROM tax_record_documents WHERE retired_at IS NULL AND node_id IS NOT NULL AND event_id IS NULL GROUP BY record_id,node_id,role HAVING COUNT(*)>1)`,
		`SELECT EXISTS(SELECT 1 FROM tax_record_documents WHERE retired_at IS NULL AND node_id IS NOT NULL AND event_id IS NOT NULL GROUP BY record_id,node_id,role,event_id HAVING COUNT(*)>1)`,
	} {
		if err := q.QueryRowContext(ctx, query).Scan(&bad); err != nil {
			return err
		}
		if bad {
			return errors.New("invalid tax document authority")
		}
	}
	return nil
}
