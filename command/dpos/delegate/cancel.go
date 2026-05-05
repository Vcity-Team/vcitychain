package delegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

type cancelSubmitResult struct {
	Success  bool   `json:"success"`
	TxHash   string `json:"txHash,omitempty"`
	Delegate string `json:"delegate,omitempty"`
	Escrow   string `json:"escrow,omitempty"`
	Note     string `json:"note,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r *cancelSubmitResult) GetOutput() string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", r)
	}
	return string(b)
}

var cancelParams = &cancelDelegateParams{}

type cancelDelegateParams struct {
	jsonRPC    string
	chainID    uint64
	address    string
	privateKey string
}

// GetCancelCommand submits DPOS+CAN on-chain cancel (native refund when mined).
func GetCancelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "cancel",
		Short:        "Submit on-chain cancel registration (DPOS+CAN) to refund deposit from escrow",
		Long:         "Calls JSON-RPC dpos_submitCancelRegisterDelegate. Refund executes when the tx is included in a block.",
		PreRunE:      cancelPreRun,
		RunE:         cancelRun,
		SilenceUsage: true,
	}
	helper.RegisterJSONRPCFlag(cmd)
	cmd.Flags().StringVar(&cancelParams.address, "address", "", "delegate address (required)")
	cmd.Flags().StringVar(&cancelParams.privateKey, "private-key", "", "private key hex 64 chars (required)")
	cmd.Flags().Uint64Var(&cancelParams.chainID, "chain-id", 0, "chain ID (required, for client consistency)")
	_ = cmd.MarkFlagRequired("address")
	_ = cmd.MarkFlagRequired("private-key")
	_ = cmd.MarkFlagRequired("chain-id")
	return cmd
}

func cancelPreRun(cmd *cobra.Command, _ []string) error {
	j, err := cmd.Flags().GetString("jsonrpc")
	if err != nil {
		return err
	}
	cancelParams.jsonRPC = j
	if cancelParams.chainID == 0 {
		return fmt.Errorf("missing --chain-id")
	}
	if !strings.HasPrefix(cancelParams.address, "0x") || len(cancelParams.address) != 42 {
		return fmt.Errorf("invalid --address")
	}
	pk := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(cancelParams.privateKey, "0x"), "0X"))
	if len(pk) != 64 {
		return fmt.Errorf("invalid --private-key length")
	}
	cancelParams.privateKey = pk
	return nil
}

func cancelRun(cmd *cobra.Command, _ []string) error {
	out := command.InitializeOutputter(cmd)
	defer out.WriteOutput()

	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_submitCancelRegisterDelegate",
		"params": map[string]interface{}{
			"delegate":   cancelParams.address,
			"privateKey": cancelParams.privateKey,
		},
		"id": 1,
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(cancelParams.jsonRPC, "application/json", bytes.NewBuffer(raw))
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var wrap struct {
		Result *cancelSubmitResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	if wrap.Error != nil {
		return fmt.Errorf("rpc error: %s", wrap.Error.Message)
	}
	if wrap.Result == nil {
		return fmt.Errorf("empty rpc result")
	}
	out.SetCommandResult(wrap.Result)
	return nil
}
