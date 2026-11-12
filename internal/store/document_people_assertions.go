package store

import (
	"context"
	"database/sql"
	"errors"

	"go.kenn.io/docbank/document"
)

const maxDocumentPersonChanges = 32

type DocumentPersonChange struct {
	PersonID, Role, Action, Note string
	Revision                     int64
}

type DocumentPeopleWrite struct {
	Facts DocumentPeopleFacts
	Undo  []DocumentPersonChange
}

// AssertDocumentPeople applies revision-fenced decisions and returns their actual inverse.
func (s *Store) AssertDocumentPeople(ctx context.Context, versionID string, changes []DocumentPersonChange) (DocumentPeopleWrite, error) {
	var out DocumentPeopleWrite
	if len(changes) == 0 || len(changes) > maxDocumentPersonChanges {
		return out, ErrInvalidPerson
	}
	err := s.withLogicalTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.factsVersionTx(ctx, tx, versionID); err != nil {
			return err
		}
		if err := requireCandidateVersionTx(ctx, tx, versionID); err != nil {
			return err
		}
		assertions := make([]PersonDocumentAssertion, len(changes))
		ids := make([]string, 0, len(changes))
		pairs := make(map[string]bool, len(changes))
		for i, c := range changes {
			a := PersonDocumentAssertion{ContentVersionID: versionID, PersonID: c.PersonID, Role: c.Role, Action: c.Action, Note: c.Note, Revision: c.Revision}
			if c.Role == personAssertionRoleDepicted {
				return ErrInvalidPerson
			}
			if err := validatePersonAssertion(a); err != nil {
				return err
			}
			id, err := resolveActiveCandidatePersonTx(ctx, tx, a.PersonID)
			if err != nil {
				return err
			}
			a.PersonID = id
			key := id + "\x00" + a.Role
			if pairs[key] {
				return ErrInvalidPerson
			}
			pairs[key] = true
			assertions[i] = a
			ids = append(ids, id)
		}
		if err := s.fenceVisiblePeopleTx(ctx, tx, ids...); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_document_assertions WHERE content_version_id=$1`, versionID).Scan(&count); err != nil {
			return err
		}
		added := 0
		for _, a := range assertions {
			_, err := personAssertionByIdentityTx(ctx, tx, versionID, a.PersonID, a.Role)
			if errors.Is(err, sql.ErrNoRows) {
				added++
			} else if err != nil {
				return err
			}
		}
		if added > 0 && count+added > document.MaxPersonEdgesPerVersion+1 {
			return ErrInvalidPerson
		}
		out.Undo = make([]DocumentPersonChange, 0, len(changes))
		for _, a := range assertions {
			saved, prior, err := s.assertDocumentPersonTx(ctx, tx, a)
			if err != nil {
				return err
			}
			inverse := DocumentPersonChange{PersonID: saved.PersonID, Role: saved.Role, Action: personAssertionActionWithdrawn, Revision: saved.Revision}
			if prior != nil {
				inverse.Action, inverse.Note = prior.Action, prior.Note
			}
			if saved.Role != "blind_copy" {
				out.Undo = append(out.Undo, inverse)
			}
		}
		if err := advancePersonBindingEpochTx(ctx, tx); err != nil {
			return err
		}
		var err error
		out.Facts, err = s.documentPeopleFactsTx(ctx, tx, versionID, true)
		return err
	})
	if err != nil {
		return DocumentPeopleWrite{}, err
	}
	return out, nil
}
