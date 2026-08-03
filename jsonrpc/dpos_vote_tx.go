package jsonrpc

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/types"
)

// CreateVoteTransactionResponse is the unsigned vote tx draft for client-side EIP-155 signing.
type CreateVoteTransactionResponse struct {
	Success  bool    `json:"success"`
	Error    string  `json:"error,omitempty"`
	Message  string  `json:"message,omitempty"`
	ChainID  string  `json:"chainId,omitempty"`  // hex, e.g. "0x..."
	From     string  `json:"from,omitempty"`
	Nonce    string  `json:"nonce,omitempty"`    // hex
	GasPrice string  `json:"gasPrice,omitempty"` // hex
	Gas      string  `json:"gas,omitempty"`      // hex (gasLimit)
	GasLimit string  `json:"gasLimit,omitempty"` // alias of gas for ethers.js
	To       *string `json:"to"`                 // null for DPoS vote txs
	Value    string  `json:"value,omitempty"`    // hex
	Data     string  `json:"data,omitempty"`     // hex calldata
	Type     uint64  `json:"type"`               // 0 = legacy
	IsUnvote bool    `json:"isUnvote,omitempty"`
}

type voteTxBuildResult struct {
	tx       *types.Transaction
	voter    types.Address
	isUnvote bool
}

func toHexUint64(v uint64) string {
	return fmt.Sprintf("0x%x", v)
}

func toHexBig(v *big.Int) string {
	if v == nil {
		return "0x0"
	}
	return "0x" + v.Text(16)
}

func toHexBytes(b []byte) string {
	if len(b) == 0 {
		return "0x"
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 2+len(b)*2)
	out[0], out[1] = '0', 'x'
	for i, v := range b {
		out[2+i*2] = hexdigits[v>>4]
		out[2+i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

// parseVoteRequest parses dpos_vote / dpos_createVoteTransaction params.
// When requirePrivateKey is false, privateKey may be omitted (create-draft path).
func (d *DPOS) parseVoteRequest(params interface{}, requirePrivateKey bool) (VoteRequest, *VoteResponse) {
	var req VoteRequest
	switch p := params.(type) {
	case []interface{}:
		minLen := 3
		if requirePrivateKey {
			minLen = 4
		}
		if len(p) < minLen {
			if requirePrivateKey {
				return req, &VoteResponse{
					Success: false,
					Error:   fmt.Sprintf("expected 4 parameters [voter, candidate, amount, privateKey], got %d", len(p)),
				}
			}
			return req, &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 3 parameters [voter, candidate, amount], got %d", len(p)),
			}
		}
		voter, ok := p[0].(string)
		if !ok {
			return req, &VoteResponse{Success: false, Error: "first parameter must be a string address (voter)"}
		}
		candidate, ok := p[1].(string)
		if !ok {
			return req, &VoteResponse{Success: false, Error: "second parameter must be a string address (candidate)"}
		}
		amount, ok := p[2].(string)
		if !ok {
			return req, &VoteResponse{Success: false, Error: "third parameter must be a string amount"}
		}
		req.Voter = voter
		req.Candidate = candidate
		req.Amount = amount
		if len(p) >= 4 {
			if privateKey, ok := p[3].(string); ok {
				req.PrivateKey = privateKey
			} else if requirePrivateKey {
				return req, &VoteResponse{Success: false, Error: "fourth parameter must be a string private key"}
			}
		}
	case map[string]interface{}:
		if voter, ok := p["voter"].(string); ok {
			req.Voter = voter
		}
		if candidate, ok := p["candidate"].(string); ok {
			req.Candidate = candidate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
		if privateKey, ok := p["privateKey"].(string); ok {
			req.PrivateKey = privateKey
		}
	case *VoteRequest:
		if p != nil {
			req = *p
		}
	default:
		return req, &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or VoteRequest", params),
		}
	}

	if req.Voter == "" {
		return req, &VoteResponse{Success: false, Error: "voter address is required"}
	}
	if req.Candidate == "" {
		return req, &VoteResponse{Success: false, Error: "candidate address is required"}
	}
	if req.Amount == "" {
		return req, &VoteResponse{Success: false, Error: "amount is required"}
	}
	if requirePrivateKey && req.PrivateKey == "" {
		return req, &VoteResponse{Success: false, Error: "private key is required"}
	}
	return req, nil
}

// buildUnsignedVoteTransaction validates the vote request and builds an unsigned legacy tx.
// On failure returns (nil, VoteResponse with Error). On success VoteResponse is nil.
func (d *DPOS) buildUnsignedVoteTransaction(req VoteRequest) (*voteTxBuildResult, *VoteResponse) {
	voterAddr := types.StringToAddress(req.Voter)
	candidateAddr := types.StringToAddress(req.Candidate)

	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return nil, &VoteResponse{Success: false, Error: "invalid amount format"}
	}
	if amountInt.Sign() == 0 {
		return nil, &VoteResponse{Success: false, Error: "amount cannot be zero"}
	}
	isUnvote := amountInt.Cmp(big.NewInt(-1)) == 0

	var balance *big.Int
	var err error

	if balanceStore, ok := d.store.(interface {
		GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
	}); ok {
		var latestRoot types.Hash
		var foundValidRoot bool

		if headerStore, ok := d.store.(interface {
			Header() *types.Header
		}); ok {
			latestHeader := headerStore.Header()
			if latestHeader != nil {
				latestRoot = latestHeader.StateRoot
				foundValidRoot = true
			}
		}

		if !foundValidRoot {
			if latestStore, ok := d.store.(interface {
				GetLatestStateRoot() types.Hash
			}); ok {
				latestRoot = latestStore.GetLatestStateRoot()
				foundValidRoot = true
			}
		}

		if !foundValidRoot {
			if headerStore, ok := d.store.(interface {
				GetLatestHeader() *types.Header
			}); ok {
				latestHeader := headerStore.GetLatestHeader()
				if latestHeader != nil {
					latestRoot = latestHeader.StateRoot
					foundValidRoot = true
				}
			}
		}

		if !foundValidRoot {
			if blockStore, ok := d.store.(interface {
				GetLatestBlock() *types.Block
			}); ok {
				latestBlock := blockStore.GetLatestBlock()
				if latestBlock != nil {
					latestRoot = latestBlock.Header.StateRoot
					foundValidRoot = true
				}
			}
		}

		if !foundValidRoot {
			if headerStore, ok := d.store.(interface {
				Header() *types.Header
				GetHeaderByNumber(uint64) (*types.Header, bool)
			}); ok {
				latestHeader := headerStore.Header()
				if latestHeader != nil && latestHeader.Number > 0 {
					if prevHeader, ok := headerStore.GetHeaderByNumber(latestHeader.Number - 1); ok {
						latestRoot = prevHeader.StateRoot
						foundValidRoot = true
					}
				}
			}
		}

		if foundValidRoot && latestRoot != (types.Hash{}) {
			balance, err = balanceStore.GetBalance(latestRoot, voterAddr)
		}
	}

	if balance == nil || err != nil {
		if hub, ok := d.store.(interface {
			GetConsensus() interface{}
		}); ok {
			consensusEngine := hub.GetConsensus()
			if balanceEngine, ok := consensusEngine.(interface {
				GetAccountBalance(addr types.Address) (*big.Int, error)
			}); ok {
				balance, err = balanceEngine.GetAccountBalance(voterAddr)
			}
		}
	}

	if balance == nil || err != nil {
		if balanceStore, ok := d.store.(interface {
			GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
		}); ok {
			zeroHash := types.Hash{}
			balance, err = balanceStore.GetBalance(zeroHash, voterAddr)
		}
	}

	if balance == nil {
		d.logger.Error("Failed to retrieve voter balance - cannot proceed with vote")
		return nil, &VoteResponse{Success: false, Error: "unable to verify voter balance - cannot proceed with vote"}
	}

	if !isUnvote && balance.Cmp(amountInt) < 0 {
		d.logger.Error("Insufficient balance", "balance", balance.String(), "required", amountInt.String())
		return nil, &VoteResponse{Success: false, Error: "insufficient balance"}
	}

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		d.logger.Error("❌ DPoS引擎不可用，无法验证受托人资格")
		return nil, &VoteResponse{Success: false, Error: "DPoS engine not available for delegate validation"}
	}

	if !isUnvote {
		if isRegistered, ok := dposEngine.(interface {
			IsDelegateRegistered(address types.Address) bool
		}); ok {
			isGenesis, okGenesis := dposEngine.(interface {
				IsGenesisValidator(address types.Address) bool
			})
			if okGenesis && isGenesis.IsGenesisValidator(candidateAddr) {
				// genesis validators may receive votes without registration
			} else if !isRegistered.IsDelegateRegistered(candidateAddr) {
				d.logger.Warn("❌ 受托人未注册，投票被拒绝",
					"candidate", candidateAddr.String(),
					"voter", voterAddr.String(),
					"amount", amountInt.String())
				return nil, &VoteResponse{
					Success: false,
					Error:   fmt.Sprintf("delegate %s is not registered", candidateAddr.String()),
				}
			}
		} else {
			d.logger.Warn("⚠️ DPoS引擎不支持受托人注册检查，跳过验证")
		}

		if isCandidate, ok := dposEngine.(interface {
			IsDelegateCandidate(address types.Address) bool
		}); ok {
			if !isCandidate.IsDelegateCandidate(candidateAddr) {
				d.logger.Warn("❌ 受托人不是候选人状态，投票被拒绝",
					"candidate", candidateAddr.String(),
					"voter", voterAddr.String(),
					"amount", amountInt.String())
				return nil, &VoteResponse{
					Success: false,
					Error:   fmt.Sprintf("delegate %s is not a candidate", candidateAddr.String()),
				}
			}
		} else {
			d.logger.Warn("⚠️ DPoS引擎不支持受托人候选人检查，跳过验证")
		}
	}

	voteMessage := &VoteMessage{
		Voter:     voterAddr,
		Delegate:  candidateAddr,
		Amount:    amountInt,
		Round:     0,
		Timestamp: uint64(time.Now().Unix()),
	}

	if dposEngineInstance, ok := dposEngine.(*dpos.DPoS); ok {
		if err := dposEngineInstance.ValidateVoteOnly(voteMessage.Voter, voteMessage.Delegate, voteMessage.Amount); err != nil {
			d.logger.Error("❌ 投票预验证失败", "error", err)
			return nil, &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("vote validation failed: %v", err),
			}
		}
	} else {
		d.logger.Warn("⚠️ DPoS引擎类型不匹配，跳过预验证")
	}

	if voterAddr == (types.Address{}) {
		d.logger.Error("🚨 CRITICAL: voter address is zero address")
		return nil, &VoteResponse{Success: false, Error: "voter address is zero address"}
	}
	if candidateAddr == (types.Address{}) {
		d.logger.Error("🚨 CRITICAL: candidate address is zero address")
		return nil, &VoteResponse{Success: false, Error: "candidate address is zero address"}
	}

	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(voterAddr)
	} else if accountStore, ok := d.store.(interface {
		GetAccount(root types.Hash, addr types.Address) (*Account, error)
	}); ok {
		if account, err := accountStore.GetAccount(types.Hash{}, voterAddr); err == nil {
			nonce = account.Nonce
		}
	}

	var gasPrice *big.Int
	if gasStore, ok := d.store.(interface {
		GetBaseFee() uint64
	}); ok {
		baseFee := gasStore.GetBaseFee()
		gasPrice = new(big.Int).SetUint64(baseFee)
	} else {
		gasPrice = big.NewInt(1000000000)
	}
	minGasPrice := big.NewInt(1000000000)
	if gasPrice.Cmp(minGasPrice) < 0 {
		gasPrice = minGasPrice
	}

	const voteTxGasLimit uint64 = 100000
	if isUnvote {
		locked := d.getLockedVoteWei(voterAddr)
		spendable := computeSpendableWei(balance, locked)
		txCost := new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(voteTxGasLimit))
		if spendable.Cmp(txCost) < 0 {
			d.logger.Warn("insufficient spendable balance for unvote gas",
				"voter", voterAddr.String(),
				"spendable", spendable.String(),
				"required", txCost.String(),
				"balance", balance.String(),
				"locked", locked.String())
			return nil, &VoteResponse{
				Success: false,
				Error: fmt.Sprintf(
					"insufficient spendable balance for unvote gas: need at least %s wei, available %s wei (balance %s, locked vote %s)",
					txCost.String(), spendable.String(), balance.String(), locked.String(),
				),
			}
		}
	}

	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      voteTxGasLimit,
		To:       nil,
		Value:    big.NewInt(0),
		Input:    d.createVoteTransactionData(voterAddr, candidateAddr, amountInt),
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
		Hash:     types.Hash{},
		Type:     types.LegacyTx,
		ChainID:  new(big.Int).SetUint64(d.chainID),
	}

	tx.ComputeHash(0)
	if tx.Hash == (types.Hash{}) {
		d.logger.Error("🚨 CRITICAL: vote transaction has zero hash after creation",
			"nonce", nonce,
			"gasPrice", gasPrice.String(),
			"voter", voterAddr.String(),
			"candidate", candidateAddr.String(),
			"amount", amountInt.String())
		return nil, &VoteResponse{Success: false, Error: "transaction hash is zero after creation"}
	}

	return &voteTxBuildResult{
		tx:       tx,
		voter:    voterAddr,
		isUnvote: isUnvote,
	}, nil
}

func (d *DPOS) unsignedVoteTxToResponse(built *voteTxBuildResult) *CreateVoteTransactionResponse {
	tx := built.tx
	gasHex := toHexUint64(tx.Gas)
	msg := "Unsigned vote transaction created; sign client-side with EIP-155, then eth_sendRawTransaction"
	if built.isUnvote {
		msg = "Unsigned unvote transaction created; sign client-side with EIP-155, then eth_sendRawTransaction"
	}
	return &CreateVoteTransactionResponse{
		Success:  true,
		Message:  msg,
		ChainID:  toHexUint64(d.chainID),
		From:     built.voter.String(),
		Nonce:    toHexUint64(tx.Nonce),
		GasPrice: toHexBig(tx.GasPrice),
		Gas:      gasHex,
		GasLimit: gasHex,
		To:       nil,
		Value:    toHexBig(tx.Value),
		Data:     toHexBytes(tx.Input),
		Type:     uint64(tx.Type),
		IsUnvote: built.isUnvote,
	}
}

// CreateVoteTransaction builds an unsigned vote/unvote transaction for client-side signing.
// Params: [voter, candidate, amount] or {voter, candidate, amount}. No private key.
// Client: EIP-155 sign → eth_sendRawTransaction(signedRaw).
func (d *DPOS) CreateVoteTransaction(ctx context.Context, params interface{}) (interface{}, error) {
	req, errResp := d.parseVoteRequest(params, false)
	if errResp != nil {
		return &CreateVoteTransactionResponse{Success: false, Error: errResp.Error}, nil
	}

	built, errResp := d.buildUnsignedVoteTransaction(req)
	if errResp != nil {
		return &CreateVoteTransactionResponse{Success: false, Error: errResp.Error}, nil
	}

	return d.unsignedVoteTxToResponse(built), nil
}
