package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordAssignmentsRejectRepeatedKeys(t *testing.T) {
	_, err := recordAssignments([]string{"contract_type=service", "contract_type=lease"}, nil, nil)
	require.Error(t, err)
	_, err = recordAssignments([]string{"term.end=2027-01-01"}, []string{"term.end"}, nil)
	require.Error(t, err)
}
func TestRecordAssignmentsPreserveOtherDateEndpoint(t *testing.T) {
	fields, err := recordAssignments([]string{"term.end=2027-01-01"}, []string{"term.start"}, map[string]any{"term": map[string]any{"start": "2026-01-01", "end": "2026-12-31"}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"term": map[string]any{"start": nil, "end": "2027-01-01"}}, fields)
}
