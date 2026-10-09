// Package push streams a read-only local folder to an explicitly selected
// keyed daemon. The server's source cursor is the only resumability authority.
package push

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/ingest"
	"go.kenn.io/docbank/internal/store"
)

// Options defines one folder push. Watch uses the watched-inbox stability
// defaults and rules; a one-shot run processes current files immediately.
type Options struct {
	Folder     config.WatchConfig
	Duplicates string
	Watch      bool
	Progress   func(ref, outcome string) error
}

// Report counts only independently acknowledged file outcomes. Failed runs
// return their partial report and an error; callers may safely run again.
type Report struct {
	Added            int
	Updated          int
	Linked           int
	Skipped          int
	DuplicateSkipped int
}

// Run executes a folder push without discovering or starting a local daemon.
func Run(ctx context.Context, connection *daemonconn.Connection, opts Options) (Report, error) {
	var report Report
	if connection == nil {
		return report, errors.New("push requires a daemon connection")
	}
	if err := store.ValidatePushSource(store.PushSource{Name: opts.Folder.Name, Ref: "file", Duplicates: opts.Duplicates}); err != nil {
		return report, err
	}
	scanner, err := ingest.NewFolderScanner(opts.Folder, func(ctx context.Context, file ingest.FolderFile) error {
		if file.Info.Size() > blob.MaxIngestBytes {
			return fmt.Errorf("source exceeds upload limit of %d bytes", blob.MaxIngestBytes)
		}
		hash := sha256.New()
		if n, err := io.Copy(hash, contextReader{ctx: ctx, reader: file.File}); err != nil {
			return fmt.Errorf("hashing source: %w", err)
		} else if n != file.Info.Size() {
			return ingest.ErrSourceChanged
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		after, err := file.File.Stat()
		if err != nil {
			return fmt.Errorf("checking hashed source: %w", err)
		}
		if after.Size() != file.Info.Size() || !after.ModTime().Equal(file.Info.ModTime()) {
			return ingest.ErrSourceChanged
		}
		state, err := connection.API().GetPushSource(ctx, &apiclient.GetPushSourceRequestOptions{Query: &apiclient.GetPushSourceQuery{PushName: opts.Folder.Name, SourceRef: file.Ref}})
		if err != nil {
			return err
		}
		if state.Known && (state.Node == nil || state.Node.ID <= 0 || state.Node.Kind != "file" || state.Node.Revision <= 0 || state.Node.ParentID == nil || *state.Node.ParentID <= 0 || state.Node.Name == "" || state.Node.CurrentVersionID == "" || state.Node.TrashedAt != "" || state.Hash == "" || state.Size < 0) {
			return errors.New("daemon returned an invalid push source cursor")
		}
		outcome := "skipped"
		if !state.Known || state.Hash != digest || state.Size != file.Info.Size() {
			var parentID int64
			uploadName := path.Base(file.Ref)
			if state.Known {
				parentID, uploadName = *state.Node.ParentID, state.Node.Name
			} else {
				parent, err := ensureDirectory(ctx, connection, path.Join(opts.Folder.Destination, path.Dir(file.Ref)))
				if err != nil {
					return err
				}
				parentID = parent.ID
			}
			if _, err := file.File.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("rewinding source: %w", err)
			}
			head := make([]byte, 512)
			n, err := file.File.Read(head)
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("reading file type: %w", err)
			}
			if _, err := file.File.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("rewinding source: %w", err)
			}
			receipt, err := connection.PushUpload(ctx, parentID, uploadName, ingest.DetectMIME(file.Ref, head[:n]), digest, file.Info.Size(), contextReader{ctx: ctx, reader: file.File}, store.PushSource{Name: opts.Folder.Name, Ref: file.Ref, ModifiedAt: file.Info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z"), Duplicates: opts.Duplicates})
			if err != nil {
				return err
			}
			outcome = receipt.Status
		}
		switch outcome {
		case "added":
			report.Added++
		case "updated":
			report.Updated++
		case "linked":
			report.Linked++
		case "skipped":
			report.Skipped++
		case "duplicate_skipped":
			report.DuplicateSkipped++
		}
		if opts.Progress != nil {
			return opts.Progress(file.Ref, outcome)
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	if opts.Watch {
		err = scanner.Run(ctx)
	} else {
		err = scanner.ScanOnce(ctx)
	}
	return report, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		err = r.ctx.Err()
	}
	return n, err
}

// Resolve every component through HTTP and create only missing directories.
// A concurrent creator may win; re-resolve that component after its conflict.
func ensureDirectory(ctx context.Context, connection *daemonconn.Connection, destination string) (api.Node, error) {
	var node api.Node
	current := "/"
	components := strings.Split(strings.Trim(destination, "/"), "/")
	if destination == "/" {
		components = nil
	}
	for _, component := range append([]string{""}, components...) {
		current = path.Join(current, component)
		resolved, err := connection.API().ResolvePath(ctx, &apiclient.ResolvePathRequestOptions{Query: &apiclient.ResolvePathQuery{Path: current}})
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return api.Node{}, err
			}
			created, createErr := connection.MkdirPath(ctx, current)
			if createErr != nil {
				if !errors.Is(createErr, store.ErrExists) {
					return api.Node{}, createErr
				}
				resolved, err = connection.API().ResolvePath(ctx, &apiclient.ResolvePathRequestOptions{Query: &apiclient.ResolvePathQuery{Path: current}})
				if err != nil {
					return api.Node{}, err
				}
			} else {
				resolved = &created
			}
		}
		if resolved == nil || resolved.ID <= 0 || resolved.Kind != "dir" || resolved.TrashedAt != "" {
			return api.Node{}, errors.New("push destination must resolve to a live directory")
		}
		node = *resolved
	}
	return node, nil
}
