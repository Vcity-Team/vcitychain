package rewards

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the rewards command
func GetCommand() *cobra.Command {
	rewardsCmd := &cobra.Command{
		Use:   "rewards <validatorAddress> <epochNumber>",
		Short: "Get validator reward information",
		Long:  "Get reward information for a specific validator in a specific epoch",
		Args:  cobra.ExactArgs(2),
		Run:   runCommand,
	}

	helper.RegisterJSONOutputFlag(rewardsCmd)

	return rewardsCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := helper.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	validatorAddress := args[0]
	epochNumber := args[1]

	// 构建JSON-RPC请求
	method := "dpos_getValidatorRewardsInfo"
	params := []interface{}{validatorAddress, epochNumber}

	// 调用JSON-RPC
	result, err := callJSONRPC(method, params)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to call JSON-RPC: %w", err))
		return
	}

	outputter.SetResult(result)
}

func callJSONRPC(method string, params []interface{}) (interface{}, error) {
	// 这里应该实现实际的JSON-RPC调用
	// 暂时返回模拟数据
	return map[string]interface{}{
		"method": method,
		"params": params,
		"note":   "JSON-RPC call implementation needed",
	}, nil
}
