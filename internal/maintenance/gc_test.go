package maintenance

import (
	"bytes"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/packstore"

	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
)

func TestRetireLooseCandidatesContinuesAfterMissingLocation(t *testing.T) {
	locations := []packstore.ReadLocation{
		{
			StoreID: "missing", Loose: &packstore.LooseLocation{
				Encoding: packstore.LooseEncodingRaw,
			},
		},
		{
			StoreID: "present", Loose: &packstore.LooseLocation{
				Encoding: packstore.LooseEncodingZstd,
			},
		},
	}
	var retired []packstore.StoreID
	count, err := retireLooseCandidates(locations, func(location packstore.ReadLocation) error {
		retired = append(retired, location.StoreID)
		if location.StoreID == "missing" {
			return fs.ErrNotExist
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, []packstore.StoreID{"missing", "present"}, retired)
}

func TestPurgeDerivativesRunsLocationAwarePhysicalGC(t *testing.T) {
	// Mutation caught: stopping after catalog-manifest deletion leaves sensitive
	// loose provider output in the live vault instead of completing physical GC.
	root := t.TempDir()
	metadata, err := store.Open(filepath.Join(root, "metadata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metadata.Close()) })
	blobRoot := filepath.Join(root, "blobs")
	blobs, err := blob.New(store.NewPackCatalog(metadata), blobRoot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blobs.Close()) })
	written, err := blobs.WriteDetailedContext(
		t.Context(), bytes.NewReader([]byte("abandoned synthetic provider output")),
	)
	require.NoError(t, err)
	encoding, err := written.EncodingName()
	require.NoError(t, err)
	require.NoError(t, metadata.RecordRenditionBlob(t.Context(), written.Hash, written.Size,
		store.BlobPhysical{
			Encoding: encoding, StoredBytes: written.StoredSize,
			PackEligible: written.PackEligible, Created: written.Created,
		}))
	path := filepath.Join(blobRoot, written.Hash[:2], written.Hash)
	require.FileExists(t, path)

	report, err := PurgeDerivatives(t.Context(), metadata, blobs, store.PurgeRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Physical.CandidateBlobs)
	assert.Equal(t, 1, report.Physical.ReclaimedFiles)
	assert.Equal(t, 1, report.Physical.RemovedBlobs)
	assert.True(t, report.Purge.ImmutableBackupCopiesUntouched)
	assert.NoFileExists(t, path)
	recorded, err := metadata.HasBlob(t.Context(), written.Hash)
	require.NoError(t, err)
	assert.False(t, recorded)
}
