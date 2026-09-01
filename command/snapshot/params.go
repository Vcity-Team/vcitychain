package snapshot

import (
	"errors"
	"fmt"
)

const (
	dataDirFlag  = "data-dir"
	outFlag      = "out"
	snapshotFlag = "snapshot"
)

var (
	errDataDirRequired  = errors.New("data-dir is required")
	errOutRequired      = errors.New("out is required")
	errSnapshotRequired = errors.New("snapshot is required")
	errUnknownAction    = errors.New("unknown action; use create or restore")
)

var params = &snapshotParams{}

type snapshotParams struct {
	dataDir  string
	out      string
	snapshot string
}

func validateAction(action string) error {
	if action != "create" && action != "restore" {
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
