package tosclient

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
	"github.com/Cdners/S3S/internal/transfer"
)

// Verify that a regional account and native SDK identity survive the same
// persisted download pipeline used by a desktop task after restart.
func TestPersistedDownloadFromKnownTOSBucket(t *testing.T) {
	const key = "a//../目录/%2F+data"
	const version = "abc+/"
	const etag = `"fixture-version"`
	original := []byte("火山引擎 regional SDK readback\n")
	src := localTOS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "data."+beijingHost || r.URL.Path != "/"+key {
			t.Error("worker used a different bucket, key, or account-wide operation")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("x-tos-version-id", version)
		w.Header().Set("Content-Length", strconv.Itoa(len(original)))
		if r.Method == http.MethodHead {
			return
		}
		if r.Header.Get("If-Match") != etag || r.URL.Query().Get("versionId") != version ||
			r.Header.Get("Range") != "bytes=0-"+strconv.Itoa(len(original)-1) {
			t.Error("worker read an unpinned object")
		}
		w.Header().Set("Content-Range", "bytes 0-"+strconv.Itoa(len(original)-1)+"/"+strconv.Itoa(len(original)))
		w.WriteHeader(206)
		_, _ = io.Copy(w, bytes.NewReader(original))
	})
	ctx := t.Context()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := core.Connection{ID: "c", Name: "TOS", Provider: core.TOS, Region: "cn-beijing"}
	if err := db.SaveConnection(ctx, profile); err != nil {
		db.Close()
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "download.bin")
	task, err := transfer.New(db).Enqueue(ctx, profile, src, "data", key, dest)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("enqueue/close: %v, %v", err, closeErr)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := transfer.New(db)
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(ctx, task.ID, profile, src); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("published bytes differ from the TLS origin")
	}
	saved, err := db.Task(ctx, task.ID)
	if err != nil || saved.State != core.Completed || saved.ObjectKey != key || saved.TransferredBytes != int64(len(original)) {
		t.Fatalf("unexpected persisted outcome: %v %v", saved.State, err)
	}
}
