package delegate

import (
	"bytes"

	"encoding/json"

	"fmt"

	"io"

	"net/http"

	"strings"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

var legacyTestParams = &registerLegacyTestParams{}

type registerLegacyTestParams struct {
	jsonRPC string

	chainID uint64

	genesisPath string

	address string

	name string

	website string

	description string

	privateKey string
}

// GetDelegateTestCommand registers with To = CreateAddress(sender,nonce) (legacy deposit contract).

func GetDelegateTestCommand() *cobra.Command {

	cmd := &cobra.Command{

		Use: "delegateTest",

		Aliases: []string{"legacy-register-test"},

		Short: "Legacy delegate registration: To = deposit contract address (local test)",

		Long: `Legacy registration: tx.To is the deposit contract address (crypto.CreateAddress(sender, nonce)); returns contractAddress for migration params.

Production path (deposit to escrow): dpos delegate register`,

		PreRunE: runPreRunLegacyTest,

		RunE: runLegacyTestCommand,

		SilenceUsage: true,
	}

	helper.RegisterJSONRPCFlag(cmd)

	setLegacyTestFlags(cmd)

	return cmd

}

func setLegacyTestFlags(cmd *cobra.Command) {

	cmd.Flags().StringVar(&legacyTestParams.address, "address", "", "registrant / candidate address (required)")

	cmd.Flags().StringVar(&legacyTestParams.name, "name", "", "delegate name (required)")

	cmd.Flags().StringVar(&legacyTestParams.website, "website", "", "website (optional)")

	cmd.Flags().StringVar(&legacyTestParams.description, "description", "", "description (optional)")

	cmd.Flags().StringVar(&legacyTestParams.privateKey, "private-key", "", "private key hex 64 chars (required)")

	cmd.Flags().Uint64Var(&legacyTestParams.chainID, "chain-id", 0, "chain ID for signing (required)")

	cmd.Flags().StringVar(&legacyTestParams.genesisPath, "genesis", "./genesis.json", "unused; chain-id is required")

	_ = cmd.MarkFlagRequired("address")

	_ = cmd.MarkFlagRequired("name")

	_ = cmd.MarkFlagRequired("private-key")

}

func runPreRunLegacyTest(cmd *cobra.Command, _ []string) error {

	jsonRPC, err := cmd.Flags().GetString("jsonrpc")

	if err != nil {

		return err

	}

	legacyTestParams.jsonRPC = jsonRPC

	chainID, err := cmd.Flags().GetUint64("chain-id")

	if err != nil {

		return err

	}

	if chainID == 0 {

		return fmt.Errorf("missing --chain-id")

	}

	legacyTestParams.chainID = chainID

	return validateLegacyTestParams()

}

func validateLegacyTestParams() error {

	if legacyTestParams.address == "" || legacyTestParams.name == "" || legacyTestParams.privateKey == "" {

		return fmt.Errorf("address, name, and private-key are required")

	}

	if !isValidHexAddress(legacyTestParams.address) {

		return fmt.Errorf("invalid address: %s", legacyTestParams.address)

	}

	pk := strings.TrimPrefix(strings.TrimPrefix(legacyTestParams.privateKey, "0x"), "0X")

	if !isValidPrivateKey(pk) {

		return fmt.Errorf("invalid private-key")

	}

	legacyTestParams.privateKey = pk

	return nil

}

func runLegacyTestCommand(cmd *cobra.Command, _ []string) error {

	outputter := command.InitializeOutputter(cmd)

	defer outputter.WriteOutput()

	res, err := callRegisterDelegateLegacyTest(legacyTestParams)

	if err != nil {

		return err

	}

	outputter.SetCommandResult(res)

	return nil

}

func callRegisterDelegateLegacyTest(p *registerLegacyTestParams) (*RegisterDelegateResult, error) {

	body := map[string]interface{}{

		"jsonrpc": "2.0",

		"method": "dpos_registerDelegateLegacyTest",

		"params": map[string]interface{}{

			"registrant": p.address,

			"name": p.name,

			"website": p.website,

			"description": p.description,

			"privateKey": p.privateKey,

			"chainID": p.chainID,
		},

		"id": 1,
	}

	jsonData, err := json.Marshal(body)

	if err != nil {

		return nil, err

	}

	resp, err := http.Post(p.jsonRPC, "application/json", bytes.NewBuffer(jsonData))

	if err != nil {

		return nil, err

	}

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)

	if err != nil {

		return nil, err

	}

	var response struct {
		Result *RegisterDelegateResult `json:"result"`

		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(raw, &response); err != nil {

		return nil, fmt.Errorf("parse response: %w", err)

	}

	if response.Error != nil {

		return nil, fmt.Errorf("RPC error: %s", response.Error.Message)

	}

	return response.Result, nil

}