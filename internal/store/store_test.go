package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

func openDB(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	var e *core.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want %v", err, code)
	}
}

func TestRecoveryRecordsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state #01.db")
	ctx := context.Background()
	s := openDB(t, path)
	empty, err := s.Connections(ctx)
	requireOK(t, err)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("initial connections = %#v", empty)
	}
	noTasks, err := s.Tasks(ctx)
	requireOK(t, err)
	if noTasks == nil || len(noTasks) != 0 {
		t.Fatalf("initial tasks = %#v", noTasks)
	}

	c := core.Connection{ID: "c", Name: "R2", Provider: core.R2, Region: "auto", Endpoint: "https://example.test"}
	key := " /目录/../e\u0301//./尾/ "
	task := core.Task{ID: "t", ConnectionID: c.ID, Direction: core.Upload, Bucket: "bucket", ObjectKey: key, LocalPath: "local-file", Size: 1024, State: core.Queued}
	requireCode(t, s.CreateTask(ctx, task), core.Conflict) // foreign key failure cannot leave a partial task
	requireOK(t, s.SaveConnection(ctx, c))
	c.Name = "Updated"
	requireOK(t, s.SaveConnection(ctx, c))
	requireOK(t, s.CreateTask(ctx, task))
	requireCode(t, s.CreateTask(ctx, task), core.Conflict)
	requireCode(t, s.Transition(ctx, task.ID, core.Queued, core.Completed), core.InvalidTransition)
	requireOK(t, s.Transition(ctx, task.ID, core.Queued, core.Running))
	requireCode(t, s.Transition(ctx, task.ID, core.Queued, core.Running), core.Conflict)
	p := core.Part{Number: 2, Offset: 512, Size: 512, ETag: "etag-2", Checksum: "checksum-2"}
	requireCode(t, s.Checkpoint(ctx, task.ID, p), core.Conflict)
	requireOK(t, s.BindUpload(ctx, task.ID, "session"))
	requireOK(t, s.BindUpload(ctx, task.ID, "session"))
	requireCode(t, s.BindUpload(ctx, task.ID, "another"), core.Conflict)
	requireOK(t, s.Checkpoint(ctx, task.ID, p))
	requireOK(t, s.Checkpoint(ctx, task.ID, p))
	stale := p
	stale.ETag = "other"
	requireCode(t, s.Checkpoint(ctx, task.ID, stale), core.Conflict)
	requireCode(t, s.Checkpoint(ctx, task.ID, core.Part{Number: 3, Offset: 500, Size: 13}), core.Conflict)
	requireCode(t, s.Checkpoint(ctx, task.ID, core.Part{Number: 3, Offset: 1024, Size: 1}), core.InvalidPart)
	requireOK(t, s.Close())

	s = openDB(t, path)
	connections, err := s.Connections(ctx)
	requireOK(t, err)
	if !reflect.DeepEqual(connections, []core.Connection{c}) {
		t.Fatalf("connections = %#v", connections)
	}
	tasks, err := s.Tasks(ctx)
	requireOK(t, err)
	task.State, task.UploadID = core.Running, "session"
	task.TransferredBytes = 512
	if !reflect.DeepEqual(tasks, []core.Task{task}) {
		t.Fatalf("tasks = %#v", tasks)
	}
	parts, err := s.Parts(ctx, task.ID)
	requireOK(t, err)
	if !reflect.DeepEqual(parts, []core.Part{p}) {
		t.Fatalf("parts = %#v", parts)
	}
	requireOK(t, s.Transition(ctx, task.ID, core.Running, core.Paused))
	requireCode(t, s.Checkpoint(ctx, task.ID, core.Part{Number: 1, Offset: 0, Size: 512}), core.Conflict)
}

func TestMigrationIsAtomicAndRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite3", path)
	requireOK(t, err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE tasks (original TEXT)")
	requireOK(t, err)
	if _, err := Open(path); err == nil {
		t.Fatal("expected migration conflict")
	}
	var n int
	requireOK(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='connections'").Scan(&n))
	if n != 0 {
		t.Fatal("failed migration left a partial table")
	}
	requireOK(t, db.QueryRow("PRAGMA user_version").Scan(&n))
	if n != 0 {
		t.Fatal("failed migration advanced version")
	}
	_, err = db.Exec("PRAGMA user_version = 99")
	requireOK(t, err)
	if _, err := Open(path); err == nil {
		t.Fatal("accepted newer schema")
	}
	requireOK(t, db.QueryRow("PRAGMA user_version").Scan(&n))
	if n != 99 {
		t.Fatal("changed newer schema")
	}
}

// A helper process exits after committing a checkpoint, without closing SQLite or
// running any application shutdown hooks. This exercises WAL recovery on reopen.
func TestCheckpointAfterProcessExit(t *testing.T) {
	const exitAfterCheckpoint = 81
	ctx := context.Background()
	if path := os.Getenv("S3S_TEST_CRASH_DB"); path != "" {
		s := openDB(t, path)
		c := core.Connection{ID: "c", Name: "storage", Provider: core.AWS}
		requireOK(t, s.SaveConnection(ctx, c))
		task := core.Task{ID: "t", ConnectionID: c.ID, Direction: core.Download, Bucket: "b", ObjectKey: "k", Size: 4, State: core.Queued}
		requireOK(t, s.CreateTask(ctx, task))
		requireOK(t, s.Transition(ctx, task.ID, core.Queued, core.Running))
		requireOK(t, s.Checkpoint(ctx, task.ID, core.Part{Number: 1, Offset: 0, Size: 2, Checksum: "abc"}))
		os.Exit(exitAfterCheckpoint)
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointAfterProcessExit$")
	cmd.Env = append(cmd.Environ(), "S3S_TEST_CRASH_DB="+path)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitAfterCheckpoint {
		t.Fatalf("checkpoint writer: %v (%s)", err, out)
	}

	s := openDB(t, path)
	tasks, err := s.Tasks(ctx)
	requireOK(t, err)
	if len(tasks) != 1 || tasks[0].State != core.Running {
		t.Fatalf("lost in-progress state: %#v", tasks)
	}
	parts, err := s.Parts(ctx, "t")
	requireOK(t, err)
	if !reflect.DeepEqual(parts, []core.Part{{Number: 1, Offset: 0, Size: 2, Checksum: "abc"}}) {
		t.Fatalf("lost checkpoint: %#v", parts)
	}
}

func TestMigrationFromM0PreservesUnboundTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	old, err := sql.Open("sqlite3", path)
	requireOK(t, err)
	_, err = old.Exec(schema1 + `PRAGMA user_version=1;
        INSERT INTO connections VALUES ('c','origin','aws','','');
        INSERT INTO tasks VALUES ('legacy','c','download','b','a//../b','output',1,'running','');`)
	requireOK(t, err)
	requireOK(t, old.Close())
	db := openDB(t, path)
	requireOK(t, db.RecoverDownloads(t.Context()))
	task, err := db.Task(t.Context(), "legacy")
	requireOK(t, err)
	if task.Resumable || task.State != core.Running || task.ObjectKey != "a//../b" {
		t.Fatalf("legacy task changed: %#v", task)
	}
	requireCode(t, db.ClaimDownload(t.Context(), "legacy"), core.Conflict)
}
