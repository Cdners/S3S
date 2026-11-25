// Package transfer owns durable file transfers. Callers supply a client bound to
// the indicated connection; object bytes never cross a desktop IPC boundary.
package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

const (
	identityError    core.ErrorCode = "error.downloadIdentity"
	rangeError       core.ErrorCode = "error.downloadRange"
	fileError        core.ErrorCode = "error.downloadFile"
	destinationError core.ErrorCode = "error.downloadDestination"
)

type Source interface {
	Head(ctx context.Context, bucket, key string) (core.ObjectIdentity, error)
	// Implementations condition the request on identity and verify HTTP status,
	// Content-Range, length and validators before returning the body.
	ReadRange(ctx context.Context, bucket, key string, identity core.ObjectIdentity, offset, length int64) (io.ReadCloser, error)
}

type Engine struct {
	db        *store.Store
	rangeSize int64
}

func New(db *store.Store) *Engine { return &Engine{db: db, rangeSize: 8 << 20} }

func (e *Engine) Recover(ctx context.Context) error {
	if err := e.db.RecoverDownloads(ctx); err != nil {
		return err
	}
	pending, err := e.db.PendingCleanup(ctx)
	if err != nil {
		return err
	}
	for _, p := range pending {
		e.cleanup(ctx, p.ID, p.Path, p.Destination, p.FileID)
	}
	return nil
}

func (e *Engine) Enqueue(ctx context.Context, c core.Connection, source Source, bucket, key, destination string) (core.Task, error) {
	if !filepath.IsAbs(destination) || !filepath.IsLocal(filepath.Base(destination)) || filepath.Base(destination) == "." || bucket == "" || key == "" {
		return core.Task{}, core.NewError(destinationError)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return core.Task{}, core.NewError(destinationError)
	}
	destination = filepath.Join(dir, filepath.Base(destination))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return core.Task{}, core.NewError(destinationError)
	}
	identity, err := source.Head(ctx, bucket, key)
	if err != nil {
		return core.Task{}, err
	}
	strongETag := len(identity.ETag) > 2 && strings.HasPrefix(identity.ETag, "\"") && strings.HasSuffix(identity.ETag, "\"")
	if identity.Size < 0 || !strongETag && (identity.VersionID == "" || identity.VersionID == "null") {
		return core.Task{}, core.NewError(identityError)
	}
	path, fileID, err := newPartial(dir)
	if err != nil {
		return core.Task{}, err
	}
	task := core.Task{ID: rand.Text(), ConnectionID: c.ID, Direction: core.Download, Bucket: bucket, ObjectKey: key,
		LocalPath: destination, Size: identity.Size, State: core.Queued, Resumable: true}
	manifest := store.Download{Provider: c.Provider, Region: c.Region, Endpoint: c.Endpoint, Identity: identity, PartialPath: path, FileID: fileID, RangeSize: e.rangeSize}
	if err = e.db.CreateDownload(ctx, task, manifest); err != nil {
		os.Remove(path)
		return core.Task{}, err
	}
	return task, nil
}

func newPartial(dir string) (string, string, error) {
	f, err := os.CreateTemp(dir, ".s3s-*.part")
	if err != nil {
		return "", "", core.NewError(fileError)
	}
	path := f.Name()
	id, _, statErr := fileIdentity(f)
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || syncDirectory(dir) != nil {
		os.Remove(path)
		return "", "", core.NewError(fileError)
	}
	return path, id, nil
}

// Run is synchronous. Cancellation pauses the worker after its last durable
// checkpoint. A repeated call claims queued, paused or failed downloads only.
func (e *Engine) Run(ctx context.Context, id string, c core.Connection, source Source) error {
	if err := e.db.ClaimDownload(ctx, id); err != nil {
		return err
	}
	return e.RunClaimed(ctx, id, c, source)
}

// RunClaimed executes exactly one task already claimed by Store.ClaimDownload.
// A desktop caller can claim under its shutdown lock before starting a goroutine.
func (e *Engine) RunClaimed(ctx context.Context, id string, c core.Connection, source Source) error {
	t, err := e.db.Task(ctx, id)
	if err == nil && (t.State != core.Running || !t.Resumable) {
		return core.NewError(core.Conflict)
	}
	var d store.Download
	if err == nil {
		d, err = e.db.Download(ctx, id)
	}
	if err == nil {
		if c.ID != t.ConnectionID || c.Provider != d.Provider || c.Region != d.Region || c.Endpoint != d.Endpoint {
			err = core.NewError(identityError)
		} else {
			err = e.transfer(ctx, source, t, &d)
		}
	}
	// A canceled request context must not prevent persisting its paused state.
	state := core.Completed
	var detail *core.Error
	if err != nil {
		if ctx.Err() != nil {
			state = core.Paused
			err = ctx.Err()
		} else {
			state = core.Failed
			if !errors.As(err, &detail) {
				detail = core.NewError(fileError)
			}
			err = detail
		}
	}
	if finish := e.db.FinishDownload(context.WithoutCancel(ctx), id, state, detail); finish != nil {
		return finish
	}
	if state == core.Completed {
		e.cleanup(context.WithoutCancel(ctx), id, d.PartialPath, t.LocalPath, d.FileID)
	}
	return err
}

// A crash can separate the completion commit from unlinking the private name.
// Unlink only a regular file still referring to the published destination.
func (e *Engine) cleanup(ctx context.Context, id, partial, dest, fileID string) {
	p, err := os.Lstat(partial)
	if errors.Is(err, os.ErrNotExist) {
		_ = e.db.CleanupDone(ctx, id)
		return
	}
	d, destErr := os.Lstat(dest)
	if err != nil || destErr != nil || !p.Mode().IsRegular() || !os.SameFile(p, d) {
		return
	}
	f, err := os.Open(partial)
	if err != nil {
		return
	}
	idOnDisk, _, err := fileIdentity(f)
	_ = f.Close()
	if err != nil || idOnDisk != fileID {
		return
	}
	if os.Remove(partial) == nil && syncDirectory(filepath.Dir(partial)) == nil {
		_ = e.db.CleanupDone(ctx, id)
	}

}

func remoteMatches(ctx context.Context, source Source, t core.Task, want core.ObjectIdentity) error {
	got, err := source.Head(ctx, t.Bucket, t.ObjectKey)
	if err != nil {
		return err
	}
	if got != want {
		return core.NewError(identityError)
	}
	return nil
}

func (e *Engine) transfer(ctx context.Context, source Source, t core.Task, d *store.Download) error {
	dir := filepath.Dir(t.LocalPath)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir || filepath.Dir(d.PartialPath) != dir {
		return core.NewError(destinationError)
	}
	parts, err := e.db.Parts(ctx, t.ID)
	if err != nil {
		return err
	}
	existing := make(map[int]core.Part, len(parts))
	for _, p := range parts {
		if p.Offset >= t.Size || p.Offset%d.RangeSize != 0 || int64(p.Number-1) != p.Offset/d.RangeSize ||
			p.Size != min(d.RangeSize, t.Size-p.Offset) || len(p.Checksum) != sha256.Size*2 {
			return core.NewError(core.InvalidPart)
		}
		existing[p.Number] = p
	}
	// A temporary file may disappear after a power loss or external cleanup.
	// Recreate only when the destination is still unused, then rehash/refetch.
	tmpInfo, err := os.Lstat(d.PartialPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, destErr := os.Lstat(t.LocalPath); !errors.Is(destErr, os.ErrNotExist) {
			return core.NewError(destinationError)
		}
		path, id, createErr := newPartial(dir)
		if createErr != nil {
			return createErr
		}
		if err = e.db.ReplaceMissingPartial(ctx, t.ID, d.PartialPath, path, id); err != nil {
			os.Remove(path)
			return err
		}
		d.PartialPath, d.FileID = path, id
		tmpInfo, err = os.Lstat(d.PartialPath)
	}
	if err != nil || !tmpInfo.Mode().IsRegular() {
		return core.NewError(fileError)
	}
	dest, err := os.Lstat(t.LocalPath)
	published := err == nil && os.SameFile(dest, tmpInfo)
	if !published && !errors.Is(err, os.ErrNotExist) {
		return core.NewError(destinationError)
	}
	if !published {
		if err := remoteMatches(ctx, source, t, d.Identity); err != nil {
			return err
		}
	}
	flags := os.O_RDWR
	if published {
		flags = os.O_RDONLY
	}
	f, err := os.OpenFile(d.PartialPath, flags, 0)
	if err != nil {
		return core.NewError(fileError)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(tmpInfo, info) {
		return core.NewError(fileError)
	}
	localID, links, err := fileIdentity(f)
	if err != nil || localID != d.FileID {
		return core.NewError(fileError)
	}
	if !published && !writableLinks(d.PartialPath, tmpInfo, links) {
		return core.NewError(fileError)
	}
	if published && info.Size() != t.Size {
		return core.NewError(destinationError)
	}
	for number, offset := 1, int64(0); offset < t.Size; number++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		length := min(d.RangeSize, t.Size-offset)
		p, ok := existing[number]
		if !ok || !matchesFile(f, p) {
			// A published file is read-only to the recovery path, even if its bytes changed.
			if published {
				return core.NewError(destinationError)
			}
			if err = e.fetch(ctx, f, t, *d, number, offset, length, source); err != nil {
				return err
			}
		}
		offset += length
	}
	if !published {
		if err = f.Truncate(t.Size); err != nil {
			return core.NewError(fileError)
		}
		if err = f.Sync(); err != nil {
			return core.NewError(fileError)
		}

		if err = remoteMatches(ctx, source, t, d.Identity); err != nil {
			return err
		}
	}
	// Windows refuses a rename while our data handle disallows FILE_SHARE_DELETE.
	if err = f.Close(); err != nil {
		return core.NewError(fileError)
	}
	// Do not publish a different inode if a local process replaced the partial.
	atPath, err := os.Lstat(d.PartialPath)
	if err != nil || !os.SameFile(atPath, tmpInfo) {
		return core.NewError(fileError)
	}
	if !published {
		if err = publish(d.PartialPath, t.LocalPath); err != nil {
			return core.NewError(destinationError)
		}
	}
	if err = syncDirectory(filepath.Dir(t.LocalPath)); err != nil {
		return core.NewError(fileError)
	}
	dest, err = os.Lstat(t.LocalPath)
	if err != nil || !os.SameFile(dest, tmpInfo) {
		return core.NewError(destinationError)
	}
	return nil
}

func matchesFile(f *os.File, p core.Part) bool {
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(f, p.Offset, p.Size))
	return err == nil && n == p.Size && hex.EncodeToString(hash.Sum(nil)) == p.Checksum
}

func (e *Engine) fetch(ctx context.Context, f *os.File, t core.Task, d store.Download, number int, offset, length int64, source Source) error {
	body, err := source.ReadRange(ctx, t.Bucket, t.ObjectKey, d.Identity, offset, length)
	if err != nil {
		return err
	}
	defer body.Close()
	hash := sha256.New()
	out := io.MultiWriter(io.NewOffsetWriter(f, offset), hash)
	if _, err = io.CopyN(out, body, length); err != nil {
		var local *os.PathError
		if errors.As(err, &local) {
			return core.NewError(fileError)
		}
		return core.NewError(rangeError)
	}
	var extra [1]byte
	if _, err = io.ReadFull(body, extra[:]); err != io.EOF {
		return core.NewError(rangeError)
	}
	if err = f.Sync(); err != nil {
		return core.NewError(fileError)
	}
	return e.db.Checkpoint(ctx, t.ID, core.Part{Number: number, Offset: offset, Size: length, Checksum: hex.EncodeToString(hash.Sum(nil))})
}
