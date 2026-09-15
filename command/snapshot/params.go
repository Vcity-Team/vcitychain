package snapshot

import (
	"errors"
	"fmt"
)

const (
	dataDirFlag  = "data-dir"
	outFlag      = "out"
	snapshotFlag = "snapshot"
	urlFlag      = "url"
	heightFlag   = "height"
	hashFlag     = "hash"
	keyFileFlag  = "key-file"
	pubkeyFlag   = "pubkey"
)

var (
	errDataDirRequired  = errors.New("data-dir is required")
	errOutRequired      = errors.New("out is required")
	errSnapshotRequired = errors.New("snapshot is required")
	errURLRequired      = errors.New("url is required")
	errKeyFileRequired  = errors.New("key-file is required")
	errUnknownAction    = errors.New("unknown action; use create, restore, fetch, verify, info, keygen or sign")
)

var params = &snapshotParams{}

type snapshotParams struct {
	dataDir  string
	out      string
	snapshot string
	urls     []string
	height   string
	hash     string
	keyFile  string
	pubkey   string
}

func validateAction(action string) error {
	switch action {
	case "create", "restore", "fetch", "verify", "info", "keygen", "sign":
		return nil
	default:
		return fmt.Errorf("%w: %s", errUnknownAction, action)
	}
}

func (p *snapshotParams) validateCreateFlags() error {
	if p.dataDir == "" {
		return errDataDirRequired
	}
	if p.out == "" {
		return errOutRequired
	}
	return nil
}

func (p *snapshotParams) validateRestoreFlags() error {
	if p.snapshot == "" {
		return errSnapshotRequired
	}
	if p.dataDir == "" {
		return errDataDirRequired
	}
	return nil
}

func (p *snapshotParams) validateFetchFlags() error {
	if len(p.urls) == 0 {
		return errURLRequired
	}
	if p.out == "" {
		return errOutRequired
	}
	return nil
}

func (p *snapshotParams) validateSnapshotFileFlag() error {
	if p.snapshot == "" {
		return errSnapshotRequired
	}
	return nil
}

func (p *snapshotParams) validateKeygenFlags() error {
	if p.keyFile == "" {
		return errKeyFileRequired
	}
	return nil
}

func (p *snapshotParams) validateSignFlags() error {
	if p.snapshot == "" {
		return errSnapshotRequired
	}
	if p.keyFile == "" {
		return errKeyFileRequired
	}
	return nil
}
