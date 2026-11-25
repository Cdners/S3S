//go:build s3integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
	"github.com/Cdners/S3S/internal/store"
	"github.com/Cdners/S3S/internal/transfer"
)

// Intercept only the checkpoint boundary. Every part is actually sent to the
// independent HTTPS service. The child exits before the third acknowledgement
// can reach the engine or SQLite.
type multipartProbe struct {
	*s3client.Client
	crashAfterPart int
	uploaded       []int
	lists          int
}

func (p *multipartProbe) UploadPart(ctx context.Context, bucket, key, id string, number int, body io.ReadSeeker, size int64, md5Base64 string) (string, error) {
	etag, err := p.Client.UploadPart(ctx, bucket, key, id, number, body, size, md5Base64)
	if err == nil {
		p.uploaded = append(p.uploaded, number)
		if p.crashAfterPart == number {
			os.Exit(33)
		}
	}
	return etag, err
}

func (p *multipartProbe) ListParts(ctx context.Context, bucket, key, id string) ([]core.Part, error) {
	p.lists++
	return p.Client.ListParts(ctx, bucket, key, id)
}

func openDB(t *testing.T, dir string, c core.Connection) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "upload.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.SaveConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return db
}

func randomFile(t *testing.T, dir string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, "local.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(f, rand.Reader, size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMultipartAfterProcessExit(t *testing.T) {
	for _, provider := range []core.Provider{core.AWS, core.R2} {
		t.Run(string(provider), func(t *testing.T) {
			c, client, obj := fixture(t, provider)
			ctx := t.Context()
			key := "upload-test/" + string(provider) + "//空格 +%2F/../resume.bin"
			if dir := os.Getenv("S3S_UPLOAD_CRASH_DIR"); dir != "" {
				db := openDB(t, dir, c)
				engine := transfer.New(db)
				task, err := engine.EnqueueUpload(ctx, c, obj.Bucket, key, filepath.Join(dir, "local.bin"))
				if err != nil {
					t.Fatal(err)
				}
				probe := &multipartProbe{Client: client, crashAfterPart: 3}
				t.Fatalf("expected crash during third part, RunUpload returned: %v", engine.RunUpload(ctx, task.ID, c, probe))
			}
			dir := t.TempDir()
			source := randomFile(t, dir, 24<<20+311)
			cmd := exec.Command(os.Args[0], "-test.run=^TestMultipartAfterProcessExit$/^"+string(provider)+"$")
			cmd.Env = append(os.Environ(), "S3S_UPLOAD_CRASH_DIR="+dir)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 33 {
				t.Fatalf("child: %v\n%s", err, out)
			}
			db := openDB(t, dir, c)
			rows, err := db.Tasks(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("tasks: %d, %v", len(rows), err)
			}
			task := rows[0]
			if task.State != core.Running || task.UploadID == "" {
				t.Fatal("missing interrupted multipart session")
			}
			local, err := db.Parts(ctx, task.ID)
			if err != nil || len(local) != 2 {
				t.Fatalf("expected two durable confirmations: %v", err)
			}
			remote, err := client.ListParts(ctx, obj.Bucket, key, task.UploadID)
			if err != nil || len(remote) != 3 {
				t.Fatalf("expected three real parts: %v", err)
			}
			// Introduce a second discrepancy: a checkpointed part has been replaced
			// on the provider. Recovery must preserve part one, repair part two, and
			// safely handle part three whose acknowledgement was lost.
			wrong := bytes.Repeat([]byte{0x42}, int(local[1].Size))
			digest := md5.Sum(wrong)
			etag, err := client.UploadPart(ctx, obj.Bucket, key, task.UploadID, 2, bytes.NewReader(wrong), int64(len(wrong)), base64.StdEncoding.EncodeToString(digest[:]))
			if err != nil || etag == local[1].ETag {
				t.Fatalf("failed to replace remote part two: %v", err)
			}
			engine := transfer.New(db)
			if err := engine.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			paused, err := db.Task(ctx, task.ID)
			if err != nil || paused.State != core.Paused {
				t.Fatalf("restart recovery: %v", err)
			}
			probe := &multipartProbe{Client: client}
			if err := engine.RunUpload(ctx, task.ID, c, probe); err != nil {
				t.Fatal(err)
			}
			if probe.lists == 0 || !slices.Equal(probe.uploaded, []int{2, 3, 4}) {
				t.Fatalf("ListParts=%d, uploaded=%v", probe.lists, probe.uploaded)
			}
			finished, err := db.Task(ctx, task.ID)
			if err != nil || finished.State != core.Completed || finished.TransferredBytes != task.Size {
				t.Fatalf("completed task state/progress: %v", err)
			}
			identity, err := client.Head(ctx, obj.Bucket, key)
			if err != nil || identity.Size != task.Size {
				t.Fatalf("wrong remote size: %v", err)
			}
			body, err := client.ReadRange(ctx, obj.Bucket, key, identity, 0, task.Size)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			remoteSHA := sha256.New()
			n, err := io.Copy(remoteSHA, body)
			if err != nil || n != task.Size {
				t.Fatalf("whole remote file: %d, %v", n, err)
			}
			file, err := os.Open(source)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			localSHA := sha256.New()
			if _, err := io.Copy(localSHA, file); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(localSHA.Sum(nil), remoteSHA.Sum(nil)) {
				t.Fatal("multipart object differs from local source")
			}
		})
	}
}

// Introduce a competing writer immediately before CompleteMultipartUpload. A
// HEAD-only existence check cannot protect this case; the final write must be
// conditional on the provider.
type competingWriter struct {
	*s3client.Client
	inserted bool
}

func (w *competingWriter) CompleteUpload(ctx context.Context, bucket, key, id string, parts []core.Part) error {
	empty := sha256.Sum256(nil)
	if err := w.PutEmpty(ctx, bucket, key, "other-upload", hex.EncodeToString(empty[:])); err != nil {
		return err
	}
	w.inserted = true
	return w.Client.CompleteUpload(ctx, bucket, key, id, parts)
}

func TestMultipartNoOverwriteAndCancel(t *testing.T) {
	for _, provider := range []core.Provider{core.AWS, core.R2} {
		t.Run(string(provider), func(t *testing.T) {
			c, client, obj := fixture(t, provider)
			ctx := t.Context()
			key := "upload-test/" + string(provider) + "/occupied.bin"
			dir := t.TempDir()
			db := openDB(t, dir, c)
			engine := transfer.New(db)
			task, err := engine.EnqueueUpload(ctx, c, obj.Bucket, key, randomFile(t, dir, 12345))
			if err != nil {
				t.Fatal(err)
			}
			competitor := &competingWriter{Client: client}
			err = engine.RunUpload(ctx, task.ID, c, competitor)
			var detail *core.Error
			if !competitor.inserted || !errors.As(err, &detail) || detail.Code != "error.uploadExists" {
				t.Fatalf("completed despite conflict or returned wrong error: %v", err)
			}
			failed, err := db.Task(ctx, task.ID)
			if err != nil || failed.State != core.Failed || failed.UploadID == "" {
				t.Fatalf("missing failed upload: %v", err)
			}
			parts, err := client.ListParts(ctx, obj.Bucket, key, failed.UploadID)
			if err != nil || len(parts) != 1 {
				t.Fatalf("expected an incomplete session before cancel: %v", err)
			}
			if err := db.RequestUploadCancel(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			if err := engine.RunUpload(ctx, task.ID, c, client); err != nil {
				t.Fatal(err)
			}
			stopped, err := db.Task(ctx, task.ID)
			if err != nil || stopped.State != core.Canceled {
				t.Fatalf("cancel state: %v", err)
			}
			_, err = client.ListParts(ctx, obj.Bucket, key, failed.UploadID)
			if !errors.As(err, &detail) || detail.Code != "error.uploadSessionMissing" {
				t.Fatalf("multipart session survived abort: %v", err)
			}
			empty := sha256.Sum256(nil)
			ok, err := client.VerifyUpload(ctx, obj.Bucket, key, "other-upload", hex.EncodeToString(empty[:]), 0)
			if err != nil || !ok {
				t.Fatalf("competing object or its metadata was replaced: %v", err)
			}
		})
	}
}
