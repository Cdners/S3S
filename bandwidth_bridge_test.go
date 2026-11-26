package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/zalando/go-keyring"
)

// Retry a truncated range after reopening the database, concurrently with a
// download on another account. Both go through real SDK signers, SQLite task
// transitions and atomic publication; the injected transport never dials out.
func TestDownloadsShareLivePersistedLimitsAcrossRetry(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	ctx := t.Context()
	w := openWorkspaceAt(dir)
	defer func() { w.close() }()
	profiles := []core.Connection{
		{Name: "first", Provider: core.AWS, Region: "us-west-2"},
		{Name: "second", Provider: core.S3Compatible, Endpoint: "https://objects.example.test"},
	}
	for i, c := range profiles {
		keys := accountSecrets()
		result := w.SaveAccount(ctx, nil, c, &keys)
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		profiles[i] = *result.Data
	}
	const size = 16 << 10
	payload := bytes.Repeat([]byte{0xce}, size)
	var gets atomic.Int32
	reached := make(chan string, 2)
	release := make(chan struct{})
	transport := rejectUploadRequest(func(r *http.Request) (*http.Response, error) {
		headers := make(http.Header)
		headers.Set("ETag", `"rev"`)
		headers.Set("Content-Length", strconv.Itoa(size))
		result := &http.Response{Request: r, StatusCode: http.StatusOK, Header: headers, ContentLength: size, Body: http.NoBody}
		if r.Method == http.MethodHead {
			return result, nil
		}
		if r.Method != http.MethodGet || r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", size-1) || r.Header.Get("If-Match") != `"rev"` {
			return nil, fmt.Errorf("unexpected object request: %s", r.Method)
		}
		result.StatusCode = http.StatusPartialContent
		headers.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", size-1, size))
		data := payload
		if gets.Add(1) == 1 {
			data = payload[:size/2] // Drop the connection midway through the first range.
		} else {
			reached <- r.URL.Host
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		result.Body = io.NopCloser(bytes.NewReader(data))
		return result, nil
	})
	choose := func(title string) (string, error) { return filepath.Join(dir, title+".bin"), nil }
	w.chooseDestination, w.storageTransport = choose, transport
	first := w.StartDownload(ctx, profiles[0], "known", "one", "first")
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	joinWorker(t, w, first.Data)
	failed, err := w.store.Task(ctx, first.Data)
	if err != nil || failed.State != core.Failed || failed.TransferredBytes != 0 {
		t.Fatalf("truncated range result: state=%s checkpoint=%d err=%v", failed.State, failed.TransferredBytes, err)
	}
	slow := core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: 444, DownloadBytesPerSecond: 1}
	if saved := w.SaveTransferOptions(ctx, slow); saved.Error != nil || saved.Data != slow {
		t.Fatalf("save custom: %v", saved.Error)
	}
	w.close()
	w = openWorkspaceAt(dir)
	w.chooseDestination, w.storageTransport = choose, transport
	if stored := w.GetTransferOptions(ctx); stored.Error != nil || stored.Data != slow {
		t.Fatal("persisted budgets changed on reopen")
	}

	if resumed := w.ResumeDownload(ctx, first.Data); resumed.Error != nil {
		t.Fatal(resumed.Error)
	}
	second := w.StartDownload(ctx, profiles[1], "known", "two", "second")
	if second.Error != nil {
		t.Fatal(second.Error)
	}
	hosts := make(map[string]bool)
	for range 2 {
		select {
		case h := <-reached:
			hosts[h] = true
		case <-time.After(10 * time.Second):
			t.Fatal("workers did not reach transport")
		}
	}
	if !hosts["s3.us-west-2.amazonaws.com"] || !hosts["objects.example.test"] {
		t.Fatal("workers did not use distinct configured origins")
	}
	close(release)
	// At one byte/s neither task can finish before this live edit. If the
	// application kept using its old budget the workers could not finish.
	limit := core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: 444, DownloadBytesPerSecond: 32 << 10}
	start := time.Now()
	if saved := w.SaveTransferOptions(ctx, limit); saved.Error != nil {
		t.Fatal(saved.Error)
	}
	joinWorker(t, w, first.Data)
	joinWorker(t, w, second.Data)
	// Together 32 KiB at 32 KiB/s takes one second (allow a small burst).
	// Two independent account budgets could finish in half that time.
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("downloads, including the retried range, exceeded the application budget")
	}
	for _, name := range []string{"first", "second"} {
		got, err := os.ReadFile(filepath.Join(dir, name+".bin"))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("published %s: %v", name, err)
		}
	}
	if gets.Load() != 3 {
		t.Fatalf("unexpected range request count: %d", gets.Load())
	}
}
