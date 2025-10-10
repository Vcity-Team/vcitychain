package epoch

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the epoch command
func GetCommand() *cobra.Command {
	epochCmd := &cobra.Command{
		Use:   "epoch [epochNumber]",
		Short: "Get DPoS epoch information",
		Long:  "Get current epoch information or specific epoch by number",
		Args:  cobra.MaximumNArgs(1),
		Run:   runCommand,
	}

	helper.RegisterJSONOutputFlag(epochCmd)

	return epochCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// 构建JSON-RPC请求
	var method string
	var params []interface{}

	if len(args) == 0 {
		// 获取当前epoch信息
		method = "dpos_getCurrentEpochInfo"
		params = []interface{}{}
	} else {
		// 获取指定epoch信息
		method = "dpos_getEpochInfoByNumber"
		params = []interface{}{args[0]}
	}

	// 调用JSON-RPC
	result, err := callJSONRPC(method, params)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to call JSON-RPC: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &EpochResult{
		Data: result.(map[string]interface{}),
	}
	outputter.SetCommandResult(commandResult)
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

