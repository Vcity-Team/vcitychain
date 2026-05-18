package rollback

import (
	"bytes"
	"fmt"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

type RollbackResult struct {
	CurrentHeight    uint64 `json:"currentHeight"`
	TargetHeight     uint64 `json:"targetHeight"`
	TargetHash       string `json:"targetHash"`
	BlocksDeleted    uint64 `json:"blocksDeleted"`
	KeepBlocks       bool   `json:"keepBlocks"`
	StateHealed      bool   `json:"stateHealed"`
	StateRootChecked bool   `json:"stateRootChecked"`
	BlocksReplayed   uint64 `json:"blocksReplayed"`
	FinalHeadHeight  uint64 `json:"finalHeadHeight"`
}

func (r *RollbackResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[ROLLBACK]\n")
	buffer.WriteString("Rollback completed successfully:\n")
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Current Height|%d", r.CurrentHeight),
		fmt.Sprintf("Target Height|%d", r.TargetHeight),
		fmt.Sprintf("Target Hash|%s", r.TargetHash),
		fmt.Sprintf("Blocks Deleted|%d", r.BlocksDeleted),
		fmt.Sprintf("Keep Blocks|%v", r.KeepBlocks),
		fmt.Sprintf("State Healed|%v", r.StateHealed),
		fmt.Sprintf("State Root Checked|%v", r.StateRootChecked),
		fmt.Sprintf("Blocks Replayed|%d", r.BlocksReplayed),
		fmt.Sprintf("Final Head Height|%d", r.FinalHeadHeight),
	}))

	return buffer.String()
}


