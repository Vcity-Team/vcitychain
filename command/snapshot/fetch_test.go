package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchSnapshotSuccess(t *testing.T) {
	dir := t.TempDir()
	snapshotData := []byte("snapshot-bytes")
	checksumData := []byte(fmt.Sprintf("%x  %s\n", sha256Sum(snapshotData), "snapshot.tar.gz"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/snapshot.tar.gz.sha256" {
			_, _ = w.Write(checksumData)
			return
		}
		if r.URL.Path == "/snapshot.tar.gz" {
			_, _ = w.Write(snapshotData)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	out := filepath.Join(dir, "snapshot.tar.gz")
	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err != nil {
		t.Fatalf("fetchSnapshot failed: %v", err)
	}
	if got := readTestFile(t, out); string(got) != string(snapshotData) {
		t.Fatalf("fetched file = %q, want %q", got, snapshotData)
	}
}

func TestFetchSnapshotMissingChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/snapshot.tar.gz" {
			_, _ = w.Write([]byte("data"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, filepath.Join(t.TempDir(), "snapshot.tar.gz"))
	if err == nil {
		t.Fatal("fetchSnapshot should fail when checksum is missing")
	}
}

func TestFetchSnapshotChecksumMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/snapshot.tar.gz.sha256" {
			_, _ = w.Write([]byte("deadbeef  snapshot.tar.gz\n"))
			return
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer server.Close()

	err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, filepath.Join(t.TempDir(), "snapshot.tar.gz"))
	if err == nil {
		t.Fatal("fetchSnapshot should fail on checksum mismatch")
	}
}

func TestFetchSnapshotAllowsDifferentOutputName(t *testing.T) {
	dir := t.TempDir()
	snapshotData := []byte("snapshot-bytes")
	checksumData := []byte(fmt.Sprintf("%x  %s\n", sha256Sum(snapshotData), "snapshot.tar.gz"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/snapshot.tar.gz.sha256" {
			_, _ = w.Write(checksumData)
			return
		}
		if r.URL.Path == "/snapshot.tar.gz" {
			_, _ = w.Write(snapshotData)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	out := filepath.Join(dir, "renamed.tar.gz")
	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err != nil {
		t.Fatalf("fetchSnapshot should allow a different output name: %v", err)
	}
	if got := string(readTestFile(t, out)); got != string(snapshotData) {
		t.Fatalf("fetched file = %q, want %q", got, snapshotData)
	}
}

func TestFetchSnapshotDownloadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, filepath.Join(t.TempDir(), "snapshot.tar.gz"))
	if err == nil {
		t.Fatal("fetchSnapshot should fail on download error")
	}
}

func TestValidateFetchFlags(t *testing.T) {
	tests := []struct {
		name    string
		urls    []string
		out     string
		wantErr bool
	}{
		{name: "all flags set", urls: []string{"http://example/s.tar.gz"}, out: "/s.tar.gz", wantErr: false},
		{name: "missing url", out: "/s.tar.gz", wantErr: true},
		{name: "missing out", urls: []string{"http://example/s.tar.gz"}, wantErr: true},
		{name: "both missing", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &snapshotParams{urls: tt.urls, out: tt.out}
			err := p.validateFetchFlags()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateFetchFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFetchSnapshotFallsBackToMirror(t *testing.T) {
	dir := t.TempDir()
	snapshotData := []byte("mirror-bytes")
	checksumData := []byte(fmt.Sprintf("%x  %s\n", sha256Sum(snapshotData), "snapshot.tar.gz"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mirror/snapshot.tar.gz.sha256" {
			_, _ = w.Write(checksumData)
			return
		}
		if r.URL.Path == "/mirror/snapshot.tar.gz" {
			_, _ = w.Write(snapshotData)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	out := filepath.Join(dir, "snapshot.tar.gz")
	urls := []string{
		server.URL + "/missing/snapshot.tar.gz",
		server.URL + "/mirror/snapshot.tar.gz",
	}
	if err := fetchSnapshot(context.Background(), urls, out); err != nil {
		t.Fatalf("fetchSnapshot should fall back to mirror: %v", err)
	}
	if got := string(readTestFile(t, out)); got != string(snapshotData) {
		t.Fatalf("fetched file = %q, want %q", got, snapshotData)
	}
}

func TestFetchSnapshotAllMirrorsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	err := fetchSnapshot(context.Background(), []string{
		server.URL + "/a/snapshot.tar.gz",
		server.URL + "/b/snapshot.tar.gz",
	}, filepath.Join(t.TempDir(), "snapshot.tar.gz"))
	if err == nil {
		t.Fatal("fetchSnapshot should fail when all mirrors fail")
	}
}

func TestFetchSnapshotResumesPartialDownload(t *testing.T) {
	data := []byte("0123456789abcdef")
	const etag = `"v1"`
	var rangeHeader string
	server := newRangeSnapshotServer(t, data, etag, &rangeHeader)
	defer server.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "snapshot.tar.gz")
	writePartial(t, out, data[:6], server.URL+"/snapshot.tar.gz", etag)

	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err != nil {
		t.Fatalf("fetchSnapshot should resume partial download: %v", err)
	}
	if got := string(readTestFile(t, out)); got != string(data) {
		t.Fatalf("fetched file = %q, want %q", got, data)
	}
	if rangeHeader != "bytes=6-" {
		t.Fatalf("Range header = %q, want %q", rangeHeader, "bytes=6-")
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be removed after success (err=%v)", err)
	}
}

func TestFetchSnapshotRestartsWhenServerIgnoresRange(t *testing.T) {
	data := []byte("0123456789abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(fmt.Sprintf("%x  %s\n", sha256Sum(data), "snapshot.tar.gz")))
			return
		}
		// Ignore Range: always return the full body.
		_, _ = w.Write(data)
	}))
	defer server.Close()

	out := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	writePartial(t, out, data[:4], server.URL+"/snapshot.tar.gz", `"v1"`)

	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err != nil {
		t.Fatalf("fetchSnapshot should restart when server ignores Range: %v", err)
	}
	if got := string(readTestFile(t, out)); got != string(data) {
		t.Fatalf("fetched file = %q, want %q", got, data)
	}
}

func TestFetchSnapshotRestartsWhenValidatorChanges(t *testing.T) {
	data := []byte("new-content")
	const etag = `"v2"`
	server := newRangeSnapshotServer(t, data, etag, nil)
	defer server.Close()

	out := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	writePartial(t, out, []byte("stale"), server.URL+"/snapshot.tar.gz", `"v1"`)

	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err != nil {
		t.Fatalf("fetchSnapshot should restart when validator changes: %v", err)
	}
	if got := string(readTestFile(t, out)); got != string(data) {
		t.Fatalf("fetched file = %q, want %q", got, data)
	}
}

func TestFetchSnapshotKeepsPartialOnInterruptedDownload(t *testing.T) {
	data := []byte("0123456789abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(fmt.Sprintf("%x  %s\n", sha256Sum(data), "snapshot.tar.gz")))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		_, _ = w.Write(data[:5])
	}))
	defer server.Close()

	out := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err == nil {
		t.Fatal("fetchSnapshot should fail on interrupted download")
	}
	part := readTestFile(t, out+".part")
	if len(part) == 0 {
		t.Fatal("partial data should be kept for resume")
	}
}

func TestFetchSnapshotRemovesPartialOnChecksumMismatch(t *testing.T) {
	data := []byte("0123456789abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(fmt.Sprintf("%x  %s\n", sha256Sum([]byte("other")), "snapshot.tar.gz")))
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	out := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := fetchSnapshot(context.Background(), []string{server.URL + "/snapshot.tar.gz"}, out); err == nil {
		t.Fatal("fetchSnapshot should fail on checksum mismatch")
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be removed on checksum mismatch (err=%v)", err)
	}
}

func newRangeSnapshotServer(t *testing.T, data []byte, etag string, rangeHeader *string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(fmt.Sprintf("%x  %s\n", sha256Sum(data), "snapshot.tar.gz")))
			return
		}

		w.Header().Set("ETag", etag)

		if r.Header.Get("If-Range") != "" && r.Header.Get("If-Range") != etag {
			_, _ = w.Write(data)
			return
		}

		rangeValue := r.Header.Get("Range")
		if rangeHeader != nil {
			*rangeHeader = rangeValue
		}
		if rangeValue != "" {
			var start int
			if _, err := fmt.Sscanf(rangeValue, "bytes=%d-", &start); err == nil {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(data[start:])
				return
			}
		}
		_, _ = w.Write(data)
	}))
}

func writePartial(t *testing.T, out string, data []byte, url, validator string) {
	t.Helper()

	if err := os.WriteFile(out+".part", data, 0644); err != nil {
		t.Fatalf("failed to write partial file: %v", err)
	}
	meta, err := json.Marshal(partialDownload{URL: url, Validator: validator})
	if err != nil {
		t.Fatalf("failed to marshal partial meta: %v", err)
	}
	if err := os.WriteFile(out+".part.meta", meta, 0644); err != nil {
		t.Fatalf("failed to write partial meta: %v", err)
	}
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return data
}
