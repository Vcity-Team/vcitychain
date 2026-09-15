package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	partialSuffix     = ".part"
	partialMetaSuffix = ".part.meta"
)

// partialDownload records resume state for an interrupted snapshot download.
type partialDownload struct {
	URL       string `json:"url"`
	Validator string `json:"validator,omitempty"`
	TotalSize int64  `json:"totalSize,omitempty"`
}

// fetchSnapshot tries each URL in order, downloading it plus its ".sha256"
// sidecar, verifying the checksum, and saving the first valid result to out.
// Existing out files are not overwritten.
func fetchSnapshot(ctx context.Context, urls []string, out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("output file %q already exists", out)
	} else if !os.IsNotExist(err) {
		return err
	}

	var errs []error
	for _, url := range urls {
		if err := tryFetch(ctx, url, out); err == nil {
			return nil
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", url, err))
		}
	}

	return fmt.Errorf("failed to fetch snapshot from any URL: %v", errs)
}

func tryFetch(ctx context.Context, url, out string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	partPath := out + partialSuffix
	metaPath := out + partialMetaSuffix

	if err := downloadSnapshot(ctx, client, url, partPath, metaPath); err != nil {
		return err
	}

	if err := verifyFetchedSnapshot(client, url, partPath); err != nil {
		// A complete-but-invalid file cannot be resumed safely.
		_ = os.Remove(partPath)
		_ = os.Remove(metaPath)
		return err
	}

	if err := os.Rename(partPath, out); err != nil {
		return err
	}
	_ = os.Remove(metaPath)

	return nil
}

// downloadSnapshot downloads url into partPath, resuming from an existing
// partial file when the server supports Range and the resource is unchanged.
// Partial data is kept on failure so a later run can resume it.
func downloadSnapshot(ctx context.Context, client *http.Client, url, partPath, metaPath string) error {
	offset, validator, total := resumeState(url, partPath, metaPath)
	if total > 0 && offset == total {
		return nil // already complete, waiting for verification
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if offset > 0 && validator != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", validator)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if offset == 0 {
			return errors.New("server returned partial content without a resume request")
		}
		start, contentTotal, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil || start != offset {
			return fmt.Errorf("invalid Content-Range %q for offset %d", resp.Header.Get("Content-Range"), offset)
		}
		total = contentTotal
	case http.StatusOK:
		offset = 0
		if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	default:
		return fmt.Errorf("failed to download snapshot: %s", resp.Status)
	}

	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()

	if total == 0 && resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}
	_ = writePartialDownload(metaPath, &partialDownload{
		URL:       url,
		Validator: responseValidator(resp),
		TotalSize: total,
	})

	if copyErr != nil {
		return copyErr // keep partial data for the next resume
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.ContentLength > 0 && written != resp.ContentLength {
		return fmt.Errorf("incomplete download: got %d bytes, want %d", written, resp.ContentLength)
	}

	return nil
}

func resumeState(url, partPath, metaPath string) (offset int64, validator string, total int64) {
	meta, err := readPartialDownload(metaPath)
	if err != nil || meta.URL != url {
		_ = os.Remove(partPath)
		_ = os.Remove(metaPath)
		return 0, "", 0
	}

	info, err := os.Stat(partPath)
	if err != nil {
		_ = os.Remove(metaPath)
		return 0, "", 0
	}

	return info.Size(), meta.Validator, meta.TotalSize
}

func parseContentRange(value string) (start, total int64, err error) {
	var end int64
	if _, err = fmt.Sscanf(value, "bytes %d-%d/%d", &start, &end, &total); err != nil {
		return 0, 0, err
	}
	return start, total, nil
}

func responseValidator(resp *http.Response) string {
	if etag := resp.Header.Get("ETag"); etag != "" {
		return etag
	}
	return resp.Header.Get("Last-Modified")
}

func verifyFetchedSnapshot(client *http.Client, url, partPath string) error {
	sumResp, err := client.Get(url + ".sha256")
	if err != nil {
		return err
	}
	defer sumResp.Body.Close()
	if sumResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download snapshot checksum: %s", sumResp.Status)
	}

	sumData, err := io.ReadAll(sumResp.Body)
	if err != nil {
		return err
	}

	fields := strings.Fields(string(sumData))
	if len(fields) < 1 {
		return errors.New("invalid snapshot checksum format")
	}

	actual, err := sha256File(partPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return errors.New("snapshot checksum mismatch")
	}

	return nil
}

func readPartialDownload(path string) (*partialDownload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta partialDownload
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

func writePartialDownload(path string, meta *partialDownload) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func sha256File(path string) ([32]byte, error) {
	var sum [32]byte

	file, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return sum, err
	}
	copy(sum[:], hasher.Sum(nil))

	return sum, nil
}
