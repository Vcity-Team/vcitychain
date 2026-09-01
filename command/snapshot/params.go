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
)

var (
	errDataDirRequired  = errors.New("data-dir is required")
	errOutRequired      = errors.New("out is required")
	errSnapshotRequired = errors.New("snapshot is required")
	errURLRequired      = errors.New("url is required")
	errUnknownAction    = errors.New("unknown action; use create, restore or fetch")
)

var params = &snapshotParams{}

type snapshotParams struct {
	dataDir  string
	out      string
	snapshot string
	urls     []string
}

func validateAction(action string) error {
	if action != "create" && action != "restore" && action != "fetch" {
		return fmt.Errorf("%w: %s", errUnknownAction, action)
	}
	return nil
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
