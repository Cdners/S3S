//go:build s3integration

// Package integration_test runs only in the offline fixture created by run.sh.
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

type fixtureObject struct {
	Bucket, Key, SHA256 string
	Size                int64
}

func readJSON(t *testing.T, path string, dest any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, dest); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, provider core.Provider) (core.Connection, *s3client.Client, fixtureObject) {
	t.Helper()
	host, region := "s3.us-east-1.amazonaws.com", "us-east-1"
	if provider == core.R2 {
		host, region = "00000000000000000000000000000000.r2.cloudflarestorage.com", "auto"
	}
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		t.Fatal("missing offline test hostname")
	}
	for _, addr := range addrs {
		if !addr.IsLoopback() {
			t.Fatal("endpoint must be loopback")
		}
	}
	c := core.Connection{ID: string(provider), Name: "Fixture", Provider: provider, Region: region, Endpoint: "https://" + host}
	var keys struct{ AccessKeyID, SecretAccessKey string }
	readJSON(t, "/fixture/credentials.json", &keys)
	client, err := s3client.New(c, s3client.Credentials{AccessKeyID: keys.AccessKeyID, SecretAccessKey: keys.SecretAccessKey})
	if err != nil {
		t.Fatal(err)
	}
	var obj fixtureObject
	readJSON(t, "/fixture/object.json", &obj)
	return c, client, obj
}

func TestProviderBrowse(t *testing.T) {
	for _, provider := range []core.Provider{core.AWS, core.R2} {
		t.Run(string(provider), func(t *testing.T) {
			_, client, obj := fixture(t, provider)
			ctx := t.Context()
			buckets, err := client.ListBuckets(ctx, "")
			if err != nil || !slices.Contains(buckets.Buckets, obj.Bucket) {
				t.Fatalf("list buckets: %v", err)
			}
			var keys, prefixes []string
			first, err := client.ListObjects(ctx, obj.Bucket, "page/", "")
			if err != nil || first.NextToken == "" {
				t.Fatalf("missing first page/token: %v", err)
			}
			last, err := client.ListObjects(ctx, obj.Bucket, "page/", first.NextToken)
			if err != nil || last.NextToken != "" {
				t.Fatalf("unexpected final page: %v", err)
			}
			for _, page := range []s3client.ObjectPage{first, last} {
				for _, entry := range page.Objects {
					keys = append(keys, entry.Key)
				}
				prefixes = append(prefixes, page.Prefixes...)
			}
			if len(keys) != 103 || !slices.Equal(prefixes, []string{"page/child/"}) {
				t.Fatalf("wrong contents: %d keys, %v", len(keys), prefixes)
			}
			for i, key := range keys {
				if key != fmt.Sprintf("page/%03d", i) {
					t.Fatalf("wrong key order at %d", i)
				}
			}
			page, err := client.ListObjects(ctx, obj.Bucket, obj.Key, "")
			if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != obj.Key || page.Objects[0].Size != obj.Size {
				t.Fatalf("opaque key round trip failed: objects=%v, err=%v", page.Objects, err)
			}
			head, err := client.Head(ctx, obj.Bucket, obj.Key)
			if err != nil || head.Size != obj.Size {
				t.Fatalf("Head size=%d, err=%v", head.Size, err)
			}
			body, err := client.ReadRange(ctx, obj.Bucket, obj.Key, head, 10, 39)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			n, err := io.Copy(io.Discard, body)
			if err != nil || n != 39 {
				t.Fatalf("range returned %d bytes, err=%v", n, err)
			}
		})
	}
}

// The child actually exits after the first checkpoint. No worker cleanup or orderly
// database shutdown occurs, and every successful range is served by the S3 emulator.
type observedSource struct {
	*s3client.Client
	crash   bool
	offsets []int64
}

func (s *observedSource) ReadRange(ctx context.Context, bucket, key string, identity core.ObjectIdentity, offset, length int64) (io.ReadCloser, error) {
	if s.crash && len(s.offsets) > 0 {
		os.Exit(32)
	}
	s.offsets = append(s.offsets, offset)
	return s.Client.ReadRange(ctx, bucket, key, identity, offset, length)
}

func TestDownloadAfterProcessExit(t *testing.T) {
	c, client, obj := fixture(t, core.AWS)
	ctx := t.Context()
	folder := os.Getenv("S3S_CRASH_FIXTURE")
	if folder != "" {
		db, err := store.Open(filepath.Join(folder, "test.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		if err = db.SaveConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
		source := &observedSource{Client: client, crash: true}
		engine := transfer.New(db)
		task, err := engine.Enqueue(ctx, c, source, obj.Bucket, obj.Key, filepath.Join(folder, "download.bin"))
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("expected process exit before Run returned: %v", engine.Run(ctx, task.ID, c, source))
	}
	folder = t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadAfterProcessExit$")
	cmd.Env = append(os.Environ(), "S3S_CRASH_FIXTURE="+folder)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 32 {
		t.Fatalf("child process: %v\n%s", err, out)
	}
	db, err := store.Open(filepath.Join(folder, "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks, err := db.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].State != core.Running {
		t.Fatalf("interrupted task: %v", err)
	}
	task := tasks[0]
	parts, err := db.Parts(ctx, task.ID)
	if err != nil || len(parts) != 1 || parts[0].Offset != 0 {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := os.Stat(task.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete destination visible")
	}
	engine := transfer.New(db)
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.Task(ctx, task.ID)
	if err != nil || recovered.State != core.Paused {
		t.Fatalf("recovery did not pause task: %v", err)
	}
	source := &observedSource{Client: client}
	if err := engine.Run(ctx, task.ID, c, source); err != nil {
		t.Fatal(err)
	}
	if len(source.offsets) == 0 || source.offsets[0] != parts[0].Size || slices.Contains(source.offsets, 0) {
		t.Fatalf("incorrect resumed offsets: %v", source.offsets)
	}
	got, err := os.ReadFile(task.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != obj.SHA256 {
		t.Fatal("published bytes differ from the independently seeded fixture")
	}
	finished, err := db.Task(ctx, task.ID)
	if err != nil || finished.State != core.Completed || finished.TransferredBytes != obj.Size {
		t.Fatalf("final state/progress mismatch: %v", err)
	}
}
