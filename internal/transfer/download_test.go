package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

// This source supplies deterministic object bytes. Assertions exercise SQLite,
// the actual filesystem, publication and the precise ranges read after restart.
type object struct {
	bytes   []byte
	etag    string
	key     string
	offsets []int64
	onRead  func(int64)
	adjust  int
}

func (o *object) Head(_ context.Context, _, key string) (core.ObjectIdentity, error) {
	if key != o.key {
		return core.ObjectIdentity{}, errors.New("object key changed")
	}
	return core.ObjectIdentity{Size: int64(len(o.bytes)), ETag: o.etag}, nil
}
func (o *object) ReadRange(ctx context.Context, bucket, key string, i core.ObjectIdentity, offset, length int64) (io.ReadCloser, error) {
	o.offsets = append(o.offsets, offset)
	if o.onRead != nil {
		o.onRead(offset)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	identity, err := o.Head(ctx, bucket, key)
	if err != nil || identity != i {
		return nil, errors.New("range not bound to source")
	}
	data := o.bytes[offset : offset+length]
	if o.adjust < 0 {
		data = data[:len(data)-1]
	} else if o.adjust > 0 {
		data = append(slices.Clone(data), '!')
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type fixture struct {
	*Engine
	c    core.Connection
	obj  *object
	task core.Task
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func setup(t *testing.T, dir string, data []byte) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "workspace.db"))
	check(t, err)
	t.Cleanup(func() { db.Close() })
	c := core.Connection{ID: "profile", Name: "profile", Provider: core.AWS}
	check(t, db.SaveConnection(t.Context(), c))
	obj := &object{bytes: data, etag: "\"object-01\"", key: " /./文件//../%2e "}
	e := New(db)
	e.rangeSize = 3
	task, err := e.Enqueue(t.Context(), c, obj, "bucket", obj.key, filepath.Join(dir, "chosen"))
	check(t, err)
	return &fixture{e, c, obj, task}
}
func (f *fixture) run(ctx context.Context) error { return f.Run(ctx, f.task.ID, f.c, f.obj) }
func TestResumeAfterProcessDeath(t *testing.T) {
	const crash = 72
	data := []byte("restartable")
	if dir := os.Getenv("S3S_TEST_ENGINE_CHILD"); dir != "" {
		f := setup(t, dir, data)
		f.obj.onRead = func(offset int64) {
			if offset == 6 {
				os.Exit(crash)
			}
		}
		t.Fatalf("expected exit: %v", f.run(t.Context()))
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeAfterProcessDeath$")
	cmd.Env = append(cmd.Environ(), "S3S_TEST_ENGINE_CHILD="+dir)
	out, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != crash {
		t.Fatalf("child: %v: %s", err, out)
	}
	db, err := store.Open(filepath.Join(dir, "workspace.db"))
	check(t, err)
	defer db.Close()
	ctx := t.Context()
	tasks, err := db.Tasks(ctx)
	check(t, err)
	if len(tasks) != 1 || tasks[0].State != core.Running || tasks[0].TransferredBytes != 6 {
		t.Fatalf("lost checkpoints: %+v", tasks)
	}
	task := tasks[0]
	d, err := db.Download(ctx, task.ID)
	check(t, err)
	tmp, err := os.OpenFile(d.PartialPath, os.O_WRONLY, 0)
	check(t, err)
	_, err = tmp.WriteAt([]byte("X"), 4)
	check(t, err)
	check(t, tmp.Close())
	c, err := db.Connection(ctx, task.ConnectionID)
	check(t, err)
	obj := &object{bytes: data, etag: d.Identity.ETag, key: " /./文件//../%2e "}
	e := New(db)
	check(t, e.Recover(ctx))
	saved, err := db.Task(ctx, task.ID)
	check(t, err)
	if saved.State != core.Paused {
		t.Fatalf("after restart: %s", saved.State)
	}
	check(t, e.Run(ctx, task.ID, c, obj))
	if !slices.Equal(obj.offsets, []int64{3, 6, 9}) {
		t.Fatalf("resume offsets %v", obj.offsets)
	}
	got, err := os.ReadFile(task.LocalPath)
	check(t, err)
	if !bytes.Equal(data, got) {
		t.Fatal("published bytes differ")
	}
	saved, err = db.Task(ctx, task.ID)
	check(t, err)
	if saved.State != core.Completed || saved.TransferredBytes != int64(len(data)) || saved.LastError != nil {
		t.Fatalf("completed task %+v", saved)
	}
	if _, err = os.Lstat(d.PartialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed partial not removed")
	}
}
func TestRejectBadRangeAndModifiedSource(t *testing.T) {
	for _, adjust := range []int{-1, 1} {
		f := setup(t, t.TempDir(), []byte("abcdef"))
		ctx := t.Context()
		f.obj.adjust = adjust
		if f.run(ctx) == nil {
			t.Fatal("accepted bad byte count")
		}
		task, err := f.db.Task(ctx, f.task.ID)
		check(t, err)
		if task.State != core.Failed || task.TransferredBytes != 0 || task.LastError == nil || task.LastError.Code != rangeError {
			t.Fatalf("failure not recorded: %#v", task)
		}
		f.obj.adjust = 0
		f.obj.etag = "\"new-object\""
		if f.run(ctx) == nil {
			t.Fatal("accepted changed ETag")
		}
		f.obj.etag = "\"object-01\""
		f.c.Region = "different"
		if f.run(ctx) == nil {
			t.Fatal("accepted changed origin")
		}
		if _, err = os.Stat(f.task.LocalPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed transfer published")
		}
		f.c.Region = ""
		check(t, f.run(ctx))
	}
}
func TestNeverOverwriteDestination(t *testing.T) {
	f := setup(t, t.TempDir(), []byte("the download"))
	f.obj.onRead = func(offset int64) {
		if offset == 0 {
			check(t, os.WriteFile(f.task.LocalPath, []byte("another writer"), 0600))
		}
	}
	if f.run(t.Context()) == nil {
		t.Fatal("published over another writer")
	}
	got, err := os.ReadFile(f.task.LocalPath)
	check(t, err)
	if string(got) != "another writer" {
		t.Fatal("clobbered existing bytes")
	}
}
func TestCrashAfterPublication(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("payload")} {
		f := setup(t, t.TempDir(), data)
		ctx := t.Context()
		check(t, f.db.ClaimDownload(ctx, f.task.ID))
		d, err := f.db.Download(ctx, f.task.ID)
		check(t, err)
		check(t, f.transfer(ctx, f.obj, f.task, &d)) // publication durable; SQLite still 'running'
		check(t, f.Recover(ctx))
		f.obj.offsets = nil
		f.obj.etag = "\"replaced-after-publication\""
		check(t, f.run(ctx))
		got, err := os.ReadFile(f.task.LocalPath)
		check(t, err)
		if !bytes.Equal(got, data) || len(f.obj.offsets) != 0 {
			t.Fatal("did not reconcile published destination")
		}
	}
}

func TestCancelDuringRange(t *testing.T) {
	f := setup(t, t.TempDir(), []byte("abcdef"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.obj.onRead = func(offset int64) {
		if offset == 3 {
			cancel()
		}
	}
	if !errors.Is(f.run(ctx), context.Canceled) {
		t.Fatal("did not pause")
	}
	task, err := f.db.Task(t.Context(), f.task.ID)
	check(t, err)
	if task.State != core.Paused || task.TransferredBytes != 3 {
		t.Fatalf("paused task %+v", task)
	}
	f.obj.offsets = nil
	f.obj.onRead = nil
	check(t, f.run(t.Context()))
	if !slices.Equal(f.obj.offsets, []int64{3}) {
		t.Fatalf("retrieved %v", f.obj.offsets)
	}
}

func TestReplacedAndHardLinkedPartialAreNotWritten(t *testing.T) {
	for _, replaced := range []bool{true, false} {
		f := setup(t, t.TempDir(), []byte("the-object"))
		d, err := f.db.Download(t.Context(), f.task.ID)
		check(t, err)
		unrelated := d.PartialPath + ".other"
		if replaced {
			// Keep the original inode alive so replacement has a distinct ID.
			check(t, os.Rename(d.PartialPath, d.PartialPath+".moved"))
			check(t, os.WriteFile(d.PartialPath, []byte("keep"), 0600))
		} else {
			check(t, os.WriteFile(d.PartialPath, []byte("keep"), 0600))
			check(t, os.Link(d.PartialPath, unrelated))
		}
		if f.run(t.Context()) == nil {
			t.Fatal("accepted an unsafe partial")
		}
		got, err := os.ReadFile(d.PartialPath)
		check(t, err)
		if string(got) != "keep" {
			t.Fatal("wrote to a replaced or multiply-linked file")
		}
	}
}

func TestMissingPartialIsRecreatedAndCompletionCleanupReplayed(t *testing.T) {
	f := setup(t, t.TempDir(), []byte("refetch"))
	ctx := t.Context()
	old, err := f.db.Download(ctx, f.task.ID)
	check(t, err)
	check(t, os.Remove(old.PartialPath))
	check(t, f.db.ClaimDownload(ctx, f.task.ID))
	d := old
	check(t, f.transfer(ctx, f.obj, f.task, &d))
	check(t, f.db.FinishDownload(ctx, f.task.ID, core.Completed, nil))
	if d.PartialPath == old.PartialPath {
		t.Fatal("did not bind a fresh temporary file")
	}
	check(t, f.Recover(ctx))
	if _, err = os.Lstat(d.PartialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandoned completed partial")
	}
	got, err := os.ReadFile(f.task.LocalPath)
	check(t, err)
	if !bytes.Equal(got, f.obj.bytes) {
		t.Fatal("cleanup lost published bytes")
	}
}

func TestConcurrentRunClaimsOnce(t *testing.T) {
	f := setup(t, t.TempDir(), []byte("abc"))
	entered, release := make(chan struct{}), make(chan struct{})
	f.obj.onRead = func(int64) { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- f.run(t.Context()) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("worker exited before reading the object: %v", err)
	}
	err := f.run(t.Context())
	close(release)
	check(t, <-done)
	var detail *core.Error
	if !errors.As(err, &detail) || detail.Code != core.Conflict || len(f.obj.offsets) != 1 {
		t.Fatalf("concurrent attempt %v, reads %v", err, f.obj.offsets)
	}
}
