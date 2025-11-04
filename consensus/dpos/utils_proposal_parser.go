package dpos

import (
	"encoding/json"
	"fmt"
)

// ParseProposalInput 解析标准交易 input 中的提案载荷，返回类别：create / vote / execute
func ParseProposalInput(input []byte) (string, error) {
	if len(input) == 0 {
		return "", fmt.Errorf("empty input")
	}
	var generic map[string]any
	if err := json.Unmarshal(input, &generic); err != nil {
		return "", err
	}
	// 判断 create：包含 proposalType（parameter / validator_recovery）
	if v, ok := generic["proposalType"].(string); ok && v != "" {
		return "create", nil
	}
	// 判断 vote：包含 support 且包含 proposalId
	if _, ok := generic["support"]; ok {
		if id, ok2 := generic["proposalId"].(string); ok2 && id != "" {
			return "vote", nil
		}
	}
	// 判断 execute：仅包含 proposalId
	if id, ok := generic["proposalId"].(string); ok && id != "" {
		return "execute", nil
	}
	return "", fmt.Errorf("not a proposal payload")
}




