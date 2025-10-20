package min_threshold

import (
	"fmt"
	"os"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

// MinThresholdCommand 最小投票门槛查询命令
type MinThresholdCommand struct {
	*helper.BaseCommand
}

// GetName 返回命令名称
func (c *MinThresholdCommand) GetName() string {
	return "min-threshold"
}

// GetDescription 返回命令描述
func (c *MinThresholdCommand) GetDescription() string {
	return "查询最小投票门槛"
}

// GetUsage 返回命令用法
func (c *MinThresholdCommand) GetUsage() string {
	return "dpos min-threshold [--server <server>]"
}

// Execute 执行命令
func (c *MinThresholdCommand) Execute(args []string) error {
	// 解析参数
	server := "http://localhost:8545"

	for i, arg := range args {
		switch arg {
		case "--server":
			if i+1 < len(args) {
				server = args[i+1]
			} else {
				return fmt.Errorf("--server requires a value")
			}
		}
	}

	// 获取最小投票门槛
	result, err := GetMinVotingThreshold(server)
	if err != nil {
		return fmt.Errorf("获取最小投票门槛失败: %w", err)
	}

	// 输出结果
	if result.Success {
		fmt.Printf("最小投票门槛: %s wei\n", result.Threshold)
		fmt.Printf("说明: %s\n", result.Message)
	} else {
		fmt.Printf("获取失败: %s\n", result.Message)
		os.Exit(1)
	}

	return nil
}

// NewMinThresholdCommand 创建最小投票门槛查询命令
func NewMinThresholdCommand() *MinThresholdCommand {
	return &MinThresholdCommand{
		BaseCommand: helper.NewBaseCommand(),
	}
}
