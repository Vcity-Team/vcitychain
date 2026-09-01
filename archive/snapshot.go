package archive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const snapshotChecksumSuffix = ".sha256"

// CreateSnapshot archives srcDir into dstFile as a gzip-compressed tar and
// writes dstFile+".sha256" containing its SHA-256 checksum. It refuses to
// overwrite an existing dstFile and removes partial output on failure.
func CreateSnapshot(srcDir, dstFile string) error {
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("snapshot source %q: %w", srcDir, err)
	}

	file, err := os.OpenFile(dstFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("snapshot output %q: %w", dstFile, err)
	}

	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(dstFile)
		_ = os.Remove(dstFile + snapshotChecksumSuffix)
	}

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	walkErr := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, src)
			_ = src.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
	if walkErr != nil {
		cleanup()
		return walkErr
	}
	if err := tarWriter.Close(); err != nil {
		cleanup()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}

	sum, err := sha256File(dstFile)
	if err != nil {
		cleanup()
		return err
	}
	checksum := fmt.Sprintf("%x  %s\n", sum, filepath.Base(dstFile))
	if err := os.WriteFile(dstFile+snapshotChecksumSuffix, []byte(checksum), 0644); err != nil {
		cleanup()
		return err
	}

	return nil
}

// VerifySnapshot verifies snapshotFile against its sidecar ".sha256" file.
func VerifySnapshot(snapshotFile string) error {
	if _, err := os.Stat(snapshotFile); err != nil {
		return fmt.Errorf("snapshot %q: %w", snapshotFile, err)
	}
	checksumData, err := os.ReadFile(snapshotFile + snapshotChecksumSuffix)
	if err != nil {
		return fmt.Errorf("snapshot checksum %q: %w", snapshotFile+snapshotChecksumSuffix, err)
	}

	fields := strings.Fields(string(checksumData))
	if len(fields) < 2 || fields[1] != filepath.Base(snapshotFile) {
		return errors.New("invalid snapshot checksum format")
	}

	actual, err := sha256File(snapshotFile)
	if err != nil {
		return err
	}
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return errors.New("snapshot checksum mismatch")
	}

	return nil
}

// RestoreSnapshot verifies snapshotFile and extracts it into dstDir, creating
// dstDir when needed. Paths outside dstDir are rejected.
func RestoreSnapshot(snapshotFile, dstDir string) error {
	if err := VerifySnapshot(snapshotFile); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	file, err := os.Open(snapshotFile)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		name := filepath.Clean(filepath.FromSlash(header.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("snapshot contains unsafe path %q", header.Name)
		}
		target := filepath.Join(dstDir, name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & os.ModePerm
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// RestoreSnapshotIfEmpty restores snapshotFile into dataDir only when dataDir
// is missing or empty, so an existing node's data is never overwritten.
func RestoreSnapshotIfEmpty(snapshotFile, dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("data dir %q is not empty; refusing to restore snapshot", dataDir)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return RestoreSnapshot(snapshotFile, dataDir)
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
