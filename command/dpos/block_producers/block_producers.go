package block_producers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
)

var (
	params = &blockProducersParams{}
)

type blockProducersParams struct {
	jsonRPC     string
	dataDir     string
	chainID     uint64
	mode        string // "blockRange" or "epoch"
	startBlock  uint64
	endBlock    uint64
	epochNumber uint64
}

// BlockProducersResult represents the result of the block producers command
type BlockProducersResult struct {
	Success           bool                     `json:"success"`
	Mode              string                   `json:"mode"`
	StartBlock        uint64                   `json:"startBlock,omitempty"`
	EndBlock          uint64                   `json:"endBlock,omitempty"`
	EpochNumber       uint64                   `json:"epochNumber,omitempty"`
	TotalBlocks       uint64                   `json:"totalBlocks"`
	ActualBlocksFound int                      `json:"actualBlocksFound"`
	Producers         []map[string]interface{} `json:"producers"`
	Error             string                   `json:"error,omitempty"`
}

// GetOutput returns the string representation of the result
func (r *BlockProducersResult) GetOutput() string {
	if !r.Success {
		return fmt.Sprintf("Error: %s", r.Error)
	}

	var output string
	output += fmt.Sprintf("Block Producers Information\n")
	output += fmt.Sprintf("=====================================\n\n")

	// 显示查询模式
	if r.Mode == "epoch" {
		output += fmt.Sprintf("Query Mode: Epoch-based\n")
		output += fmt.Sprintf("Epoch Number: %d\n", r.EpochNumber)
		output += fmt.Sprintf("Block Range: [%d, %d]\n\n", r.StartBlock, r.EndBlock)
	} else {
		output += fmt.Sprintf("Query Mode: Block Range\n")
		output += fmt.Sprintf("Block Range: [%d, %d]\n\n", r.StartBlock, r.EndBlock)
	}

	output += fmt.Sprintf("Total Blocks in Range: %d\n", r.TotalBlocks)
	output += fmt.Sprintf("Actual Blocks Found: %d\n\n", r.ActualBlocksFound)

	// 显示出块者统计
	output += fmt.Sprintf("Block Producers Summary:\n")
	output += fmt.Sprintf("------------------------------------\n")
	output += fmt.Sprintf("%-42s %-8s\n", "Address", "Blocks")
	output += fmt.Sprintf("------------------------------------\n")

	for _, producer := range r.Producers {
		address := producer["address"].(string)
		count := producer["count"].(int)
		output += fmt.Sprintf("%-42s %-8d\n", address, count)
	}

	output += fmt.Sprintf("\n")

	// 显示详细出块列表（仅当出块者数量<=5时显示）
	if len(r.Producers) > 0 && len(r.Producers) <= 5 {
		output += fmt.Sprintf("\nDetailed Block List:\n")
		output += fmt.Sprintf("------------------------------------\n")
		for _, producer := range r.Producers {
			address := producer["address"].(string)
			count := producer["count"].(int)
			blocks := producer["blocks"].([]interface{})

			output += fmt.Sprintf("Producer: %s (produced %d blocks)\n", address, count)

			// 格式化区块列表（每行最多10个）
			blocksStr := ""
			for i, blockNum := range blocks {
				if i > 0 && i%10 == 0 {
					blocksStr += "\n  "
				}
				blocksStr += fmt.Sprintf("%.0f, ", blockNum)
			}
			if blocksStr != "" {
				output += fmt.Sprintf("  Blocks: %s\n", blocksStr[:len(blocksStr)-2])
			}
			output += fmt.Sprintf("\n")
		}
	}

	return output
}

// GetCommand returns the block-producers command
func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block-producers",
		Short: "Query block producers within a block range or epoch",
		Long: `Query block producers within a specific block range or epoch.

The command supports two modes:
1. Block Range: Specify start and end block numbers
   Example: ./main.exe dpos block-producers --chain-id 20230826 --start-block 1000 --end-block 2000

2. Epoch: Specify epoch number (0 for IBFT, >=1 for DPoS)
   Example: ./main.exe dpos block-producers --chain-id 20230826 --epoch 1`,
		Args: cobra.NoArgs,
		RunE: runCommand,
	}

	// Flags
	flags := cmd.Flags()
	flags.StringVar(&params.jsonRPC, "jsonrpc", "http://0.0.0.0:8545", "the JSON-RPC interface")
	flags.StringVar(&params.dataDir, "data-dir", "", "the directory for the Polygon Edge data")
	flags.Uint64Var(&params.chainID, "chain-id", 0, "the chain ID to interact with")

	// Query mode flags
	flags.Uint64Var(&params.startBlock, "start-block", 0, "start block number (for block range mode)")
	flags.Uint64Var(&params.endBlock, "end-block", 0, "end block number (for block range mode)")
	flags.Uint64Var(&params.epochNumber, "epoch", 0, "epoch number (for epoch mode)")

	return cmd
}

func runCommand(cmd *cobra.Command, args []string) error {
	// Validate parameters
	if params.chainID == 0 {
		return fmt.Errorf("chain-id is required")
	}

	// Determine mode
	if params.epochNumber > 0 {
		params.mode = "epoch"
	} else if params.startBlock > 0 || params.endBlock > 0 {
		params.mode = "blockRange"
		if params.startBlock == 0 || params.endBlock == 0 {
			return fmt.Errorf("both start-block and end-block are required for block range mode")
		}
		if params.endBlock < params.startBlock {
			return fmt.Errorf("end-block must be greater than or equal to start-block")
		}
	} else {
		return fmt.Errorf("either epoch or (start-block and end-block) must be specified")
	}

	// Prepare RPC request
	var rpcParams []interface{}
	if params.mode == "epoch" {
		rpcParams = []interface{}{params.epochNumber}
	} else {
		rpcParams = []interface{}{params.startBlock, params.endBlock}
	}

	result, err := queryBlockProducers(params.jsonRPC, rpcParams)
	if err != nil {
		return fmt.Errorf("failed to query block producers: %w", err)
	}

	// Output result
	fmt.Print(result.GetOutput())

	return nil
}

func queryBlockProducers(jsonRPC string, rpcParams []interface{}) (command.CommandResult, error) {
	requestBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getBlockProducers",
		"params":  rpcParams,
		"id":      1,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := http.Post(jsonRPC, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResponse struct {
		Result map[string]interface{} `json:"result"`
		Error  map[string]interface{} `json:"error"`
	}

	if err := json.Unmarshal(body, &rpcResponse); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if rpcResponse.Error != nil {
		return &BlockProducersResult{
			Success: false,
			Error:   fmt.Sprintf("RPC error: %v", rpcResponse.Error),
		}, nil
	}

	resultData := rpcResponse.Result

	// Parse result
	result := &BlockProducersResult{
		Success: true,
	}

	if mode, ok := resultData["mode"].(string); ok {
		result.Mode = mode
	}

	if startBlock, ok := resultData["startBlock"].(float64); ok {
		result.StartBlock = uint64(startBlock)
	}

	if endBlock, ok := resultData["endBlock"].(float64); ok {
		result.EndBlock = uint64(endBlock)
	}

	if epochNumber, ok := resultData["epoch"].(float64); ok {
		result.EpochNumber = uint64(epochNumber)
	}

	if totalBlocks, ok := resultData["totalBlocks"].(float64); ok {
		result.TotalBlocks = uint64(totalBlocks)
	}

	if actualBlocksFound, ok := resultData["actualBlocksFound"].(float64); ok {
		result.ActualBlocksFound = int(actualBlocksFound)
	}

	if producers, ok := resultData["producers"].([]interface{}); ok {
		result.Producers = make([]map[string]interface{}, 0)
		for _, p := range producers {
			if prodMap, ok := p.(map[string]interface{}); ok {
				result.Producers = append(result.Producers, prodMap)
			}
		}
	}

	return result, nil
}

func (r *BlockProducersResult) DisplayOutput() {
	fmt.Print(r.GetOutput())
}
