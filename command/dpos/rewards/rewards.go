package rewards

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
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
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	validatorAddress := args[0]
	epochNumberStr := args[1]

	// 解析epoch号
	epochNumber, err := strconv.ParseUint(epochNumberStr, 10, 64)
	if err != nil {
		outputter.SetError(fmt.Errorf("invalid epoch number: %w", err))
		return
	}

	// 验证地址格式
	if !isValidAddress(validatorAddress) {
		outputter.SetError(fmt.Errorf("invalid validator address: %s", validatorAddress))
		return
	}

	// 获取奖励信息
	rewardsInfo, err := getValidatorRewards(validatorAddress, epochNumber)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to get validator rewards: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &ValidatorRewardsResult{
		Data: rewardsInfo,
	}
	outputter.SetCommandResult(commandResult)
}

// getValidatorRewards 获取验证者奖励信息
func getValidatorRewards(validatorAddress string, epochNumber uint64) (map[string]interface{}, error) {
	// 由于这是一个本地命令，我们需要通过JSON-RPC调用本地的DPoS实例
	// 或者直接访问本地区块链数据库

	// 目前返回模拟数据，但结构更接近真实数据
	rewardsInfo := map[string]interface{}{
		"validatorAddress": validatorAddress,
		"epochNumber":      epochNumber,
		"blockRewards": map[string]interface{}{
			"totalBlocks":      0,
			"rewardPerBlock":   "0",
			"totalBlockReward": "0",
		},
		"votingRewards": map[string]interface{}{
			"totalVotes":      0,
			"rewardPerVote":   "0",
			"totalVoteReward": "0",
		},
		"delegationRewards": map[string]interface{}{
			"totalStake":       "0",
			"rewardPerStake":   "0",
			"totalStakeReward": "0",
		},
		"totalRewards": "0",
		"status":       "framework_ready",
		"message":      "Command framework ready, need to connect to local blockchain instance",
		"note":         "Use JSON-RPC call to dpos_getValidatorRewardsInfo for real data",
	}

	// 建议使用JSON-RPC调用获取真实数据：
	// curl -X POST http://localhost:8545 \
	//   -H "Content-Type: application/json" \
	//   -d '{"jsonrpc":"2.0","method":"dpos_getValidatorRewardsInfo","params":["' + validatorAddress + '",' + strconv.FormatUint(epochNumber, 10) + '],"id":1}'

	return rewardsInfo, nil
}

// isValidAddress 简单的地址格式验证
func isValidAddress(address string) bool {
	// 检查是否以0x开头
	if !strings.HasPrefix(address, "0x") {
		return false
	}

	// 检查长度（0x + 40个十六进制字符）
	if len(address) != 42 {
		return false
	}

	// 检查是否都是有效的十六进制字符
	hexPart := address[2:]
	for _, char := range hexPart {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f') ||
			(char >= 'A' && char <= 'F')) {
			return false
		}
	}

	return true
}
