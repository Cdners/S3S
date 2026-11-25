package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/vault"
	"github.com/zalando/go-keyring"
)

func TestProfileEditsGuardSecretWritesAndReads(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	w := openWorkspaceAt(dir)
	if w.store == nil {
		t.Fatal("failed to open workspace")
	}
	defer w.close()
	ctx := context.Background()
	saved := w.SaveConnection(ctx, core.Connection{Name: "test", Provider: core.AWS})
	if saved.Error != nil {
		t.Fatal(saved.Error)
	}
	c := *saved.Data
	secret := vault.Credentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text()}
	if r := w.SetCredentials(ctx, c, secret); r.Error != nil || !r.Data {
		t.Fatal("credential write failed")
	}
	if _, err := w.client(ctx, c); err != nil {
		t.Fatal("saved explicit credentials not usable")
	}
	old := c
	c.Region = "eu-west-1"
	if r := w.SaveConnection(ctx, c); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := w.SetCredentials(ctx, old, secret); r.Error == nil || r.Error.Code != "error.connectionChanged" {
		t.Fatal("stale write accepted")
	}
	if r := w.ForgetCredentials(ctx, old); r.Error == nil {
		t.Fatal("stale delete accepted")
	}
	if _, err := w.client(ctx, c); err == nil || err.Error() != "error.credentialsMissing" {
		t.Fatal("retargeted profile obtained old credentials")
	}
	r := w.ListBuckets(ctx, old, "")
	if r.Error == nil || r.Error.Code != "error.connectionChanged" {
		t.Fatal("stale list was not rejected")
	}
	b, err := json.Marshal([]any{w.Connections(ctx), w.Tasks(ctx), r})
	if err != nil || strings.Contains(string(b), secret.SecretAccessKey) || strings.Contains(string(b), secret.AccessKeyID) {
		t.Fatal("IPC exposed credential material")
	}
	if r := w.ForgetCredentials(ctx, c); r.Error != nil {
		t.Fatal(r.Error)
	}
	for _, path := range []string{"workspace.sqlite3", "workspace.sqlite3-wal"} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret.SecretAccessKey) || strings.Contains(string(data), secret.AccessKeyID) {
			t.Fatal("database exposed credential material")
		}
	}
}

func TestCancelSaveDialogCreatesNoTask(t *testing.T) {
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	w.chooseDestination = func(title string) (string, error) {
		if title != "title" {
			t.Fatal("unexpected title")
		}
		return "", nil
	}
	result := w.StartDownload(context.Background(), core.Connection{}, "bucket", "../../private", "title")
	if result.Error != nil || result.Data != "" {
		t.Fatal("unexpected canceled result")
	}
	tasks := w.Tasks(context.Background())
	if tasks.Error != nil || len(tasks.Data) != 0 {
		t.Fatal("unexpected task after cancel")
	}
}

type waitingSource struct{ reading chan struct{} }

func (*waitingSource) Head(context.Context, string, string) (core.ObjectIdentity, error) {
	return core.ObjectIdentity{ETag: "\"etag\"", Size: 100}, nil
}
func (s *waitingSource) ReadRange(ctx context.Context, _ string, _ string, _ core.ObjectIdentity, _, _ int64) (io.ReadCloser, error) {
	close(s.reading)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWorkspaceOwnsTransfersUntilShutdown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := openWorkspaceAt(dir)
	if w.store == nil {
		t.Fatal("failed to open workspace")
	}
	saved := w.SaveConnection(ctx, core.Connection{Name: "test", Provider: core.AWS})
	if saved.Error != nil {
		t.Fatal(saved.Error)
	}
	c := *saved.Data
	source := &waitingSource{reading: make(chan struct{})}
	task, err := w.engine.Enqueue(ctx, c, source, "bucket", "/../opaque", filepath.Join(t.TempDir(), "selection"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.launch(ctx, task.ID, c, source); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("range did not start")
	}
	second := openWorkspaceAt(dir)
	if second.store != nil || second.unavailable() != "error.workspaceInUse" {
		t.Fatal("competing workspace acquired database")
	}
	state, err := w.store.Task(ctx, task.ID)
	if err != nil || state.State != core.Running {
		t.Fatal("second opener interfered with active transfer")
	}
	if paused := w.PauseDownload(ctx, task.ID); paused.Error != nil || !paused.Data {
		t.Fatal("pause did not join the worker")
	}
	source = &waitingSource{reading: make(chan struct{})}
	if err := w.launch(ctx, task.ID, c, source); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("paused range did not restart")
	}
	w.close()
	next := openWorkspaceAt(dir)
	if next.store == nil {
		t.Fatal("lock was not released")
	}
	defer next.close()
	state, err = next.store.Task(ctx, task.ID)
	if err != nil || state.State != core.Paused {
		t.Fatal("expected durable paused task")
	}
	c.Region = "eu-west-1"
	if r := next.SaveConnection(ctx, c); r.Error != nil {
		t.Fatal(r.Error)
	}
	r := next.ResumeDownload(ctx, task.ID)
	if r.Error == nil || r.Error.Code != "error.connectionChanged" {
		t.Fatal("old task accepted a different connection origin")
	}
}

// A failed terminal SQLite write leaves a running manifest after its worker has
// exited. Explicit controls must recover only that idle task, without requiring
// a second process to acquire the workspace and run startup recovery.
func TestControlInterruptedWorkersWithoutRestart(t *testing.T) {
	ctx := context.Background()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	c := core.Connection{ID: "id", Name: "local", Provider: core.AWS}
	if err := w.store.SaveConnection(ctx, c); err != nil {
		t.Fatal(err)
	}

	download, err := w.engine.Enqueue(ctx, c, &waitingSource{}, "bucket", "key", filepath.Join(t.TempDir(), "download"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.ClaimDownload(ctx, download.ID); err != nil {
		t.Fatal(err)
	}
	if r := w.PauseDownload(ctx, download.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	d, err := w.store.Task(ctx, download.ID)
	if err != nil || d.State != core.Paused {
		t.Fatal("stale download could not be paused")
	}

	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte(t.Name()), 0600); err != nil {
		t.Fatal(err)
	}
	upload, err := w.engine.EnqueueUpload(ctx, c, "bucket", "key", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.ClaimUpload(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	if r := w.PauseUpload(ctx, upload.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	// The user's retry may itself be handling a stale cleanup worker. Its
	// durable cancellation intent and aborting phase must survive the CAS.
	if err := w.store.RequestUploadCancel(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.store.ClaimUpload(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.store.MarkUploadPhase(ctx, upload.ID, "ready", "aborting"); err != nil {
		t.Fatal(err)
	}
	if r := w.CancelUpload(ctx, upload.ID); r.Error != nil {
		t.Fatal(r.Error)
	}
	joinWorker(t, w, upload.ID)
	u, err := w.store.Task(ctx, upload.ID)
	if err != nil || u.State != core.Canceled || u.UploadPhase != "aborting" {
		t.Fatal("stale upload cleanup could not finish")
	}
}
