package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestR1GenerateSchemaContract(t *testing.T) {
	db := schemaContractDB(t, DefaultSQLiteDriver(), schemaSQL)
	digest := schemaCatalogDigest(t, db)
	indexes := schemaIndexDigests(t, db)
	names := make([]string, 0, len(indexes))
	for name := range indexes {
		names = append(names, name)
	}
	slices.Sort(names)
	var body strings.Builder
	for _, name := range names {
		fmt.Fprintf(&body, "%s %s\n", name, indexes[name])
	}
	dir := filepath.Join("testdata", "schema-contracts")
	require.NoError(t, os.MkdirAll(dir, 0755))
	path := filepath.Join(dir, schemaContractFile(schemaContract{Version: currentStorageSchemaVersion, Digest: digest}, "record-types"))
	require.NoError(t, os.WriteFile(path, []byte(body.String()), 0644))
	t.Logf("contract: %s", path)
}
