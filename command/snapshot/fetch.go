package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fetchSnapshot downloads url plus its ".sha256" sidecar, verifies the checksum,
// and saves the result to out. Existing out files are not overwritten.
func fetchSnapshot(ctx context.Context, url, out string) error {
	client := &http.Client{Timeout: 30 * time.Second}

	tmp, err := os.CreateTemp(filepath.Dir(out), ".snapshot-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download snapshot: %s", resp.Status)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

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

	actual, err := sha256File(tmp.Name())
	if err != nil {
		return err
	}
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return errors.New("snapshot checksum mismatch")
	}

	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("output file %q already exists", out)
	} else if !os.IsNotExist(err) {
		return err
	}

	return os.Rename(tmp.Name(), out)
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
