package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/recordtype"
)

func TestRecordAssignmentsRejectRepeatedKeys(t *testing.T) {
	def, _ := recordtype.Builtin("contract")
	_, err := recordAssignments(def, []string{"contract_type=service", "contract_type=lease"}, nil)
	require.Error(t, err)
	_, err = recordAssignments(def, []string{"term.end=2027-01-01"}, []string{"term.end"})
	require.Error(t, err)
}
func TestRecordAssignmentsDateEndpointDelta(t *testing.T) {
	def, _ := recordtype.Builtin("contract")
	fields, err := recordAssignments(def, []string{"term.end=2027-01-01"}, []string{"term.start"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"term": map[string]any{"start": nil, "end": "2027-01-01"}}, fields)
}
