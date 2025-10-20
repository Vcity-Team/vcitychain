package min_threshold

import (
	"github.com/spf13/cobra"
)

// GetCommand 返回最小投票门槛查询命令
func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "min-threshold",
		Short: "查询最小投票门槛",
		Long:  "查询参与投票所需的最小质押门槛",
		RunE: func(cmd *cobra.Command, args []string) error {
			command := NewMinThresholdCommand()
			return command.Execute(args)
		},
	}

	// 添加服务器地址参数
	cmd.Flags().String("server", "http://localhost:8545", "JSON-RPC服务器地址")

	return cmd
}
