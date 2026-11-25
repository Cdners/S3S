package transfer

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

// Replayable provider fixture. The engine drives actual files and SQLite. The
// fixture validates the wire digest and assembles the exact received sections.
type objectSink struct {
	onEmpty                                           func() error
	id, nonce, digest                                 string
	sessions                                          int
	beginsLost, completeLost, headOffline, abortFails bool
	parts                                             map[int][]byte
	object                                            []byte
	hook                                              func(int)
}

func (s *objectSink) BeginUpload(_ context.Context, _, _, nonce, digest string) (string, error) {
	s.sessions++
	s.id = "bound"
	s.nonce = nonce
	s.digest = digest
	s.parts = map[int][]byte{}
	if s.beginsLost {
		return "", io.ErrUnexpectedEOF
	}
	return s.id, nil
}
func (s *objectSink) UploadPart(_ context.Context, _, _, id string, n int, body io.ReadSeeker, length int64, md string) (string, error) {
	if id != s.id || s.parts == nil {
		return "", core.NewError(missingSession)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(b)
	if int64(len(b)) != length || base64.StdEncoding.EncodeToString(sum[:]) != md {
		return "", errors.New("digest mismatch")
	}
	s.parts[n] = b
	if s.hook != nil {
		s.hook(n)
	}
	return hex.EncodeToString(sum[:]), nil
}
func (s *objectSink) ListParts(_ context.Context, _, _, id string) ([]core.Part, error) {
	if id != s.id || s.parts == nil {
		return nil, core.NewError(missingSession)
	}
	var out []core.Part
	for n, b := range s.parts {
		etag := md5.Sum(b)
		out = append(out, core.Part{Number: n, Size: int64(len(b)), ETag: hex.EncodeToString(etag[:])})
	}
	return out, nil
}
func (s *objectSink) CompleteUpload(_ context.Context, _, _, id string, parts []core.Part) error {
	if s.object != nil {
		return core.NewError(objectExists)
	}
	if id != s.id || s.parts == nil {
		return core.NewError(missingSession)
	}
	var b []byte
	for _, p := range parts {
		v := s.parts[p.Number]
		sum := md5.Sum(v)
		if p.ETag != hex.EncodeToString(sum[:]) {
			return core.NewError(partsError)
		}
		b = append(b, v...)
	}
	s.object = b
	s.parts = nil
	if s.completeLost {
		s.headOffline = true
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (s *objectSink) VerifyUpload(_ context.Context, _, _, nonce, digest string, size int64) (bool, error) {
	if s.headOffline {
		return false, core.NewError("error.cloudRequest")
	}
	if s.object == nil {
		return false, nil
	}
	sum := sha256.Sum256(s.object)
	if s.nonce != nonce || s.digest != digest || int64(len(s.object)) != size || hex.EncodeToString(sum[:]) != digest {
		return false, core.NewError(objectExists)
	}
	return true, nil
}
func (s *objectSink) PutEmpty(_ context.Context, _, _, nonce, digest string) error {
	if s.onEmpty != nil {
		return s.onEmpty()
	}
	if s.object != nil {
		return core.NewError(objectExists)
	}
	s.object = []byte{}
	s.nonce = nonce
	s.digest = digest
	return nil
}
func (s *objectSink) AbortUpload(_ context.Context, _, _, id string) error {
	if s.abortFails {
		return core.NewError("error.uploadCleanup")
	}
	if id == "" || id != s.id {
		return core.NewError(core.InvalidTask)
	}
	s.parts = nil
	return nil
}

type uploadFixture struct {
	*Engine
	conn core.Connection
	tsk  core.Task
	dest *objectSink
}

func uploadSetup(t *testing.T, b []byte) *uploadFixture {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "local.bin")
	check(t, os.WriteFile(src, b, 0600))
	db, err := store.Open(filepath.Join(dir, "tasks.db"))
	check(t, err)
	t.Cleanup(func() { db.Close() })
	c := core.Connection{ID: "upload-test", Name: "Test", Provider: core.R2}
	check(t, db.SaveConnection(t.Context(), c))
	e := New(db)
	task, err := e.EnqueueUpload(t.Context(), c, "bucket", " /../多//% ", src)
	check(t, err)
	return &uploadFixture{e, c, task, &objectSink{}}
}
func (f *uploadFixture) run(ctx context.Context) error {
	return f.RunUpload(ctx, f.tsk.ID, f.conn, f.dest)
}
func TestUploadAmbiguousPublicationAndCancel(t *testing.T) {
	f := uploadSetup(t, []byte("uploaded before reply was lost"))
	ctx := t.Context()
	f.dest.completeLost = true
	if f.run(ctx) == nil {
		t.Fatal("reported completion with verification offline")
	}
	task, err := f.db.Task(ctx, f.tsk.ID)
	check(t, err)
	if task.State != core.Failed || task.UploadPhase != "completing" {
		t.Fatal(task.State, task.UploadPhase)
	}
	check(t, f.db.RequestUploadCancel(ctx, task.ID))
	check(t, os.Remove(task.LocalPath))
	f.dest.headOffline = false
	check(t, f.run(ctx))
	task, err = f.db.Task(ctx, task.ID)
	check(t, err)
	if task.State != core.Completed || !bytes.Equal(f.dest.object, []byte("uploaded before reply was lost")) {
		t.Fatal("misreported verified published object")
	}
}
func TestUploadCreationResponseLost(t *testing.T) {
	f := uploadSetup(t, []byte("abc"))
	ctx := t.Context()
	f.dest.beginsLost = true
	if !hasCode(f.run(ctx), unknownCreate) {
		t.Fatal("missing uncertainty diagnosis")
	}
	check(t, f.db.RequestUploadCancel(ctx, f.tsk.ID))
	if !hasCode(f.run(ctx), unknownCreate) {
		t.Fatal("claimed cleanup without session ID")
	}
	task, err := f.db.Task(ctx, f.tsk.ID)
	check(t, err)
	if f.dest.sessions != 1 || task.State != core.Failed || !task.CancelRequested || task.UploadPhase != "creating" {
		t.Fatal("lost uncertain creation intent")
	}
}
func TestUploadRejectsChangedNextPartAndRetriesAbort(t *testing.T) {
	data := bytes.Repeat([]byte{'a'}, int(memoryPartSize+1))
	f := uploadSetup(t, data)
	info, err := os.Stat(f.tsk.LocalPath)
	check(t, err)
	f.dest.hook = func(part int) {
		// Change an unsent byte while preserving size, inode and modification time.
		file, err := os.OpenFile(f.tsk.LocalPath, os.O_WRONLY, 0)
		check(t, err)
		value := byte('X')
		_, err = file.WriteAt([]byte{value}, memoryPartSize)
		check(t, err)
		check(t, file.Close())
		check(t, os.Chtimes(f.tsk.LocalPath, info.ModTime(), info.ModTime()))
	}
	if err := f.run(t.Context()); !hasCode(err, uploadSourceError) {
		t.Fatalf("mixed source bytes accepted: %v", err)
	}
	if f.dest.object != nil || len(f.dest.parts) != 1 {
		t.Fatal("sent unexpected bytes or published inconsistent ranges")
	}
	f.dest.hook = nil
	// An edited source also fails verification after resuming the session.
	if !hasCode(f.run(t.Context()), uploadSourceError) {
		t.Fatal("reused wrong bytes")
	}
	check(t, f.db.RequestUploadCancel(t.Context(), f.tsk.ID))
	f.dest.abortFails = true
	if f.run(t.Context()) == nil {
		t.Fatal("false cancellation")
	}
	task, err := f.db.Task(t.Context(), f.tsk.ID)
	check(t, err)
	if !task.CancelRequested || task.UploadPhase != "aborting" {
		t.Fatal("lost cleanup intent")
	}
	f.dest.abortFails = false
	check(t, f.run(t.Context()))
	task, err = f.db.Task(t.Context(), f.tsk.ID)
	check(t, err)
	if task.State != core.Canceled || task.TransferredBytes != 0 || f.dest.parts != nil {
		t.Fatal("incomplete cleanup")
	}
}
func TestUploadEmptyAndExpiredSession(t *testing.T) {
	empty := uploadSetup(t, nil)
	check(t, empty.run(t.Context()))
	if empty.dest.object == nil || len(empty.dest.object) != 0 || empty.dest.sessions != 0 {
		t.Fatal("empty object not published conditionally")
	}
	src := bytes.Repeat([]byte("r"), int(memoryPartSize+1))
	f := uploadSetup(t, src)
	ctx, pause := context.WithCancel(t.Context())
	defer pause()
	f.dest.hook = func(n int) {
		if n == 1 {
			pause()
		}
	}
	if !errors.Is(f.run(ctx), context.Canceled) {
		t.Fatal("expected pause")
	}
	f.dest.parts = nil
	f.dest.hook = nil
	check(t, f.run(t.Context()))
	if f.dest.sessions != 2 || !bytes.Equal(f.dest.object, src) {
		t.Fatal("lost session not restarted correctly")
	}
}
func TestUploadSourceReplacementAndQueuedCancel(t *testing.T) {
	f := uploadSetup(t, []byte("unchanged"))
	path := f.tsk.LocalPath
	info, err := os.Stat(path)
	check(t, err)
	check(t, os.Rename(path, path+".original"))
	check(t, os.WriteFile(path, []byte("unchanged"), 0600))
	check(t, os.Chtimes(path, time.Now(), info.ModTime()))
	if !hasCode(f.run(t.Context()), uploadSourceError) || f.dest.sessions != 0 {
		t.Fatal("accepted different source inode")
	}
	check(t, f.db.RequestUploadCancel(t.Context(), f.tsk.ID))
	check(t, f.RunUpload(t.Context(), f.tsk.ID, core.Connection{}, nil))
	task, err := f.db.Task(t.Context(), f.tsk.ID)
	check(t, err)
	if task.State != core.Canceled {
		t.Fatal("local cancellation failed")
	}
}
func TestUploadGeometryAndLargeSnapshot(t *testing.T) {
	size := uploadPartSize(maxUploadSize)
	if size > 5<<30 || (maxUploadSize+size-1)/size > 10000 || size%(1<<20) != 0 {
		t.Fatal("unsupported S3/R2 part geometry", size)
	}
	data := bytes.Repeat([]byte{'S'}, int(memoryPartSize+1))
	path := filepath.Join(t.TempDir(), "source")
	check(t, os.WriteFile(path, data, 0600))
	src, err := openSource(path)
	check(t, err)
	defer src.f.Close()
	snap, err := src.snapshot(t.Context(), 0, int64(len(data)), sha256.New(), true)
	check(t, err)
	defer snap.close()
	if snap.scratch == nil {
		t.Fatal("large snapshot retained in RAM")
	}
	got, err := io.ReadAll(snap.body)
	check(t, err)
	if !bytes.Equal(got, data) {
		t.Fatal("scratch snapshot corrupted")
	}
}

func TestRejectedInitialEmptyPutCanCancel(t *testing.T) {
	f := uploadSetup(t, nil)
	ctx := t.Context()
	f.dest.onEmpty = func() error {
		check(t, f.db.RequestUploadCancel(ctx, f.tsk.ID))
		return core.NewError("error.cloudAccess")
	}
	if !hasCode(f.run(ctx), "error.cloudAccess") {
		t.Fatal("lost definitive provider rejection")
	}
	task, err := f.db.Task(ctx, f.tsk.ID)
	check(t, err)
	if task.UploadPhase != "ready" || !task.CancelRequested {
		t.Fatalf("unsafe post-rejection state: %#v", task)
	}
	check(t, f.RunUpload(ctx, f.tsk.ID, core.Connection{}, nil))
	task, err = f.db.Task(ctx, f.tsk.ID)
	check(t, err)
	if task.State != core.Canceled {
		t.Fatal(task.State)
	}
}
func TestRejectedEmptyRetryDoesNotFencePriorPut(t *testing.T) {
	f := uploadSetup(t, nil)
	ctx := t.Context()
	f.dest.onEmpty = func() error { return io.ErrUnexpectedEOF }
	if f.run(ctx) == nil {
		t.Fatal("accepted missing response")
	}
	f.dest.onEmpty = func() error { return core.NewError("error.cloudAccess") }
	if !hasCode(f.run(ctx), "error.cloudAccess") {
		t.Fatal("lost rejection on retry")
	}
	check(t, f.db.RequestUploadCancel(ctx, f.tsk.ID))
	if !hasCode(f.run(ctx), verifyError) {
		t.Fatal("unfenced original PUT reported canceled")
	}
	task, err := f.db.Task(ctx, f.tsk.ID)
	check(t, err)
	if task.State != core.Failed || task.UploadPhase != "completing" || !task.CancelRequested {
		t.Fatal("lost pending intent")
	}
}
