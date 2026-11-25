package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/vault"
	"github.com/zalando/go-keyring"
)

type rejectUploadRequest func(*http.Request) (*http.Response, error)

func (f rejectUploadRequest) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func joinWorker(t *testing.T, w *Workspace, id string) {
	t.Helper()
	w.mu.Lock()
	job, active := w.active[id]
	w.mu.Unlock()
	if active {
		select {
		case <-job.done:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not stop")
		}
	}
}

func TestStartUploadUsesPickerAndExactKey(t *testing.T) {
	ctx := context.Background()
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	saved := w.SaveConnection(ctx, core.Connection{Name: "test", Provider: core.AWS})
	if saved.Error != nil {
		t.Fatal(saved.Error)
	}
	c := *saved.Data
	creds := vault.Credentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text()}
	if saved := w.SetCredentials(ctx, c, creds); saved.Error != nil {
		t.Fatal(saved.Error)
	}
	// Deny signed requests locally. Starting a task must never imply upload success.
	requests := make(chan string, 10)
	w.storageTransport = rejectUploadRequest(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "s3.us-east-1.amazonaws.com" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("expected a signed request scoped to the configured origin")
		}
		requests <- r.URL.Path
		return &http.Response{StatusCode: 403, Status: "403 Forbidden", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("<Error><Code>AccessDenied</Code></Error>")), Request: r}, nil
	})
	key := "a//../资料 %"
	w.chooseSource = func(title string) (string, error) {
		if title != "choose" {
			t.Fatal("picker title")
		}
		return "", nil
	}
	if result := w.StartUpload(ctx, c, "bucket", key, "choose"); result.Error != nil || result.Data != "" {
		t.Fatal("canceled picker created task")
	}
	if list := w.Tasks(ctx); list.Error != nil || len(list.Data) != 0 {
		t.Fatal("expected empty queue")
	}
	source := filepath.Join(t.TempDir(), "private-file")
	contents := rand.Text()
	if err := os.WriteFile(source, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	w.chooseSource = func(string) (string, error) { return source, nil }
	queued := w.StartUpload(ctx, c, "bucket", key, "choose")
	if queued.Error != nil || queued.Data == "" {
		t.Fatal("did not enqueue", queued.Error)
	}
	joinWorker(t, w, queued.Data)
	task, err := w.store.Task(ctx, queued.Data)
	if err != nil || task.ObjectKey != key || task.State != core.Failed {
		t.Fatal("incorrect stored task", err)
	}
	expected, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat(task.LocalPath)
	if err != nil || !os.SameFile(expected, actual) {
		t.Fatal("manifest did not retain the native file selection")
	}
	select {
	case p := <-requests:
		if p != "/bucket/"+key {
			t.Fatalf("SDK received a different remote path %q", p)
		}
	default:
		t.Fatal("no actual signed SDK call")
	}
	wire, err := json.Marshal(w.Tasks(ctx))
	if err != nil || strings.Contains(string(wire), contents) || strings.Contains(string(wire), "private-file") ||
		strings.Contains(string(wire), creds.AccessKeyID) || strings.Contains(string(wire), creds.SecretAccessKey) {
		t.Fatal("private source or credentials in task IPC")
	}
}

func TestCancelUnstartedUploadAndRejectCrossDirection(t *testing.T) {
	ctx := context.Background()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	c := core.Connection{ID: "id", Name: "local-only", Provider: core.AWS}
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(file, []byte(t.Name()), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := w.engine.EnqueueUpload(ctx, c, "bucket", "key", file)
	if err != nil {
		t.Fatal(err)
	}
	if r := w.PauseDownload(ctx, task.ID); r.Error == nil {
		t.Fatal("download control accepted an upload ID")
	}
	if r := w.PauseUpload(ctx, task.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if r := w.CancelUpload(ctx, task.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	joinWorker(t, w, task.ID)
	final, err := w.store.Task(ctx, task.ID)
	if err != nil || final.State != core.Canceled {
		t.Fatal("local cancellation required a file or credentials", err)
	}
}

func TestCancelRetargetedUploadKeepsOriginalCleanupPending(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := openWorkspaceAt(dir)
	c := core.Connection{ID: "id", Name: "test", Provider: core.AWS}
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(file, []byte(t.Name()), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := w.engine.EnqueueUpload(ctx, c, "bucket", "key", file)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.ClaimUpload(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.store.MarkUploadPhase(ctx, task.ID, "ready", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := w.store.BindUpload(ctx, task.ID, "session"); err != nil {
		t.Fatal(err)
	}
	if err := w.store.FinishUpload(ctx, task.ID, core.Paused, nil); err != nil {
		t.Fatal(err)
	}
	c.Region = "eu-west-1"
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := w.CancelUpload(ctx, task.ID)
	if r.Error == nil || r.Error.Code != "error.connectionChanged" {
		t.Fatal("cleanup crossed destination scope")
	}
	w.close()
	next := openWorkspaceAt(dir)
	defer next.close()
	state, err := next.store.Task(ctx, task.ID)
	if err != nil || !state.CancelRequested || state.UploadID != "session" || state.State == core.Canceled {
		t.Fatal("lost pending cleanup on restart")
	}
	if r := next.ResumeUpload(ctx, task.ID); r.Error == nil {
		t.Fatal("canceled intent resumed as upload")
	}
}

// Completion may have reached the provider while the user pressed Cancel.
// The IPC acknowledgment must preserve the worker's verified completed result.
func TestCancelUploadRacingCompletion(t *testing.T) {
	ctx := context.Background()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	c := core.Connection{ID: "id", Name: "race", Provider: core.AWS}
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	task, err := w.engine.EnqueueUpload(ctx, c, "bucket", "key", file)
	if err != nil {
		t.Fatal(err)
	}
	committing := make(chan error, 1)
	persisted := make(chan error, 1)
	err = w.startWorker(ctx, task.ID, core.Upload, false, w.store.ClaimUpload, func(active context.Context) {
		committing <- w.store.MarkUploadPhase(active, task.ID, "ready", "completing")
		<-active.Done()
		persisted <- w.store.FinishUpload(context.WithoutCancel(active), task.ID, core.Completed, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-committing; err != nil {
		t.Fatal(err)
	}
	if r := w.CancelUpload(ctx, task.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	if err := <-persisted; err != nil {
		t.Fatal(err)
	}
	if r := w.CancelUpload(ctx, task.ID); r.Error != nil {
		t.Fatal("repeat after completion", r.Error)
	}
	final, err := w.store.Task(ctx, task.ID)
	if err != nil || final.State != core.Completed || final.CancelRequested {
		t.Fatal("completion was lost")
	}
}

func TestRetryLocalCancellationAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := openWorkspaceAt(dir)
	c := core.Connection{ID: "id", Name: "local", Provider: core.AWS}
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	task, err := w.engine.EnqueueUpload(ctx, c, "bucket", "key", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.ClaimUpload(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RequestUploadCancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.store.MarkUploadPhase(ctx, task.ID, "ready", "aborting"); err != nil {
		t.Fatal(err)
	}
	w.close()
	next := openWorkspaceAt(dir)
	defer next.close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if r := next.CancelUpload(ctx, task.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	joinWorker(t, next, task.ID)
	final, err := next.store.Task(ctx, task.ID)
	if err != nil || final.State != core.Canceled {
		t.Fatal("failed to finalize local cancellation")
	}
}
