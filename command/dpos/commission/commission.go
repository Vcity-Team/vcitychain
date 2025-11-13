package commission

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the root commission command
func GetCommand() *cobra.Command {
	commissionCmd := &cobra.Command{
		Use:   "commission",
		Short: "Validator commission operations",
		Long:  "Commands for inspecting validator commission configuration",
	}

	helper.RegisterJSONOutputFlag(commissionCmd)

	commissionCmd.AddCommand(newGetCommand())

	return commissionCmd
}

func newGetCommand() *cobra.Command {
	var validator string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get validator commission information",
		Run: func(cmd *cobra.Command, args []string) {
			runGetCommand(cmd, validator)
		},
	}

	cmd.Flags().StringVar(&validator, "validator", "", "Validator address (0x...)")
	cmd.MarkFlagRequired("validator")

	helper.RegisterJSONOutputFlag(cmd)

	return cmd
}

func runGetCommand(cmd *cobra.Command, validator string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	validator = strings.TrimSpace(validator)
	if validator == "" {
		outputter.SetError(fmt.Errorf("validator address is required"))
		return
	}

	info, err := fetchValidatorCommission(validator)
	if err != nil {
		outputter.SetError(err)
		return
	}

	outputter.SetCommandResult(&CommissionResult{Data: info})
}

func fetchValidatorCommission(validator string) (map[string]interface{}, error) {
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getValidatorCommission",
		"params":  []interface{}{validator},
		"id":      1,
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://localhost:8545", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return map[string]interface{}{
			"validator": validator,
			"status":    "rpc_unavailable",
			"error":     err.Error(),
		}, nil
	}
	defer resp.Body.Close()

	var jsonrpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&jsonrpcResp); err != nil {
		return map[string]interface{}{
			"validator": validator,
			"status":    "invalid_response",
			"error":     err.Error(),
		}, nil
	}

	if rpcErr, exists := jsonrpcResp["error"]; exists {
		return map[string]interface{}{
			"validator": validator,
			"status":    "rpc_error",
			"error":     rpcErr,
		}, nil
	}

	if result, exists := jsonrpcResp["result"]; exists {
		if resultMap, ok := result.(map[string]interface{}); ok {
			return resultMap, nil
		}
	}

	return map[string]interface{}{
		"validator": validator,
		"status":    "no_result",
	}, nil
}

// CommissionResult implements command.CommandResult
func (r *CommissionResult) GetOutput() string {
	if r == nil {
		return ""
	}

	if r.Data == nil {
		return "{}"
	}

	bytes, err := json.MarshalIndent(r.Data, "", "  ")
	if err != nil {
		return fmt.Sprintf("failed to marshal commission result: %v", err)
	}

	return string(bytes)
}

type CommissionResult struct {
	Data map[string]interface{} `json:"data"`
}
