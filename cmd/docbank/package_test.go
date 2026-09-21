package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestPackagePreflightCLIUsesDaemonAndReturnsTypedResult(t *testing.T) {
	_ = setupVaultHome(t)
	root := t.TempDir()
	pdf, err := os.ReadFile(filepath.Join("..", "..", "document", "testdata", "scanassessment", "blank.pdf"))
	require.NoError(t, err)
	files := map[string][]byte{
		"VOL001/DATA/a.dat":        []byte("þDOCIDþ\x14þNATIVEþ\r\nþDOC-Aþ\x14þNATIVES/DOC-A.pdfþ\r\n"),
		"VOL001/DATA/a.opt":        []byte("DOC-A,VOL001,IMAGES/DOC-A.tif,Y,1,,\r\n"),
		"VOL001/IMAGES/DOC-A.tif":  []byte("synthetic-image"),
		"VOL001/NATIVES/DOC-A.pdf": pdf,
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, contents, 0o600))
	}
	out, err := runCLI(t, "package", "preflight", root, "--profile", "dat-concordance-v1", "--page-map-profile", "opt-pagecount5-v1", "--encoding", "utf-8", "--json")
	require.NoError(t, err)
	var result api.PackagePreflight
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, 1, result.Records)
	assert.False(t, result.Blocking)
	operation := uuid.NewString()
	out, err = runCLI(t, "package", "import", result.PreflightID, "--name", "synthetic-package",
		"--operation-id", operation, "--index-supplied-text", "--json")
	require.NoError(t, err)
	var job api.PackageImportJob
	require.NoError(t, json.Unmarshal([]byte(out), &job))
	assert.Equal(t, operation, job.OperationID)
	assert.Equal(t, 1, job.Total)
	out, err = runCLI(t, "package", "import", "status", operation, "--json")
	require.NoError(t, err)
	var status api.PackageImportJob
	require.NoError(t, json.Unmarshal([]byte(out), &status))
	assert.Equal(t, job.JobID, status.JobID)
}

func TestPackagePreflightCLIRequiresDeclaredCodec(t *testing.T) {
	_, err := runCLI(t, "package", "preflight", "container-id")
	require.ErrorContains(t, err, "--profile is required")
}

func TestPackagePreflightSourceMakesDirectoryAbsolute(t *testing.T) {
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	reference, err := packagePreflightSource(".")
	require.NoError(t, err)
	assert.Equal(t, workingDirectory, reference)
}

func TestPackagePreflightSourceRejectsMissingPathAndFile(t *testing.T) {
	root := t.TempDir()
	_, err := packagePreflightSource(filepath.Join(root, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
	path := filepath.Join(root, "source.txt")
	require.NoError(t, os.WriteFile(path, []byte("synthetic"), 0o600))
	_, err = packagePreflightSource(path)
	require.ErrorContains(t, err, "must be a directory")
}
