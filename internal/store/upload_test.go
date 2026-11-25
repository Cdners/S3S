package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

func newUpload(t *testing.T, s *Store, size int64) (core.Task, Upload) {
	t.Helper()
	ctx := t.Context()
	c := core.Connection{ID: "connection", Name: "profile", Provider: core.R2, Region: "auto", Endpoint: "https://example.test"}
	requireOK(t, s.SaveConnection(ctx, c))
	task := core.Task{ID: "task", ConnectionID: c.ID, Direction: core.Upload, Bucket: "test", ObjectKey: "a//../B %+文", LocalPath: "file", State: core.Queued, Size: size}
	manifest := Upload{Provider: c.Provider, Region: c.Region, Endpoint: c.Endpoint, FileID: "inode", ModTimeNS: 123, PartSize: 4, SHA256: strings.Repeat("b", 64)}
	requireOK(t, s.CreateUpload(ctx, task, manifest))
	manifest.Phase = "ready"
	return task, manifest
}

func bind(t *testing.T, s *Store, id, session string) {
	t.Helper()
	requireOK(t, s.ClaimUpload(t.Context(), id))
	requireOK(t, s.MarkUploadPhase(t.Context(), id, "ready", "creating"))
	requireOK(t, s.BindUpload(t.Context(), id, session))
}

func TestUploadReconciliationAndCommitRecovery(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "state.db")
	s := openDB(t, path)
	task, manifest := newUpload(t, s, 5)
	id := task.ID
	got, err := s.Upload(ctx, id)
	requireOK(t, err)
	if got != manifest {
		t.Fatalf("manifest = %#v", got)
	}

	bind(t, s, id, "session1")
	requireCode(t, s.ClaimUpload(ctx, id), core.Conflict)
	requireCode(t, s.BindUpload(ctx, id, "wrong"), core.Conflict)
	first := core.Part{Number: 1, Size: 4, Checksum: strings.Repeat("a", 64), ETag: "one"}
	last := core.Part{Number: 2, Offset: 4, Size: 1, Checksum: strings.Repeat("c", 64), ETag: "two"}
	requireOK(t, s.SaveUploadPart(ctx, id, "session1", first))
	requireOK(t, s.SaveUploadPart(ctx, id, "session1", first))
	requireCode(t, s.SaveUploadPart(ctx, id, "wrong", last), core.Conflict)
	requireCode(t, s.MarkUploadPhase(ctx, id, "active", "completing"), core.Conflict)
	requireCode(t, s.Checkpoint(ctx, id, last), core.Conflict)
	requireCode(t, s.Transition(ctx, id, core.Running, core.Completed), core.Conflict)
	requireCode(t, s.ReconcileUploadParts(ctx, id, "session1", []core.Part{last, last}), core.Conflict)
	// The delete and first insert must both have rolled back.
	parts, err := s.Parts(ctx, id)
	requireOK(t, err)
	if !reflect.DeepEqual(parts, []core.Part{first}) {
		t.Fatalf("partial reconcile: %v", parts)
	}
	requireOK(t, s.ReconcileUploadParts(ctx, id, "session1", []core.Part{last}))
	gotTask, err := s.Task(ctx, id)
	requireOK(t, err)
	if gotTask.TransferredBytes != 1 || !gotTask.Resumable || gotTask.ObjectKey != task.ObjectKey {
		t.Fatalf("task = %#v", gotTask)
	}
	requireOK(t, s.SaveUploadPart(ctx, id, "session1", first))
	requireOK(t, s.MarkUploadPhase(ctx, id, "active", "completing"))
	requireCode(t, s.RestartUpload(ctx, id, "session1"), core.Conflict)
	requireCode(t, s.MarkUploadPhase(ctx, id, "completing", "ready"), core.Conflict)
	requireOK(t, s.Close())

	s = openDB(t, path)
	requireOK(t, s.RecoverUploads(ctx))
	tasks, err := s.Tasks(ctx)
	requireOK(t, err)
	if len(tasks) != 1 || tasks[0].State != core.Paused || tasks[0].UploadPhase != "completing" ||
		tasks[0].TransferredBytes != 5 || tasks[0].UploadID != "session1" {
		t.Fatalf("lost commit intent or checkpoints: %v", tasks)
	}
	requireOK(t, s.ClaimUpload(ctx, id))
	requireOK(t, s.FinishUpload(ctx, id, core.Completed, nil))
	requireCode(t, s.ClaimUpload(ctx, id), core.Conflict)
	requireCode(t, s.RequestUploadCancel(ctx, id), core.Conflict)
}

func TestUploadCancellationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	ctx := t.Context()
	s := openDB(t, path)
	task, _ := newUpload(t, s, 4)
	id := task.ID
	requireOK(t, s.ClaimUpload(ctx, id))
	requireOK(t, s.MarkUploadPhase(ctx, id, "ready", "creating"))
	requireOK(t, s.RequestUploadCancel(ctx, id)) // network create is still in flight
	requireOK(t, s.BindUpload(ctx, id, "late-session"))
	p := core.Part{Number: 1, Size: 4, Checksum: strings.Repeat("a", 64), ETag: "confirmed"}
	requireOK(t, s.SaveUploadPart(ctx, id, "late-session", p))
	requireCode(t, s.MarkUploadPhase(ctx, id, "active", "completing"), core.Conflict)
	requireCode(t, s.FinishUpload(ctx, id, core.Canceled, nil), core.Conflict)
	requireOK(t, s.MarkUploadPhase(ctx, id, "active", "aborting"))
	detail := &core.Error{Code: "error.uploadAbort", ProviderCode: "SlowDown", RequestID: "request", Retryable: true}
	requireOK(t, s.FinishUpload(ctx, id, core.Failed, detail))
	requireOK(t, s.Close())

	s = openDB(t, path)
	requireOK(t, s.RecoverUploads(ctx))
	got, err := s.Task(ctx, id)
	requireOK(t, err)
	if got.State != core.Failed || !got.CancelRequested || got.UploadPhase != "aborting" || got.UploadID != "late-session" ||
		!reflect.DeepEqual(got.LastError, detail) {
		t.Fatalf("lost cleanup intent or diagnostics: %#v", got)
	}
	requireOK(t, s.ClaimUpload(ctx, id))
	requireOK(t, s.FinishUpload(ctx, id, core.Canceled, nil))
	got, err = s.Task(ctx, id)
	requireOK(t, err)
	if got.State != core.Canceled || got.CancelRequested || got.LastError != nil || got.TransferredBytes != 0 {
		t.Fatalf("canceled task: %#v", got)
	}
}

func TestExpiredUploadSessionRejectsStaleCheckpoints(t *testing.T) {
	ctx := t.Context()
	db := openDB(t, filepath.Join(t.TempDir(), "expired.db"))
	task, _ := newUpload(t, db, 4)
	id := task.ID
	bind(t, db, id, "old")
	p := core.Part{Number: 1, Size: 4, ETag: "one", Checksum: strings.Repeat("d", 64)}
	requireOK(t, db.SaveUploadPart(ctx, id, "old", p))
	requireCode(t, db.RestartUpload(ctx, id, "other"), core.Conflict)
	requireOK(t, db.RestartUpload(ctx, id, "old"))
	got, err := db.Task(ctx, id)
	requireOK(t, err)
	if got.UploadID != "" || got.UploadPhase != "ready" || got.TransferredBytes != 0 {
		t.Fatalf("bad reset: %#v", got)
	}
	requireOK(t, db.MarkUploadPhase(ctx, id, "ready", "creating"))
	requireOK(t, db.BindUpload(ctx, id, "new"))
	requireCode(t, db.SaveUploadPart(ctx, id, "old", p), core.Conflict)
	requireCode(t, db.ReconcileUploadParts(ctx, id, "old", nil), core.Conflict)
	requireOK(t, db.SaveUploadPart(ctx, id, "new", p))
}

func TestUploadMigrationPreservesV2Records(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	old, err := sql.Open("sqlite3", path)
	requireOK(t, err)
	_, err = old.Exec(schema1 + schema2 + `PRAGMA user_version=2;
        INSERT INTO connections VALUES ('c','origin','aws','','');
        INSERT INTO tasks VALUES ('old','c','upload','b','./K','s',3,'running','legacy-session');
        INSERT INTO tasks VALUES ('dl','c','download','b','../K','dst',3,'running','');
        INSERT INTO parts VALUES('old',1,0,3,'etag','old-digest');
        INSERT INTO downloads(task_id,provider,region,endpoint,etag,version_id,partial_path,file_id,range_size)
            VALUES ('dl','aws','','','tag','','part','file',8);`)
	requireOK(t, err)
	requireOK(t, old.Close())
	db := openDB(t, path)
	ctx := t.Context()
	requireOK(t, db.RecoverUploads(ctx))
	legacy, err := db.Task(ctx, "old")
	requireOK(t, err)
	if legacy.Resumable || legacy.UploadPhase != "" || legacy.UploadID != "legacy-session" || legacy.TransferredBytes != 3 || legacy.State != core.Running {
		t.Fatalf("legacy upload changed: %#v", legacy)
	}
	requireCode(t, db.ClaimUpload(ctx, "old"), core.Conflict)
	requireOK(t, db.RecoverDownloads(ctx))
	d, err := db.Task(ctx, "dl")
	requireOK(t, err)
	if !d.Resumable || d.State != core.Paused || d.ObjectKey != "../K" || d.UploadPhase != "" || d.CancelRequested {
		t.Fatalf("download = %#v", d)
	}
}

func TestClaimUploadAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.db")
	first := openDB(t, path)
	second := openDB(t, path)
	task, _ := newUpload(t, first, 0)
	ctx := t.Context()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, db := range []*Store{first, second} {
		go func() { <-start; results <- db.ClaimUpload(ctx, task.ID) }()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else {
			requireCode(t, err, core.Conflict)
		}
	}
	if successes != 1 {
		t.Fatalf("claims succeeded: %d", successes)
	}
	requireOK(t, first.MarkUploadPhase(ctx, task.ID, "ready", "completing"))
	requireOK(t, second.RequestUploadCancel(ctx, task.ID))
	// An identified, already-published object wins over a late cancel.
	requireOK(t, first.FinishUpload(ctx, task.ID, core.Completed, nil))
	got, err := second.Task(ctx, task.ID)
	requireOK(t, err)
	if got.CancelRequested || got.State != core.Completed {
		t.Fatalf("inconsistent completed task: %#v", got)
	}
}

func TestDefinitiveEmptyPutRejectionCanReturnToReady(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "empty-rejected.db"))
	task, _ := newUpload(t, db, 0)
	id := task.ID
	ctx := t.Context()
	requireOK(t, db.ClaimUpload(ctx, id))
	requireOK(t, db.MarkUploadPhase(ctx, id, "ready", "completing"))
	requireOK(t, db.RequestUploadCancel(ctx, id))
	requireOK(t, db.MarkUploadPhase(ctx, id, "completing", "ready"))
	requireCode(t, db.MarkUploadPhase(ctx, id, "ready", "completing"), core.Conflict)
	requireOK(t, db.MarkUploadPhase(ctx, id, "ready", "aborting"))
	requireOK(t, db.FinishUpload(ctx, id, core.Canceled, nil))
}
