package snapshot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
