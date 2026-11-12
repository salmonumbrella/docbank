package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaxLinkCommandFlags(t *testing.T) {
	cmd := newTaxLinkCommand()
	add, _, err := cmd.Find([]string{"add"})
	require.NoError(t, err)
	for _, name := range []string{"node-id", "role", "event-id", "content-version-id", "form-code", "note", "revision"} {
		require.NotNil(t, add.Flags().Lookup(name), name)
	}
	remove, _, err := cmd.Find([]string{"remove"})
	require.NoError(t, err)
	require.NotNil(t, remove.Flags().Lookup("revision"))
}
