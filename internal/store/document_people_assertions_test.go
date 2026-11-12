package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
)

func checkAssertDocumentPeopleAtomicUndo(t *testing.T, newStore func(*testing.T) *Store) {
	t.Helper()
	s := newStore(t)
	_, version := seedPeopleVersion(t, s)
	a, err := s.CreatePerson(t.Context(), "Synthetic A", "operator")
	require.NoError(t, err)
	b, err := s.CreatePerson(t.Context(), "Synthetic B", "operator")
	require.NoError(t, err)
	change := DocumentPersonChange{PersonID: a.PersonID, Role: "author", Action: "suppress", Note: "prior", Revision: 1}
	_, err = s.AssertDocumentPerson(t.Context(), PersonDocumentAssertion{ContentVersionID: version, PersonID: a.PersonID, Role: "author", Action: "suppress", Note: "prior", Revision: 1})
	require.NoError(t, err)
	var before int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT binding_epoch FROM document_people_state WHERE singleton=1`).Scan(&before))
	change.Action, change.Note = "assert", ""
	out, err := s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{change})
	require.NoError(t, err)
	var after int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT binding_epoch FROM document_people_state WHERE singleton=1`).Scan(&after))
	require.Equal(t, before+1, after)
	require.Equal(t, []DocumentPersonChange{{PersonID: a.PersonID, Role: "author", Action: "suppress", Note: "prior", Revision: 2}}, out.Undo)
	_, err = s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{{PersonID: b.PersonID, Role: "author", Action: "assert", Revision: 1}, change})
	require.ErrorIs(t, err, ErrStaleRevision)
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT binding_epoch FROM document_people_state WHERE singleton=1`).Scan(&before))
	require.Equal(t, after, before)
	rows, err := s.DocumentPersonAssertions(t.Context(), version)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(2), rows[0].Revision)
	restored, err := s.AssertDocumentPeople(t.Context(), version, out.Undo)
	require.NoError(t, err)
	require.Equal(t, "suppress", restored.Facts.Assertions[0].Action)
	require.Equal(t, "prior", restored.Facts.Assertions[0].Note)
	_, err = s.AssertDocumentPeople(t.Context(), version, out.Undo)
	require.ErrorIs(t, err, ErrStaleRevision)
}

func TestValidatePersonAssertionWithdrawn(t *testing.T) {
	t.Parallel()
	roles := append(document.AllEventRoles(), document.EventRole("speaker"), document.EventRole("depicted"))
	for _, role := range roles {
		a := PersonDocumentAssertion{ContentVersionID: "00000000-0000-4000-8000-000000000001", PersonID: "00000000-0000-4000-8000-000000000002", Role: string(role), Action: "withdrawn", Revision: 1}
		require.NoError(t, validatePersonAssertion(a))
		a.Note = strings.Repeat("x", 1001)
		require.ErrorIs(t, validatePersonAssertion(a), ErrInvalidPerson)
	}
}

func checkAssertDocumentPeopleRejects(t *testing.T, newStore func(*testing.T) *Store) {
	t.Helper()
	s := newStore(t)
	_, version := seedPeopleVersion(t, s)
	person, err := s.CreatePerson(t.Context(), "Synthetic person", "operator")
	require.NoError(t, err)
	c := DocumentPersonChange{PersonID: person.PersonID, Role: "author", Action: "assert", Revision: 1}
	_, err = s.AssertDocumentPeople(t.Context(), version, nil)
	require.ErrorIs(t, err, ErrInvalidPerson)
	many := make([]DocumentPersonChange, 33)
	for i := range many {
		many[i] = c
	}
	_, err = s.AssertDocumentPeople(t.Context(), version, many)
	require.ErrorIs(t, err, ErrInvalidPerson)
	_, err = s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{c, c})
	require.ErrorIs(t, err, ErrInvalidPerson)
	c.Role = "depicted"
	_, err = s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{c})
	require.ErrorIs(t, err, ErrInvalidPerson)
	rows, err := s.DocumentPersonAssertions(t.Context(), version)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func checkAssertDocumentPeopleCapacity(t *testing.T, newStore func(*testing.T) *Store) {
	t.Helper()
	s := newStore(t)
	_, version := seedPeopleVersion(t, s)
	roles := append(document.AllEventRoles(), document.EventRole("speaker"), document.EventRole("depicted"))
	const first = "00000000-0000-4000-8001-000000000001"
	err := s.withLogicalTx(t.Context(), func(tx *sql.Tx) error {
		for i := 0; i < document.MaxPersonEdgesPerVersion+1; i++ {
			personID := fmt.Sprintf("00000000-0000-4000-8001-%012x", i/len(roles)+1)
			if i%len(roles) == 0 {
				if _, err := createPersonWithIDTx(t.Context(), tx, personID, "Synthetic capacity person", "operator"); err != nil {
					return err
				}
			}
			assertionID := fmt.Sprintf("00000000-0000-4000-8002-%012x", i+1)
			action := "withdrawn"
			if i%2 == 0 {
				action = "suppress"
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO person_document_assertions(assertion_id,content_version_id,person_id,role,action,note,recorded_at,revision) VALUES($1,$2,$3,$4,$5,'','2026-01-01T00:00:00Z',1)`, assertionID, version, personID, string(roles[i%len(roles)]), action); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	extra, err := s.CreatePerson(t.Context(), "Synthetic extra", "operator")
	require.NoError(t, err)
	var before, after int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT binding_epoch FROM document_people_state WHERE singleton=1`).Scan(&before))
	_, err = s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{{PersonID: extra.PersonID, Role: "author", Action: "assert", Revision: 1}})
	require.ErrorIs(t, err, ErrInvalidPerson)
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT binding_epoch FROM document_people_state WHERE singleton=1`).Scan(&after))
	require.Equal(t, before, after)
	_, err = s.AssertDocumentPeople(t.Context(), version, []DocumentPersonChange{{PersonID: first, Role: "author", Action: "assert", Revision: 1}})
	require.NoError(t, err)
}
