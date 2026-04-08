package rebuildcanonical

import (
	"bytes"
	"fmt"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

type Result struct {
	HeadNumber uint64 `json:"headNumber"`
	HeadHash   string `json:"headHash"`
	ToHeight   uint64 `json:"toHeight"`
	Written    int    `json:"written"`
}

func (r *Result) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[REBUILD CANONICAL]\n")
	buffer.WriteString("Canonical index rebuild completed successfully:\n")
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Head Number|%d", r.HeadNumber),
		fmt.Sprintf("Head Hash|%s", r.HeadHash),
		fmt.Sprintf("To Height|%d", r.ToHeight),
		fmt.Sprintf("Entries Written|%d", r.Written),
	}))

	return buffer.String()
}

