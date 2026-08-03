package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/types"
)

// UnsignedTxDraft is a shared EIP-155 draft for client-side signing → eth_sendRawTransaction.
type UnsignedTxDraft struct {
	Success    bool    `json:"success"`
	Error      string  `json:"error,omitempty"`
	Message    string  `json:"message,omitempty"`
	ChainID    string  `json:"chainId,omitempty"`
	From       string  `json:"from,omitempty"`
	Nonce      string  `json:"nonce,omitempty"`
	GasPrice   string  `json:"gasPrice,omitempty"`
	Gas        string  `json:"gas,omitempty"`
	GasLimit   string  `json:"gasLimit,omitempty"`
	To         *string `json:"to"`
	Value      string  `json:"value,omitempty"`
	Data       string  `json:"data,omitempty"`
	Type       uint64  `json:"type"`
	DomainHash string  `json:"domainHash,omitempty"` // governance: sign this first (65-byte ECDSA)
	CreatedAt  uint64  `json:"createdAt,omitempty"`  // must match domain + JSON payload
	DepositWei string  `json:"depositWei,omitempty"` // register: tx value
	SignSteps  string  `json:"signSteps,omitempty"`
}

func (d *DPOS) draftFromTx(from types.Address, tx *types.Transaction, message string) *UnsignedTxDraft {
	var toStr *string
	if tx.To != nil {
		s := tx.To.String()
		toStr = &s
	}
	gasHex := toHexUint64(tx.Gas)
	return &UnsignedTxDraft{
		Success:  true,
		Message:  message,
		ChainID:  toHexUint64(d.chainID),
		From:     from.String(),
		Nonce:    toHexUint64(tx.Nonce),
		GasPrice: toHexBig(tx.GasPrice),
		Gas:      gasHex,
		GasLimit: gasHex,
		To:       toStr,
		Value:    toHexBig(tx.Value),
		Data:     toHexBytes(tx.Input),
		Type:     uint64(tx.Type),
	}
}

func (d *DPOS) lookupAccountNonce(addr types.Address) uint64 {
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		return nonceStore.GetNonce(addr)
	}
	if accountStore, ok := d.store.(interface {
		GetAccount(root types.Hash, addr types.Address) (*Account, error)
	}); ok {
		if account, err := accountStore.GetAccount(types.Hash{}, addr); err == nil && account != nil {
			return account.Nonce
		}
	}
	return 0
}

func (d *DPOS) lookupMinGasPrice() *big.Int {
	var gasPrice *big.Int
	if gasStore, ok := d.store.(interface {
		GetBaseFee() uint64
	}); ok {
		gasPrice = new(big.Int).SetUint64(gasStore.GetBaseFee())
	} else {
		gasPrice = big.NewInt(1_000_000_000)
	}
	minGasPrice := big.NewInt(1_000_000_000)
	if gasPrice.Cmp(minGasPrice) < 0 {
		gasPrice = minGasPrice
	}
	return gasPrice
}

func (d *DPOS) lookupProposalGasPrice() *big.Int {
	var baseFee uint64
	if gasStore, ok := d.store.(interface{ GetBaseFee() uint64 }); ok {
		baseFee = gasStore.GetBaseFee()
	}
	var priceLimit uint64
	if txpoolStore, ok := d.store.(interface{ GetTxPool() interface{} }); ok {
		if tp := txpoolStore.GetTxPool(); tp != nil {
			if dbg, ok := tp.(interface{ DebugInfo() map[string]interface{} }); ok {
				if info := dbg.DebugInfo(); info != nil {
					if pl, ok := info["priceLimit"].(uint64); ok {
						priceLimit = pl
					}
				}
			}
		}
	}
	maxBase := baseFee
	if priceLimit > maxBase {
		maxBase = priceLimit
	}
	if maxBase == 0 {
		maxBase = 1_000_000_000
	}
	return new(big.Int).SetUint64(maxBase + (maxBase / 10))
}

func (d *DPOS) buildUnsignedLegacyTx(from types.Address, to *types.Address, value *big.Int, input []byte, gas uint64, gasPrice *big.Int) *types.Transaction {
	if value == nil {
		value = big.NewInt(0)
	}
	if gasPrice == nil {
		gasPrice = d.lookupMinGasPrice()
	}
	tx := &types.Transaction{
		Nonce:    d.lookupAccountNonce(from),
		GasPrice: gasPrice,
		Gas:      gas,
		To:       to,
		Value:    value,
		Input:    input,
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
		Hash:     types.Hash{},
		Type:     types.LegacyTx,
		ChainID:  new(big.Int).SetUint64(d.chainID),
	}
	tx.ComputeHash(0)
	return tx
}

// CreateUpdateCommissionTransaction builds unsigned DPOS+COM tx (no private key).
// Params: [validator, commissionRate] or {validator, commissionRate}.
func (d *DPOS) CreateUpdateCommissionTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	if d.chainCommissionRemoved() {
		return &UnsignedTxDraft{Success: false, Error: "commission has been removed at the configured activation epoch"}, nil
	}

	var validatorAddress string
	var commissionRate uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) < 2 {
			return &UnsignedTxDraft{Success: false, Error: "expected [validator, commissionRate]"}, nil
		}
		addr, ok := p[0].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "validator address must be a string"}, nil
		}
		validatorAddress = addr
		rate, err := parseCommissionRateParam(p[1])
		if err != nil {
			return &UnsignedTxDraft{Success: false, Error: err.Error()}, nil
		}
		commissionRate = rate
	case map[string]interface{}:
		addr, ok := p["validator"].(string)
		if !ok {
			if addr, ok = p["validatorAddress"].(string); !ok {
				return &UnsignedTxDraft{Success: false, Error: "validator address is required"}, nil
			}
		}
		validatorAddress = addr
		rate, err := parseCommissionRateParam(p["commissionRate"])
		if err != nil {
			return &UnsignedTxDraft{Success: false, Error: err.Error()}, nil
		}
		commissionRate = rate
	default:
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("invalid parameter type: %T", params)}, nil
	}

	if commissionRate < 500 || commissionRate > 8000 {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("commission rate out of range [500, 8000], got %d", commissionRate)}, nil
	}

	validatorAddr := types.StringToAddress(validatorAddress)
	input := dpos.BuildCommissionUpdateCalldata(uint16(commissionRate))
	tx := d.buildUnsignedLegacyTx(validatorAddr, nil, big.NewInt(0), input, 100000, big.NewInt(1_000_000_000))
	if tx.Hash == (types.Hash{}) {
		return &UnsignedTxDraft{Success: false, Error: "transaction hash is zero after creation"}, nil
	}
	draft := d.draftFromTx(validatorAddr, tx, "Unsigned commission update; EIP-155 sign then eth_sendRawTransaction")
	draft.SignSteps = "1) EIP-155 sign this draft 2) eth_sendRawTransaction"
	return draft, nil
}

func parseCommissionRateParam(v interface{}) (uint64, error) {
	switch t := v.(type) {
	case float64:
		return uint64(t), nil
	case string:
		s := strings.TrimSpace(t)
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			parsed, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid commission rate: %w", err)
			}
			return parsed, nil
		}
		parsed, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid commission rate: %w", err)
		}
		return parsed, nil
	case json.Number:
		parsed, err := t.Int64()
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid commission rate")
		}
		return uint64(parsed), nil
	default:
		return 0, fmt.Errorf("commission rate must be a number")
	}
}

// CreateRegisterDelegateTransaction builds unsigned DPOS+REG deposit tx (no private key).
// Params: {registrant, name, website?, description?}
func (d *DPOS) CreateRegisterDelegateTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	var registrantStr, name, website, description string
	paramMap, ok := params.(map[string]interface{})
	if !ok {
		return &UnsignedTxDraft{Success: false, Error: "invalid parameters format: expected object"}, nil
	}
	registrantStr, _ = paramMap["registrant"].(string)
	name, _ = paramMap["name"].(string)
	website, _ = paramMap["website"].(string)
	description, _ = paramMap["description"].(string)
	if registrantStr == "" {
		return &UnsignedTxDraft{Success: false, Error: "registrant address is required"}, nil
	}
	if name == "" {
		return &UnsignedTxDraft{Success: false, Error: "name is required"}, nil
	}

	registrant := types.StringToAddress(registrantStr)
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return &UnsignedTxDraft{Success: false, Error: "DPoS engine not available"}, nil
	}
	inst, ok := dposEngine.(*dpos.DPoS)
	if !ok {
		return &UnsignedTxDraft{Success: false, Error: "DPoS engine type mismatch"}, nil
	}
	if inst.IsDelegateRegistered(registrant) {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("delegate %s already registered", registrant.String())}, nil
	}

	deposit := inst.GetDelegateDepositAmount()
	escrow := dpos.DelegateDepositEscrowAddr()
	input := dpos.BuildDelegateRegistrationCalldata(registrant, name, website, description)
	tx := d.buildUnsignedLegacyTx(registrant, &escrow, deposit, input, 100000, big.NewInt(1_000_000_000))
	draft := d.draftFromTx(registrant, tx, "Unsigned register delegate; EIP-155 sign then eth_sendRawTransaction")
	draft.DepositWei = deposit.String()
	draft.SignSteps = "1) EIP-155 sign this draft (value=deposit) 2) eth_sendRawTransaction"
	return draft, nil
}

// CreateWithdrawDelegateTransaction builds unsigned DPOS+CAN escrow refund tx (no private key).
// Params: {address|registrant} or [address]. On-chain CAN sets withdrawn + refunds deposit.
func (d *DPOS) CreateWithdrawDelegateTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	var addressStr string
	switch p := params.(type) {
	case map[string]interface{}:
		addressStr, _ = p["address"].(string)
		if addressStr == "" {
			addressStr, _ = p["registrant"].(string)
		}
	case []interface{}:
		if len(p) >= 1 {
			addressStr, _ = p[0].(string)
		}
	default:
		return &UnsignedTxDraft{Success: false, Error: "invalid parameters format"}, nil
	}
	if addressStr == "" {
		return &UnsignedTxDraft{Success: false, Error: "address is required"}, nil
	}

	address := types.StringToAddress(addressStr)
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return &UnsignedTxDraft{Success: false, Error: "DPoS engine not available"}, nil
	}
	if ig, ok := dposEngine.(interface {
		IsGenesisValidator(types.Address) bool
	}); ok && ig.IsGenesisValidator(address) {
		return &UnsignedTxDraft{Success: false, Error: "genesis validators cannot cancel via DPOS+CAN"}, nil
	}

	calldata := dpos.BuildDelegateCancelRegistrationCalldata(address)
	escrow := dpos.DelegateDepositEscrowAddr()
	tx := d.buildUnsignedLegacyTx(address, &escrow, big.NewInt(0), calldata, 300_000, d.lookupMinGasPrice())
	draft := d.draftFromTx(address, tx, "Unsigned withdraw/cancel (DPOS+CAN); EIP-155 sign then eth_sendRawTransaction")
	draft.SignSteps = "1) EIP-155 sign this draft 2) eth_sendRawTransaction (on-chain CAN withdraws + refunds escrow)"
	return draft, nil
}

// CreateParameterProposalTransaction builds governance create draft (domainHash + unsigned tx with empty signature).
// Params: {parameter, newValue, description?, proposer} — no private key.
func (d *DPOS) CreateParameterProposalTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	var proposerStr, parameter, description string
	var newValue interface{}

	switch p := params.(type) {
	case []interface{}:
		if len(p) < 4 {
			return &UnsignedTxDraft{Success: false, Error: "expected [parameter, newValue, description, proposer]"}, nil
		}
		var ok bool
		parameter, ok = p[0].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "parameter must be string"}, nil
		}
		newValue = p[1]
		description, _ = p[2].(string)
		proposerStr, ok = p[3].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "proposer must be string"}, nil
		}
	case map[string]interface{}:
		var ok bool
		proposerStr, ok = p["proposer"].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "proposer is required"}, nil
		}
		parameter, ok = p["parameter"].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "parameter is required"}, nil
		}
		newValue, ok = p["newValue"]
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "newValue is required"}, nil
		}
		description, _ = p["description"].(string)
	default:
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("invalid parameters format: %T", params)}, nil
	}

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return &UnsignedTxDraft{Success: false, Error: err.Error()}, nil
	}
	if !gov.IsParameterVotable(parameter) {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("parameter %s is not votable", parameter)}, nil
	}

	proposer := types.StringToAddress(proposerStr)
	createdAt := uint64(time.Now().Unix())
	tempProposal := &dpos.ParameterProposal{
		ID:           "draft",
		ProposalType: "parameter",
		Parameter:    parameter,
		Proposer:     proposer,
		NewValue:     newValue,
		Description:  description,
		CreatedAt:    createdAt,
	}
	domainHash := dpos.BuildProposalDomainHash(d.chainID, tempProposal)

	txData := dpos.ProposalCreateTxData{
		ProposalType:      "parameter",
		Parameter:         parameter,
		NewValue:          newValue,
		Description:       description,
		ProposerSignature: []byte{}, // client fills after domain sign
		CreatedAt:         createdAt,
	}
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("marshal tx data: %v", err)}, nil
	}
	to := proposer
	tx := d.buildUnsignedLegacyTx(proposer, &to, big.NewInt(0), txDataBytes, 200000, d.lookupProposalGasPrice())
	draft := d.draftFromTx(proposer, tx, "Unsigned parameter proposal draft")
	draft.DomainHash = toHexBytes(domainHash)
	draft.CreatedAt = createdAt
	draft.SignSteps = "1) ECDSA-sign domainHash (65 bytes, same as crypto.Sign) 2) set proposerSignature to base64 of sig in JSON Input and re-encode data 3) EIP-155 sign 4) eth_sendRawTransaction"
	return draft, nil
}

// CreateRecoveryProposalTransaction builds recovery proposal draft (domainHash + unsigned tx).
// Params: {validatorAddress, recoveryReason, description?, proposer}
func (d *DPOS) CreateRecoveryProposalTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	var proposerStr, validatorAddrStr, recoveryReason, description string

	switch p := params.(type) {
	case []interface{}:
		if len(p) < 4 {
			return &UnsignedTxDraft{Success: false, Error: "expected [validatorAddress, recoveryReason, description, proposer]"}, nil
		}
		validatorAddrStr, _ = p[0].(string)
		recoveryReason, _ = p[1].(string)
		description, _ = p[2].(string)
		proposerStr, _ = p[3].(string)
	case map[string]interface{}:
		proposerStr, _ = p["proposer"].(string)
		validatorAddrStr, _ = p["validatorAddress"].(string)
		recoveryReason, _ = p["recoveryReason"].(string)
		description, _ = p["description"].(string)
	default:
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("invalid parameters format: %T", params)}, nil
	}
	if proposerStr == "" || validatorAddrStr == "" {
		return &UnsignedTxDraft{Success: false, Error: "proposer and validatorAddress are required"}, nil
	}

	proposer := types.StringToAddress(proposerStr)
	validatorAddr := types.StringToAddress(validatorAddrStr)
	createdAt := uint64(time.Now().Unix())
	tempProposal := &dpos.ParameterProposal{
		ID:               "draft",
		ProposalType:     "validator_recovery",
		Proposer:         proposer,
		ValidatorAddress: validatorAddr,
		RecoveryReason:   recoveryReason,
		Description:      description,
		CreatedAt:        createdAt,
	}
	domainHash := dpos.BuildProposalDomainHash(d.chainID, tempProposal)

	txData := dpos.ProposalCreateTxData{
		ProposalType:      "validator_recovery",
		Parameter:         validatorAddr.String(),
		Description:       description,
		RecoveryReason:    recoveryReason,
		ProposerSignature: []byte{},
		CreatedAt:         createdAt,
	}
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("marshal tx data: %v", err)}, nil
	}
	// Need validator address in tx data for processing — check existing CreateRecoveryProposal txData
	to := proposer
	tx := d.buildUnsignedLegacyTx(proposer, &to, big.NewInt(0), txDataBytes, 200000, d.lookupProposalGasPrice())
	draft := d.draftFromTx(proposer, tx, "Unsigned recovery proposal draft")
	draft.DomainHash = toHexBytes(domainHash)
	draft.CreatedAt = createdAt
	draft.SignSteps = "1) ECDSA-sign domainHash 2) set proposerSignature (base64) in JSON Input 3) EIP-155 sign 4) eth_sendRawTransaction"
	return draft, nil
}

// CreateVoteOnParameterProposalTransaction builds governance vote draft.
// Params: {proposalId, voter, support} or [proposalId, voter, support]
func (d *DPOS) CreateVoteOnParameterProposalTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	var proposalID, voterStr string
	var support bool

	switch p := params.(type) {
	case []interface{}:
		if len(p) < 3 {
			return &UnsignedTxDraft{Success: false, Error: "expected [proposalId, voter, support]"}, nil
		}
		var ok bool
		proposalID, ok = p[0].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "proposalId must be string"}, nil
		}
		voterStr, ok = p[1].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "voter must be string"}, nil
		}
		support, ok = p[2].(bool)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "support must be bool"}, nil
		}
	case map[string]interface{}:
		var ok bool
		proposalID, ok = p["proposalId"].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "proposalId is required"}, nil
		}
		voterStr, ok = p["voter"].(string)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "voter is required"}, nil
		}
		support, ok = p["support"].(bool)
		if !ok {
			return &UnsignedTxDraft{Success: false, Error: "support is required"}, nil
		}
	default:
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("invalid parameters format: %T", params)}, nil
	}

	voter := types.StringToAddress(voterStr)
	tempVote := &dpos.ParameterVote{
		Voter:      voter,
		ProposalID: proposalID,
		Support:    support,
	}
	domainHash := dpos.BuildVoteDomainHash(d.chainID, tempVote)

	txData := dpos.ProposalVoteTxData{
		ProposalID:    proposalID,
		Support:       support,
		VoteSignature: []byte{},
	}
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return &UnsignedTxDraft{Success: false, Error: fmt.Sprintf("marshal tx data: %v", err)}, nil
	}
	to := voter
	tx := d.buildUnsignedLegacyTx(voter, &to, big.NewInt(0), txDataBytes, 150000, d.lookupProposalGasPrice())
	draft := d.draftFromTx(voter, tx, "Unsigned governance vote draft")
	draft.DomainHash = toHexBytes(domainHash)
	draft.SignSteps = "1) ECDSA-sign domainHash 2) set voteSignature (base64) in JSON Input 3) EIP-155 sign 4) eth_sendRawTransaction"
	return draft, nil
}
