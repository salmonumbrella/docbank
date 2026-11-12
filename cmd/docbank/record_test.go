package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/recordtype"
)

func TestRecordAssignmentsRejectRepeatedKeys(t *testing.T) {
	def, _ := recordtype.Builtin("contract")
	_, err := recordAssignments(def, []string{"contract_type=service", "contract_type=lease"}, nil, nil)
	require.Error(t, err)
	_, err = recordAssignments(def, []string{"term.end=2027-01-01"}, []string{"term.end"}, nil)
	require.Error(t, err)
}
func TestRecordAssignmentsPreserveOtherDateEndpoint(t *testing.T) {
	def, _ := recordtype.Builtin("contract")
	fields, err := recordAssignments(def, []string{"term.end=2027-01-01"}, []string{"term.start"}, map[string]any{"term": map[string]any{"start": "2026-01-01", "end": "2026-12-31"}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"term": map[string]any{"start": nil, "end": "2027-01-01"}}, fields)
}
