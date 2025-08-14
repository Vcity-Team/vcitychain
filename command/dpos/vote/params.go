package vote

import "fmt"

// VoteResult represents the result of a voting operation
type VoteResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Voter       string `json:"voter"`
	Candidate   string `json:"candidate"`
	Amount      string `json:"amount"`
	TxHash      string `json:"txHash"`
	BlockNumber uint64 `json:"blockNumber"`
}

// GetOutput returns the formatted output string for the command
func (v *VoteResult) GetOutput() string {
	if v.Success {
		return fmt.Sprintf(`[VOTE RESULT]
Status: %s
Voter: %s
Candidate: %s
Amount: %s
Transaction Hash: %s
Block Number: %d`, v.Message, v.Voter, v.Candidate, v.Amount, v.TxHash, v.BlockNumber)
	} else {
		return fmt.Sprintf(`[VOTE RESULT]
Status: %s
Voter: %s
Candidate: %s
Amount: %s
Note: %s`, "Failed", v.Voter, v.Candidate, v.Amount, v.Message)
	}
}
