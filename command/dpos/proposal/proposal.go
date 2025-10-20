package proposal

import (
	"github.com/spf13/cobra"
)

// GetCommand 获取提案命令组
func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proposal",
		Short: "参数提案管理",
		Long:  "管理DPoS参数修改提案",
	}

	// 添加子命令
	cmd.AddCommand(GetCreateCommand())        // create-proposal
	cmd.AddCommand(GetVoteCommand())          // vote
	cmd.AddCommand(GetProposalCommand())      // get
	cmd.AddCommand(GetCheckResultCommand())   // check-result
	cmd.AddCommand(GetExecuteUpdateCommand()) // execute

	return cmd
}
