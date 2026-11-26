package bandwidth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
)

func TestAggregatePayloadRateAcrossClientsAndRetry(t *testing.T) {
	const rate = 64 << 10
	const size = 8 << 10
	data := bytes.Repeat([]byte{7}, size)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			got, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(got, data) {
				t.Error("upload bytes changed")
			}
			if r.URL.Path == "/first" {
				w.WriteHeader(503)
			}
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	for _, dir := range []core.Direction{core.Upload, core.Download} {
		t.Run(string(dir), func(t *testing.T) {
			lim := New(core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: rate, DownloadBytesPerSecond: rate})
			start := time.Now()
			var workers sync.WaitGroup
			// Two accounts each exchange two payloads. A repeated request after
			// a complete but rejected PUT consumes the same allowance again.
			for range 2 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					client := http.Client{Transport: lim.Transport(server.Client().Transport), Timeout: 5 * time.Second}
					for _, path := range []string{"/first", "/retry"} {
						method, body := http.MethodGet, io.Reader(nil)
						if dir == core.Upload {
							method, body = http.MethodPut, bytes.NewReader(data)
						}
						req, err := http.NewRequestWithContext(WithPayload(t.Context(), dir), method, server.URL+path, body)
						if err != nil {
							t.Error(err)
							return
						}
						resp, err := client.Do(req)
						if err != nil {
							t.Error(err)
							return
						}
						got, err := io.ReadAll(resp.Body)
						resp.Body.Close()
						if err != nil || dir == core.Download && !bytes.Equal(got, data) {
							t.Error("download bytes changed")
						}
					}
				}()
			}
			workers.Wait()
			// Four independently metered requests would finish much earlier.
			// Allow one full idle burst plus a small scheduling margin.
			if elapsed := time.Since(start); elapsed < 440*time.Millisecond {
				t.Fatalf("32768 bytes exceeded shared 65536 B/s budget in %s", elapsed)
			}
		})
	}
}

func TestMetadataAndOppositeDirectionUnaffected(t *testing.T) {
	data := bytes.Repeat([]byte{'m'}, 9000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(data) }))
	defer server.Close()
	l := New(core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: 1, DownloadBytesPerSecond: 1})
	client := &http.Client{Transport: l.Transport(nil), Timeout: 2 * time.Second}
	get := func(ctx context.Context) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("unexpected payload: %v", err)
		}
	}
	get(t.Context()) // unmarked SDK metadata
	l.Set(core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: 1})
	get(WithPayload(t.Context(), core.Download))
}

type waitingBody struct {
	reader  *bytes.Reader
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (b *waitingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.proceed })
	return b.reader.Read(p)
}
func (*waitingBody) Close() error { return nil }

func TestLiveRateChangeRetainsBufferedBytes(t *testing.T) {
	l := New(core.TransferOptions{Mode: core.FullSpeed})
	b := &waitingBody{reader: bytes.NewReader([]byte("abcdefgh")), started: make(chan struct{}), proceed: make(chan struct{})}
	body := pace(t.Context(), b, &l.download)
	defer body.Close()
	first := make(chan string, 1)
	go func() {
		p := make([]byte, 8)
		n, err := body.Read(p)
		if err != nil {
			first <- err.Error()
			return
		}
		first <- string(p[:n])
	}()
	<-b.started
	// Read began unlimited; by the time data arrives grant at most one byte.
	l.Set(core.TransferOptions{Mode: core.Custom, DownloadBytesPerSecond: 20})
	close(b.proceed)
	if got := <-first; got != "a" {
		t.Fatalf("delivered %q after rate reduction", got)
	}
	l.Set(core.TransferOptions{Mode: core.FullSpeed})
	rest, err := io.ReadAll(body)
	if err != nil || string(rest) != "bcdefgh" {
		t.Fatalf("dropped buffered bytes: %q %v", rest, err)
	}
}

func TestWaitingReadersRespectModeChangePauseAndClose(t *testing.T) {
	for _, action := range []string{"faster", "cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			l := New(core.TransferOptions{Mode: core.Custom, UploadBytesPerSecond: 1})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b := &waitingBody{reader: bytes.NewReader([]byte("x")), started: make(chan struct{}), proceed: make(chan struct{})}
			body := pace(ctx, b, &l.upload)
			done := make(chan error, 1)
			go func() {
				p := make([]byte, 1)
				n, err := body.Read(p)
				if err == nil && (n != 1 || p[0] != 'x') {
					err = fmt.Errorf("bad bytes")
				}
				done <- err
			}()
			<-b.started
			close(b.proceed)
			switch action {
			case "faster":
				l.Set(core.TransferOptions{Mode: core.FullSpeed})
			case "cancel":
				cancel()
			case "close":
				body.Close()
			}
			select {
			case err := <-done:
				if action == "faster" && err != nil || action != "faster" && !errors.Is(err, context.Canceled) {
					t.Fatalf("read: %v", err)
				}
			case <-time.After(600 * time.Millisecond):
				t.Fatal("reader waited for the old budget")
			}
			body.Close()
		})
	}
}
