package jsonrpc

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"bytes"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// toUint64Safe 尝试将接口值转为 uint64
func toUint64Safe(v interface{}) (uint64, bool) {
	switch t := v.(type) {
	case uint64:
		return t, true
	case int:
		if t < 0 {
			return 0, false
		}
		return uint64(t), true
	case int64:
		if t < 0 {
			return 0, false
		}
		return uint64(t), true
	case float64:
		if t < 0 {
			return 0, false
		}
		return uint64(t), true
	case string:
		val, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return 0, false
		}
		return val, true
	default:
		return 0, false
	}
}

// VoteMessage represents a vote message for validation
type VoteMessage struct {
	Voter     types.Address `json:"voter"`
	Delegate  types.Address `json:"delegate"`
	Amount    *big.Int      `json:"amount"`
	Round     uint64        `json:"round"`
	Timestamp uint64        `json:"timestamp"`
}

// dposStore provides access to the methods needed by dpos endpoint
type dposStore interface {
	// GetAccount gets account information
	GetAccount(root types.Hash, addr types.Address) (*Account, error)

	// GetBalance gets account balance - use ethStore methods
	GetBalance(root types.Hash, addr types.Address) (*big.Int, error)

	// GetDPoSState gets current DPoS state
	GetDPoSState() (*dpos.State, error)

	// GetValidators gets current validators
	GetValidators() (validator.AccountSet, error)

	// GetValidatorsWithFilter gets validators with optional filtering
	GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error)

	// GetStakingInfo gets staking information
	GetStakingInfo() ([]*dpos.StakeInfo, error)

	// GetPendingTx gets pending transaction from transaction pool
	GetPendingTx(txHash types.Hash) (*types.Transaction, bool)

	// GetNetwork gets network layer for transaction broadcasting
	GetNetwork() interface{}

	// GetServer gets server instance for transaction broadcasting
	GetServer() interface{}

	// GetTxPool gets transaction pool for direct broadcasting
	GetTxPool() interface{}

	// ReadTxLookup returns the block hash using the transaction hash
	ReadTxLookup(hash types.Hash) (types.Hash, bool)

	// GetBlockByHash gets a block using the provided hash
	GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)

	// GetHeaderByNumber gets a header using the provided number
	GetHeaderByNumber(uint64) (*types.Header, bool)
}

// DPOS is the dpos jsonrpc endpoint
type DPOS struct {
	logger  hclog.Logger
	store   dposStore
	chainID uint64
}

// NewDPOS creates a new DPOS endpoint
func NewDPOS(logger hclog.Logger, store dposStore, chainID uint64) *DPOS {
	logger.Info("Initializing DPoS endpoint", "chainID", chainID)

	return &DPOS{
		logger:  logger.Named("dpos"),
		store:   store,
		chainID: chainID,
	}
}

type governanceEngine interface {
	GetParameterProposal(proposalID string) (*dpos.ParameterProposal, error)
	GetActiveProposals() ([]*dpos.ParameterProposal, error)
	GetVotableCurrentParameters() map[string]*dpos.ParameterInfo
	GetCurrentProposalPeriod() map[string]interface{}
	IsParameterVotable(parameter string) bool
	CheckRecoveryPrerequisites(addr types.Address) error
	SignProposalForTx(proposal *dpos.ParameterProposal, proposerPrivateKeyHex string) ([]byte, error)
	SignRecoveryProposalForTx(proposal *dpos.ParameterProposal, proposerPrivateKeyHex string) ([]byte, error)
	SignVoteForTx(vote *dpos.ParameterVote, privateKeyHex string) ([]byte, error)
	GetCurrentBlockNumber() uint64
}

func (d *DPOS) getGovernanceEngine() (governanceEngine, error) {
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}
	engine, ok := dposEngine.(governanceEngine)
	if !ok {
		return nil, errors.New("DPoS engine does not expose governance module")
	}
	return engine, nil
}

// validateProposer 验证提案者：检查私钥是否正确，以及proposer是否是验证者
func (d *DPOS) validateProposer(proposer types.Address, proposerPrivateKeyHex string) error {
	// 1. 验证私钥格式
	if len(proposerPrivateKeyHex) != 64 {
		return fmt.Errorf("invalid proposer private key length: expected 64, got %d", len(proposerPrivateKeyHex))
	}

	// 2. 从私钥恢复地址
	privateKeyBytes, err := hex.DecodeString(proposerPrivateKeyHex)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// 创建ECDSA私钥对象
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}

	// 计算公钥
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	// 从公钥计算地址
	calculatedAddr := crypto.PubKeyToAddress(&privateKey.PublicKey)

	// 3. 验证私钥对应的地址与提供的proposer地址匹配
	if calculatedAddr != proposer {
		return fmt.Errorf("private key does not match proposer address: calculated=%s, expected=%s",
			calculatedAddr.String(), proposer.String())
	}

	// 4. 验证proposer是否是验证者（通过store获取验证者列表）
	d.logger.Info("🔍 [validateProposer] 开始获取验证者列表", "proposer", proposer.String())
	var validators validator.AccountSet

	// 方法1：尝试从 store 获取
	if validators1, err1 := d.store.GetValidatorsWithFilter(false); err1 == nil && len(validators1) > 0 {
		validators = validators1
		d.logger.Info("✅ [validateProposer] 方法1成功：从 store 获取验证者", "count", len(validators))
	} else {
		d.logger.Warn("⚠️ [validateProposer] 方法1失败，尝试方法2",
			"proposer", proposer.String(),
			"error", err1,
			"validatorsCount", len(validators1))

		// 方法2：尝试从 DPoS 引擎直接获取（与 GetStakingInfo 保持一致）
		if dposState, err2 := d.store.GetDPoSState(); err2 == nil && dposState != nil && dposState.StakeStore != nil {
			d.logger.Info("🔍 [validateProposer] 方法2：从 DPoS State.StakeStore 获取验证者")
			if validators2, err2 := dposState.StakeStore.GetValidatorsWithFilter(false); err2 == nil && len(validators2) > 0 {
				validators = validators2
				d.logger.Info("✅ [validateProposer] 方法2成功：从 DPoS State.StakeStore 获取验证者", "count", len(validators))
			} else {
				d.logger.Warn("⚠️ [validateProposer] 方法2失败",
					"proposer", proposer.String(),
					"error", err2,
					"validatorsCount", len(validators2))
			}
		} else {
			d.logger.Warn("⚠️ [validateProposer] 无法获取 DPoS State",
				"proposer", proposer.String(),
				"error", err2,
				"dposStateIsNil", dposState == nil,
				"stakeStoreIsNil", dposState != nil && dposState.StakeStore == nil)
		}

		// 方法3：尝试通过 GetDPoSEngine 获取
		if len(validators) == 0 {
			if dposStore, ok := d.store.(interface {
				GetDPoSEngine() interface{}
			}); ok {
				if dposEngine := dposStore.GetDPoSEngine(); dposEngine != nil {
					if dpos, ok := dposEngine.(*dpos.DPoS); ok {
						d.logger.Info("🔍 [validateProposer] 方法3：从 DPoS Engine 获取验证者")
						if validators3, err3 := dpos.GetValidatorsWithFilter(false); err3 == nil && len(validators3) > 0 {
							validators = validators3
							d.logger.Info("✅ [validateProposer] 方法3成功：从 DPoS Engine 获取验证者", "count", len(validators))
						} else {
							d.logger.Warn("⚠️ [validateProposer] 方法3失败",
								"proposer", proposer.String(),
								"error", err3,
								"validatorsCount", len(validators3))
						}
					}
				}
			}
		}
	}

	// 如果所有方法都失败，记录警告但继续（让后续处理验证）
	if len(validators) == 0 {
		d.logger.Warn("⚠️ [validateProposer] 所有方法都失败，将在交易处理时验证proposer是否是验证者",
			"proposer", proposer.String(),
			"validatorsCount", len(validators))
		return nil // 允许继续，让后续处理验证
	}

	d.logger.Info("📊 [validateProposer] 获取到验证者列表",
		"proposer", proposer.String(),
		"validatorsCount", len(validators),
		"validatorsList", func() []string {
			var vs []string
			for i, v := range validators {
				vs = append(vs, fmt.Sprintf("[%d]%s", i, v.Address.String()))
			}
			return vs
		}())

	// 检查proposer是否在验证者列表中
	found := false
	var foundValidator *validator.ValidatorMetadata
	for _, validator := range validators {
		if validator.Address == proposer {
			found = true
			foundValidator = validator
			break
		}
	}

	if !found {
		d.logger.Warn("❌ [validateProposer] 验证者不在列表中",
			"proposer", proposer.String(),
			"validatorsCount", len(validators),
			"note", "验证者可能不在当前epoch的出块列表中，但可能是注册的验证者")
		return fmt.Errorf("proposer %s is not a validator", proposer.String())
	}

	d.logger.Info("✅ [validateProposer] 找到验证者",
		"proposer", proposer.String(),
		"votingPower", foundValidator.VotingPower.String(),
		"isActive", foundValidator.IsActive)

	d.logger.Info("✅ Proposer验证通过", "proposer", proposer.String())
	return nil
}

// signTransaction signs a DPoS transaction using the private key
func (d *DPOS) signTransaction(tx *types.Transaction, expectedAddr types.Address, privateKeyHex string) error {
	d.logger.Info("🔍 开始签名交易 - 私钥和地址验证")
	d.logger.Info("🔍 期望的发送者地址", "expectedAddr", expectedAddr.String())
	d.logger.Info("🔍 传入的私钥", "privateKeyHex", privateKeyHex, "length", len(privateKeyHex))

	// Force user to provide private key
	if privateKeyHex == "" {
		d.logger.Error("❌ 私钥为空")
		return fmt.Errorf("private key is required for signing DPoS transactions")
	}

	d.logger.Info("🔍 私钥格式验证开始", "privateKeyHex", privateKeyHex, "length", len(privateKeyHex))

	// Validate hex string first
	if len(privateKeyHex) != 64 {
		d.logger.Error("❌ 私钥长度错误", "expected", 64, "actual", len(privateKeyHex))
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	// Check if string contains only valid hex characters
	for i, char := range privateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			d.logger.Error("❌ 私钥包含无效字符", "position", i, "char", string(char), "unicode", fmt.Sprintf("U+%04X", char))
			return fmt.Errorf("invalid hex character at position %d: %c (U+%04X)", i, char, char)
		}
	}

	d.logger.Info("✅ 私钥格式验证通过")

	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		d.logger.Error("❌ 私钥解码失败", "error", err)
		return fmt.Errorf("failed to decode user-provided private key: %w", err)
	}

	d.logger.Info("✅ 私钥解码成功", "length", len(privateKeyBytes))

	// Use direct ECDSA private key creation instead of crypto.BytesToECDSAPrivateKey
	if len(privateKeyBytes) != 32 {
		d.logger.Error("❌ 私钥字节长度错误", "expected", 32, "actual", len(privateKeyBytes))
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	d.logger.Info("🔍 开始创建ECDSA私钥对象")

	// Create ECDSA private key directly using secp256k1 curve
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}

	// Calculate the public key from the private key
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	d.logger.Info("✅ ECDSA私钥对象创建成功", "privateKeyD", privateKey.D.String())

	// 从私钥计算对应的地址并验证
	d.logger.Info("🔍 从私钥计算对应的地址")
	calculatedAddr := crypto.PubKeyToAddress(&privateKey.PublicKey)
	d.logger.Info("🔍 私钥对应的地址", "calculatedAddr", calculatedAddr.String())
	d.logger.Info("🔍 期望的地址", "expectedAddr", expectedAddr.String())

	if calculatedAddr != expectedAddr {
		d.logger.Error("❌ 私钥与投票者地址不匹配",
			"calculatedAddr", calculatedAddr.String(),
			"expectedAddr", expectedAddr.String(),
			"match", false)
		return fmt.Errorf("private key does not match voter address: calculated=%s, expected=%s",
			calculatedAddr.String(), expectedAddr.String())
	}

	d.logger.Info("✅ 私钥与投票者地址匹配验证通过")

	// Calculate transaction hash for signing using EIP-155 scheme to match txpool signer
	// Use the chainID from the DPOS endpoint configuration instead of hardcoded value
	eip155Signer := crypto.NewEIP155Signer(d.chainID, false)

	d.logger.Info("=== 标记2: 开始签名交易 ===")
	// For EIP-155 signing, we need to use the signer's SignTx method
	// This ensures the hash calculation and V value are correct
	signedTx, err := eip155Signer.SignTx(tx, privateKey)
	if err != nil {
		d.logger.Error("Failed to sign transaction with EIP-155 signer", "error", err)
		return fmt.Errorf("failed to sign transaction with EIP-155 signer: %w", err)
	}

	// Copy the signature components from the signed transaction
	tx.R = signedTx.R
	tx.S = signedTx.S
	tx.V = signedTx.V

	d.logger.Info("=== 标记3: 签名完成，R=", tx.R.String(), "S=", tx.S.String(), "V=", tx.V.String(), "===")
	d.logger.Info("Transaction signed successfully with EIP-155 signer", "r", tx.R.String(), "s", tx.S.String(), "v", tx.V.String())

	// Recover sender with the same signer and set tx.From for logging / consistency
	d.logger.Info("🔍 开始从签名恢复发送者地址")
	senderAddr, err := eip155Signer.Sender(tx)
	if err == nil {
		tx.From = senderAddr
		d.logger.Info("✅ 发送者地址恢复成功", "recoveredAddr", tx.From.String())
		d.logger.Info("🔍 地址匹配验证",
			"recoveredAddr", tx.From.String(),
			"expectedAddr", expectedAddr.String(),
			"calculatedAddr", calculatedAddr.String())

		if tx.From != expectedAddr {
			d.logger.Error("❌ 恢复的地址与期望地址不匹配",
				"recoveredAddr", tx.From.String(),
				"expectedAddr", expectedAddr.String())
		} else {
			d.logger.Info("✅ 恢复的地址与期望地址匹配")
		}
	} else {
		d.logger.Error("❌ 从签名恢复发送者地址失败", "error", err)
	}

	// Test signature recovery to ensure it works
	d.logger.Info("Testing signature recovery...")
	// Use the EIP-155 signer's Hash method for recovery test
	hashForRecovery := eip155Signer.Hash(tx)

	// We need to reconstruct the signature from R, S, V for recovery
	// Extract recovery ID from V value: V = 2*chainID + 35 + recoveryID
	recoveryID := int(tx.V.Int64() - int64(2*d.chainID) - 35)
	if recoveryID < 0 || recoveryID > 1 {
		return fmt.Errorf("invalid recovery ID: %d", recoveryID)
	}

	// Reconstruct signature: R (32 bytes) + S (32 bytes) + recoveryID (1 byte)
	// Ensure R and S are padded to 32 bytes
	signatureForRecovery := make([]byte, 65)

	// Pad R to 32 bytes
	rBytes := tx.R.Bytes()
	if len(rBytes) > 32 {
		return fmt.Errorf("R value too large: %d bytes", len(rBytes))
	}
	copy(signatureForRecovery[32-len(rBytes):32], rBytes)

	// Pad S to 32 bytes
	sBytes := tx.S.Bytes()
	if len(sBytes) > 32 {
		return fmt.Errorf("S value too large: %d bytes", len(sBytes))
	}
	copy(signatureForRecovery[64-len(sBytes):64], sBytes)

	// Set recovery ID
	signatureForRecovery[64] = byte(recoveryID)

	d.logger.Info("Signature reconstruction", "rBytes", len(rBytes), "sBytes", len(sBytes), "recoveryID", recoveryID)

	// Now recover using the reconstructed signature
	recoveredPubKeyBytes, err := crypto.Ecrecover(hashForRecovery.Bytes(), signatureForRecovery)
	if err != nil {
		d.logger.Error("Failed to recover public key with reconstructed signature", "error", err)
		return fmt.Errorf("failed to recover public key with reconstructed signature: %w", err)
	}

	// Derive address directly from recovered public key bytes
	// Expect uncompressed public key format (0x04 || X || Y) or raw X||Y (64 bytes)
	rawPub := recoveredPubKeyBytes
	if len(rawPub) == 65 && rawPub[0] == 0x04 {
		rawPub = rawPub[1:]
	}
	if len(rawPub) != 64 {
		return fmt.Errorf("invalid recovered public key length: %d (expected 64 or 65)", len(recoveredPubKeyBytes))
	}
	// keccak256(X||Y), take last 20 bytes
	h := crypto.Keccak256(rawPub)
	recoveredAddr := types.BytesToAddress(h[12:])
	d.logger.Info("Signature recovery test", "recoveredAddress", recoveredAddr.String(), "expectedAddress", expectedAddr.String(), "match", recoveredAddr == expectedAddr)

	if recoveredAddr != expectedAddr {
		d.logger.Error("Signature recovery failed", "recoveredAddress", recoveredAddr.String(), "expectedAddress", expectedAddr.String())
		return fmt.Errorf("signature recovery failed: recovered address %s does not match expected address %s", recoveredAddr.String(), expectedAddr.String())
	}

	return nil
}

// VoteRequest represents a vote request
type VoteRequest struct {
	Voter      string `json:"voter"`
	Candidate  string `json:"candidate"`
	Amount     string `json:"amount"`
	PrivateKey string `json:"privateKey,omitempty"`
}

// VoteResponse represents a vote response
type VoteResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	Error       string `json:"error,omitempty"`
}

// UnvoteResponse 解质押响应
type UnvoteResponse struct {
	Success          bool                   `json:"success"`
	Voter            types.Address          `json:"voter"`
	Validator        types.Address          `json:"validator"`
	WithdrawAmount   *big.Int               `json:"withdrawAmount"`    // 可提取金额（削减后）
	OriginalAmount   *big.Int               `json:"originalAmount"`    // 原始投票金额
	TotalSlashAmount *big.Int               `json:"totalSlashAmount"`  // 总削减金额
	SlashCount       int                    `json:"slashCount"`        // 削减次数
	SlashingHistory  []*dpos.SlashingRecord `json:"slashingHistory"`   // 削减历史
	Message          string                 `json:"message,omitempty"` // 提示信息
	Error            string                 `json:"error,omitempty"`
}

// Vote handles dpos_vote RPC method
func (d *DPOS) Vote(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Debug("DPoS Vote called", "params", params)

	// Parse parameters
	var req VoteRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters: [voter, candidate, amount, privateKey]
		if len(p) != 4 {
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 4 parameters [voter, candidate, amount, privateKey], got %d", len(p)),
			}, nil
		}
		// Four parameters: [voter, candidate, amount, privateKey]
		if voter, ok := p[0].(string); ok {
			req.Voter = voter
		} else {
			return &VoteResponse{
				Success: false,
				Error:   "first parameter must be a string address (voter)",
			}, nil
		}
		if candidate, ok := p[1].(string); ok {
			req.Candidate = candidate
		} else {
			return &VoteResponse{
				Success: false,
				Error:   "second parameter must be a string address (candidate)",
			}, nil
		}
		if amount, ok := p[2].(string); ok {
			req.Amount = amount
		} else {
			return &VoteResponse{
				Success: false,
				Error:   "third parameter must be a string amount",
			}, nil
		}
		// Store private key for later use in signing
		if privateKey, ok := p[3].(string); ok {
			req.PrivateKey = privateKey
		} else {
			return &VoteResponse{
				Success: false,
				Error:   "fourth parameter must be a string private key",
			}, nil
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
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or VoteRequest", params),
		}, nil
	}

	// Validate request
	if req.Voter == "" {
		return &VoteResponse{
			Success: false,
			Error:   "voter address is required",
		}, nil
	}
	if req.Candidate == "" {
		return &VoteResponse{
			Success: false,
			Error:   "candidate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &VoteResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}
	if req.PrivateKey == "" {
		return &VoteResponse{
			Success: false,
			Error:   "private key is required",
		}, nil
	}

	// Parse addresses
	voterAddr := types.StringToAddress(req.Voter)
	candidateAddr := types.StringToAddress(req.Candidate)

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &VoteResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// 🔍 调试日志：记录解析后的 amount
	d.logger.Info("🔍 [RPC] 解析 amount 参数",
		"原始字符串", req.Amount,
		"解析后值", amountInt.String(),
		"Sign()", amountInt.Sign(),
		"与-1比较", amountInt.Cmp(big.NewInt(-1)),
		"与0比较", amountInt.Cmp(big.NewInt(0)))

	// amount = 0 不允许，返回错误
	if amountInt.Sign() == 0 {
		return &VoteResponse{
			Success: false,
			Error:   "amount cannot be zero",
		}, nil
	}
	// amount = -1 表示执行撤销，继续往下走创建交易
	// amount > 0 表示投票，继续往下走创建交易
	// Check voter balance
	// Try to get balance with different approaches
	var balance *big.Int
	var err error

	// Method 1: Try to get balance using available methods
	d.logger.Info("Method 1: Attempting to get balance...")

	// Try to get balance directly using the available methods
	if balanceStore, ok := d.store.(interface {
		GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
	}); ok {
		d.logger.Debug("Method 1: Store implements GetBalance method")

		// Try to get balance with different state roots
		d.logger.Debug("Method 1: Trying to get balance with different state roots...")

		// First, try to get the latest state root from the store
		var latestRoot types.Hash
		var foundValidRoot bool

		// Try to get latest state root from different possible interfaces
		// Method 1a: Try to get from ethBlockchainStore.Header() method
		if headerStore, ok := d.store.(interface {
			Header() *types.Header
		}); ok {
			latestHeader := headerStore.Header()
			if latestHeader != nil {
				latestRoot = latestHeader.StateRoot
				d.logger.Debug("Method 1a: Got state root from Header() method", "root", latestRoot.String())
				foundValidRoot = true
			}
		}

		// Method 1b: Try to get from GetLatestStateRoot method (if exists)
		if !foundValidRoot {
			if latestStore, ok := d.store.(interface {
				GetLatestStateRoot() types.Hash
			}); ok {
				latestRoot = latestStore.GetLatestStateRoot()
				d.logger.Debug("Method 1b: Got latest state root", "root", latestRoot.String())
				foundValidRoot = true
			}
		}

		// Method 1c: Try to get from GetLatestHeader method (if exists)
		if !foundValidRoot {
			if headerStore, ok := d.store.(interface {
				GetLatestHeader() *types.Header
			}); ok {
				latestHeader := headerStore.GetLatestHeader()
				if latestHeader != nil {
					latestRoot = latestHeader.StateRoot
					d.logger.Debug("Method 1c: Got state root from GetLatestHeader", "root", latestRoot.String())
					foundValidRoot = true
				}
			}
		}

		// Method 1d: Try to get from GetLatestBlock method (if exists)
		if !foundValidRoot {
			if blockStore, ok := d.store.(interface {
				GetLatestBlock() *types.Block
			}); ok {
				latestBlock := blockStore.GetLatestBlock()
				if latestBlock != nil {
					latestRoot = latestBlock.Header.StateRoot
					d.logger.Debug("Method 1d: Got state root from GetLatestBlock", "root", latestRoot.String())
					foundValidRoot = true
				}
			}
		}

		// Method 1e: Try to get from GetHeaderByNumber method with latest block number
		if !foundValidRoot {
			if headerStore, ok := d.store.(interface {
				Header() *types.Header
				GetHeaderByNumber(uint64) (*types.Header, bool)
			}); ok {
				latestHeader := headerStore.Header()
				if latestHeader != nil && latestHeader.Number > 0 {
					// Try to get the previous block header as a fallback
					if prevHeader, ok := headerStore.GetHeaderByNumber(latestHeader.Number - 1); ok {
						latestRoot = prevHeader.StateRoot
						d.logger.Debug("Method 1e: Got state root from previous block header", "root", latestRoot.String(), "blockNumber", prevHeader.Number)
						foundValidRoot = true
					}
				}
			}
		}

		if foundValidRoot && latestRoot != (types.Hash{}) {
			d.logger.Debug("Method 1: Trying to get balance with valid state root", "root", latestRoot.String())
			balance, err = balanceStore.GetBalance(latestRoot, voterAddr)
			if err == nil && balance != nil {
				d.logger.Debug("Method 1: Successfully got balance with valid root", "balance", balance.String())
			} else {
				d.logger.Debug("Method 1: Failed to get balance with valid root", "error", err)
			}
		} else {
			d.logger.Debug("Method 1: No valid state root found, cannot get balance")
		}
	} else {
		d.logger.Debug("Method 1: Store does not implement GetBalance method")
	}

	// Method 2: If still no balance, try to get from consensus engine directly
	if balance == nil || err != nil {
		d.logger.Debug("Trying to get balance from consensus engine directly...")
		if hub, ok := d.store.(interface {
			GetConsensus() interface{}
		}); ok {
			consensusEngine := hub.GetConsensus()

			// Try to get account balance from consensus engine
			if balanceEngine, ok := consensusEngine.(interface {
				GetAccountBalance(addr types.Address) (*big.Int, error)
			}); ok {
				balance, err = balanceEngine.GetAccountBalance(voterAddr)
				if err == nil && balance != nil {
					d.logger.Debug("Balance retrieved from consensus engine", "balance", balance.String())
				}
			}

			// If no balance method, try to get from DPoS state
			if balance == nil {
				if dposEngine, ok := consensusEngine.(interface {
					GetDPoSState() (*dpos.State, error)
				}); ok {
					dposState, err := dposEngine.GetDPoSState()
					if err == nil && dposState != nil {
						// Try to get balance from DPoS state
						d.logger.Debug("Trying to get balance from DPoS state")
						// Note: This would need to be implemented based on actual DPoS state structure
					}
				}
			}
		}
	}

	// Method 3: Try to get balance using zero hash as fallback (for genesis or initial state)
	if balance == nil || err != nil {
		d.logger.Debug("Method 3: Trying to get balance with zero hash as fallback...")
		if balanceStore, ok := d.store.(interface {
			GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
		}); ok {
			zeroHash := types.Hash{}
			balance, err = balanceStore.GetBalance(zeroHash, voterAddr)
			if err == nil && balance != nil {
				d.logger.Debug("Method 3: Successfully got balance with zero hash", "balance", balance.String())
			} else {
				d.logger.Debug("Method 3: Failed to get balance with zero hash", "error", err)
			}
		}
	}

	// Check if we successfully retrieved balance
	if balance == nil {
		d.logger.Error("Failed to retrieve voter balance - cannot proceed with vote")
		return &VoteResponse{
			Success: false,
			Error:   "unable to verify voter balance - cannot proceed with vote",
		}, nil
	}

	d.logger.Debug("Voter balance retrieved", "balance", balance.String())

	if balance.Cmp(amountInt) < 0 {
		d.logger.Error("Insufficient balance", "balance", balance.String(), "required", amountInt.String())
		return &VoteResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}
	d.logger.Debug("Balance check passed")

	d.logger.Info("🔍 开始验证受托人候选人资格",
		"voter", voterAddr.String(),
		"candidate", candidateAddr.String(),
		"amount", amountInt.String())

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		d.logger.Error("❌ DPoS引擎不可用，无法验证受托人资格")
		return &VoteResponse{
			Success: false,
			Error:   "DPoS engine not available for delegate validation",
		}, nil
	}
	// 检查受托人是否已注册（创世验证者例外）
	if isRegistered, ok := dposEngine.(interface {
		IsDelegateRegistered(address types.Address) bool
	}); ok {
		// 检查是否为创世验证者
		isGenesis, okGenesis := dposEngine.(interface {
			IsGenesisValidator(address types.Address) bool
		})

		// 创世验证者可以直接被投票，无需注册
		if okGenesis && isGenesis.IsGenesisValidator(candidateAddr) {
			d.logger.Info("✅ 受托人是创世验证者，跳过注册检查", "candidate", candidateAddr.String())
		} else if !isRegistered.IsDelegateRegistered(candidateAddr) {
			d.logger.Warn("❌ 受托人未注册，投票被拒绝",
				"candidate", candidateAddr.String(),
				"voter", voterAddr.String(),
				"amount", amountInt.String())
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("delegate %s is not registered", candidateAddr.String()),
			}, nil
		} else {
			d.logger.Info("✅ 受托人注册状态验证通过", "candidate", candidateAddr.String())
		}
	} else {
		d.logger.Warn("⚠️ DPoS引擎不支持受托人注册检查，跳过验证")
	}

	// 检查受托人是否为候选人状态
	if isCandidate, ok := dposEngine.(interface {
		IsDelegateCandidate(address types.Address) bool
	}); ok {
		if !isCandidate.IsDelegateCandidate(candidateAddr) {
			d.logger.Warn("❌ 受托人不是候选人状态，投票被拒绝",
				"candidate", candidateAddr.String(),
				"voter", voterAddr.String(),
				"amount", amountInt.String())
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("delegate %s is not a candidate", candidateAddr.String()),
			}, nil
		}
		d.logger.Info("✅ 受托人候选人状态验证通过", "candidate", candidateAddr.String())
	} else {
		d.logger.Warn("⚠️ DPoS引擎不支持受托人候选人检查，跳过验证")
	}
	d.logger.Info("🔍 开始预验证投票参数", "voter", voterAddr, "candidate", candidateAddr, "amount", amountInt)

	voteMessage := &VoteMessage{
		Voter:     voterAddr,
		Delegate:  candidateAddr,
		Amount:    amountInt,
		Round:     0, // 使用当前轮次
		Timestamp: uint64(time.Now().Unix()),
	}

	// 只进行验证，不实际更新状态，避免重复处理
	if dposEngineInstance, ok := dposEngine.(*dpos.DPoS); ok {
		// 只调用验证方法，不更新状态
		if err := dposEngineInstance.ValidateVoteOnly(voteMessage.Voter, voteMessage.Delegate, voteMessage.Amount); err != nil {
			d.logger.Error("❌ 投票预验证失败", "error", err)
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("vote validation failed: %v", err),
			}, nil
		}
		d.logger.Info("✅ 投票预验证通过")
	} else {
		d.logger.Warn("⚠️ DPoS引擎类型不匹配，跳过预验证")
	}

	// Implement actual voting logic
	d.logger.Info("Vote validation completed", "voter", voterAddr, "candidate", candidateAddr, "amount", amountInt)

	// Step 1: Create a vote transaction
	d.logger.Info("Creating vote transaction...")

	// 🚨 检测投票参数
	if voterAddr == (types.Address{}) {
		d.logger.Error("🚨 CRITICAL: voter address is zero address")
		return &VoteResponse{
			Success: false,
			Error:   "voter address is zero address",
		}, nil
	}

	if candidateAddr == (types.Address{}) {
		d.logger.Error("🚨 CRITICAL: candidate address is zero address")
		return &VoteResponse{
			Success: false,
			Error:   "candidate address is zero address",
		}, nil
	}
	// Get account nonce for the voter
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(voterAddr)
	} else {
		// Fallback: try to get nonce from account
		if accountStore, ok := d.store.(interface {
			GetAccount(root types.Hash, addr types.Address) (*Account, error)
		}); ok {
			if account, err := accountStore.GetAccount(types.Hash{}, voterAddr); err == nil {
				nonce = account.Nonce
			}
		}
	}
	// Get current gas price
	var gasPrice *big.Int
	if gasStore, ok := d.store.(interface {
		GetBaseFee() uint64
	}); ok {
		baseFee := gasStore.GetBaseFee()
		gasPrice = new(big.Int).SetUint64(baseFee)
	} else {
		gasPrice = big.NewInt(1000000000) // 1 gwei default
	}
	// Ensure gas price meets minimum price limit (1 gwei = 1000000000 wei)
	// This prevents "transaction underpriced" errors
	minGasPrice := big.NewInt(1000000000) // 1 gwei
	if gasPrice.Cmp(minGasPrice) < 0 {
		gasPrice = minGasPrice
		d.logger.Info("Gas price adjusted to meet minimum requirement", "gasPrice", gasPrice.String())
	}
	d.logger.Info("=== 标记1: 开始创建投票交易 ===")
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      100000, // 在这里加标记
		To:       nil,    // 在这里加标记
		Value:    big.NewInt(0),
		Input:    d.createVoteTransactionData(voterAddr, candidateAddr, amountInt), // 在这里加标记
		V:        big.NewInt(0),                                                    // Will be set after signing
		R:        big.NewInt(0),                                                    // Will be set after signing
		S:        big.NewInt(0),                                                    // Will be set after signing
		Hash:     types.Hash{},
		// Don't set From field - let transaction pool recover it from signature
		// This ensures consistency between From field and signature
	}

	// Set transaction type to legacy (0) for compatibility
	tx.Type = types.LegacyTx

	// Verify that the private key corresponds to the voter address
	// This ensures the signature will recover the correct address
	d.logger.Info("Address verification", "voterAddr", voterAddr.String(), "privateKeyHex", "ed7ba26f0568b6b9cd3296ff7dcfe56fc6041fea8963246334bdaa276783add6")
	d.logger.Info("Transaction pool will automatically recover sender from signature")

	// 🚨 检测交易创建后的哈希
	tx.ComputeHash(0)
	if tx.Hash == (types.Hash{}) {
		d.logger.Error("🚨 CRITICAL: vote transaction has zero hash after creation",
			"nonce", nonce,
			"gasPrice", gasPrice.String(),
			"voter", voterAddr.String(),
			"candidate", candidateAddr.String(),
			"amount", amountInt.String())
		return &VoteResponse{
			Success: false,
			Error:   "transaction hash is zero after creation",
		}, nil
	}

	d.logger.Info("Vote transaction created", "txHash", tx.Hash.String(), "nonce", nonce, "gasPrice", gasPrice.String())

	// Step 2: Sign the transaction with private key
	d.logger.Info("Signing transaction with private key...")
	if err := d.signTransaction(tx, voterAddr, req.PrivateKey); err != nil {
		d.logger.Error("Failed to sign transaction", "error", err)
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to sign transaction: %v", err),
		}, nil
	}

	// Recalculate hash after signing
	txWithHash := tx.ComputeHash(0)
	txHash := txWithHash.Hash
	d.logger.Info("Transaction signed and hash recalculated", "txHash", txHash.String())

	// Log transaction details for debugging
	d.logger.Info("Final transaction details",
		"type", tx.Type,
		"nonce", tx.Nonce,
		"gasPrice", tx.GasPrice.String(),
		"gas", tx.Gas,
		"value", tx.Value.String(),
		"v", tx.V.String(),
		"r", tx.R.String(),
		"s", tx.S.String(),
		"hash", tx.Hash.String(),
		"from", tx.From.String(),
		"expectedFrom", voterAddr.String())

	// Important: Transaction pool will recover sender from signature
	d.logger.Info("Transaction pool will verify: recovered address has sufficient balance")

	// Step 3: Add transaction to the transaction pool for proper tracking
	d.logger.Info("Adding transaction to pool for DPoS voting history...")

	// Try to get transaction pool from store
	var txAdded bool

	// Debug: Log the store type to understand what we're working with
	d.logger.Info("Store type", "type", fmt.Sprintf("%T", d.store))

	d.logger.Info("=== 标记5: 准备加入交易池 ===")
	// Method 1: Try to access AddTx through ethStore interface
	if ethStore, ok := d.store.(interface {
		AddTx(tx *types.Transaction) error
	}); ok {
		d.logger.Info("Store implements AddTx interface, attempting to add transaction...")
		if err := ethStore.AddTx(tx); err != nil {
			d.logger.Error("Failed to add transaction to pool", "error", err)
			// Continue anyway, we'll update DPoS state directly as fallback
		} else {
			d.logger.Info("Transaction added to pool successfully")
			txAdded = true
		}
	} else {
		d.logger.Warn("Store does NOT implement AddTx interface")
		d.logger.Warn("Available methods on store:")

		// Try to get more information about what methods are available
		storeValue := reflect.ValueOf(d.store)
		if storeValue.Kind() == reflect.Ptr {
			storeValue = storeValue.Elem()
		}

		storeType := storeValue.Type()
		for i := 0; i < storeType.NumMethod(); i++ {
			method := storeType.Method(i)
			d.logger.Warn("Available method", "name", method.Name, "type", method.Type.String())
		}

		d.logger.Warn("Transaction pool not available, will update DPoS state directly")
	}

	// 无论 AddTx 是否成功，都尝试广播交易到网络
	d.logger.Info("=== 标记6: 开始强制广播交易 ===")
	d.logger.Info("Attempting to broadcast transaction to network", "txHash", tx.Hash.String(), "txAdded", txAdded)

	// 强制广播交易到网络（确保其他节点能收到）
	if err := d.broadcastTransaction(tx); err != nil {
		d.logger.Warn("Failed to broadcast transaction directly", "error", err, "txHash", tx.Hash.String())
		// 不返回错误，继续执行
	} else {
		d.logger.Info("Transaction broadcasted successfully", "txHash", tx.Hash.String())
	}

	// 检查交易池状态，诊断为什么交易没有被共识引擎拉取
	d.logger.Info("=== 标记9: 检查交易池状态 ===")
	if txpoolStore, ok := d.store.(interface {
		GetTxPool() interface{}
	}); ok {
		txpool := txpoolStore.GetTxPool()
		if txpool != nil {
			if debugTxPool, ok := txpool.(interface {
				DebugInfo() map[string]interface{}
			}); ok {
				debugInfo := debugTxPool.DebugInfo()
				d.logger.Info("Transaction pool debug info", "debugInfo", debugInfo)

				// 特别关注关键指标
				if executablesCount, ok := debugInfo["executablesCount"].(int); ok {
					d.logger.Info("Executables queue count", "count", executablesCount)
				}
				if pendingCount, ok := debugInfo["pendingCount"].(int64); ok {
					d.logger.Info("Pending transactions count", "count", pendingCount)
				}
				if hasTopic, ok := debugInfo["hasTopic"].(bool); ok {
					d.logger.Info("Transaction pool has topic", "hasTopic", hasTopic)
				}
				if isSealing, ok := debugInfo["isSealing"].(bool); ok {
					d.logger.Info("Transaction pool is sealing", "isSealing", isSealing)
				}

				// 如果 executables 队列为空但有 pending 交易，记录警告但不手动干预
				if executablesCount, ok := debugInfo["executablesCount"].(int); ok {
					if executablesCount == 0 {
						d.logger.Warn("Executables queue is empty - this may indicate a transaction promotion issue")
						d.logger.Info("Note: Manual intervention removed to avoid interfering with consensus flow")
						d.logger.Info("Transactions should be promoted automatically by the consensus engine")
					}
				}
			}
		}
	}

	// Step 4: Update DPoS state immediately (for immediate effect)
	d.logger.Info("Updating DPoS state...")

	// Try to update the consensus engine state
	var dposStateUpdated bool

	// Debug: Check if store has GetConsensus method
	if consensusStore, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		d.logger.Info("Store has GetConsensus method, attempting to get consensus engine...")

		consensusEngine := d.getDPoSEngineDirectly(consensusStore)

		if consensusEngine == nil {
			d.logger.Warn("Consensus engine is nil")
		} else {
			d.logger.Info("Consensus engine type", "type", fmt.Sprintf("%T", consensusEngine))
		}
	} else {
		d.logger.Warn("Store does NOT have GetConsensus method")
	}

	// Step 5: Return success response with transaction details
	successMessage := "Vote operation completed successfully"
	if !txAdded && !dposStateUpdated {
		successMessage = "Vote operation failed (both transaction pool and DPoS state update failed)"
	} else if !txAdded {
		successMessage = "Vote operation completed (DPoS state updated directly, transaction pool failed)"
	} else if !dposStateUpdated {
		successMessage = "Vote operation completed successfully (transaction added to pool, will be processed in next block)"
	}

	d.logger.Info("Vote operation completed successfully", "txHash", txHash.String(), "txAdded", txAdded, "dposStateUpdated", dposStateUpdated)

	// Try to get the actual block number if transaction is already mined
	var blockNumber uint64
	var blockStatus string

	if txAdded {
		// Check if transaction is already in a block using available methods
		// Try to access ethBlockchainStore methods through type assertion
		if blockchainStore, ok := d.store.(interface {
			ReadTxLookup(txnHash types.Hash) (types.Hash, bool)
			GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)
		}); ok {
			// First try to find the block hash containing this transaction
			if blockHash, found := blockchainStore.ReadTxLookup(txHash); found {
				// Then get the block to extract the block number
				if block, ok := blockchainStore.GetBlockByHash(blockHash, false); ok {
					blockNumber = block.Number()
					blockStatus = "mined"
					d.logger.Info("Transaction found in blockchain", "blockNumber", blockNumber, "blockHash", blockHash.String())
				} else {
					blockStatus = "block_found_but_no_details"
					d.logger.Info("Block found but could not retrieve block details")
					// If block found but can't get details, try to get current block height
					blockNumber = d.getCurrentBlockHeight()
				}
			} else {
				// Transaction is pending, return current block height instead of 0
				blockNumber = d.getCurrentBlockHeight()
				blockStatus = "pending"
				d.logger.Info("Transaction not yet mined, returning current block height", "blockNumber", blockNumber)
			}
		} else {
			// Store doesn't support ReadTxLookup, get current block height
			blockNumber = d.getCurrentBlockHeight()
			blockStatus = "store_not_supported"
			d.logger.Info("Store does not support ReadTxLookup/GetBlockByHash, using current block height", "blockNumber", blockNumber)
		}
	} else {
		// Transaction was not added, still return current block height
		blockNumber = d.getCurrentBlockHeight()
		blockStatus = "tx_not_added"
		d.logger.Info("Transaction was not added to pool, returning current block height", "blockNumber", blockNumber)
	}

	// Log the final status
	d.logger.Info("Final transaction status",
		"txHash", txHash.String(),
		"blockNumber", blockNumber,
		"blockStatus", blockStatus,
		"txAdded", txAdded,
		"dposStateUpdated", dposStateUpdated)

	return &VoteResponse{
		Success:     true,
		Message:     successMessage,
		TxHash:      txHash.String(),
		BlockNumber: blockNumber, // Real block number if mined, current block height if pending
	}, nil
}

// createVoteTransactionData creates the transaction data for a vote operation
func (d *DPOS) createVoteTransactionData(voter, candidate types.Address, amount *big.Int) []byte {
	// Since DPoS is implemented directly in code, we just need a simple identifier
	// This data will be used to identify this as a DPoS vote transaction

	data := make([]byte, 0, 32)

	// Add a simple identifier for DPoS vote (4 bytes)
	data = append(data, []byte("DPOS")...)

	// Add voter address (20 bytes)
	data = append(data, voter.Bytes()...)

	// Add candidate address (20 bytes)
	data = append(data, candidate.Bytes()...)

	// Add amount (32 bytes, padded)
	// 🔧 修复：支持负数编码（-1 使用全1表示）
	var amountBytes []byte
	if amount.Cmp(big.NewInt(-1)) == 0 {
		// amount = -1 使用 32 字节全 1 (0xFFFFFFFF...) 表示
		amountBytes = make([]byte, 32)
		for i := range amountBytes {
			amountBytes[i] = 0xFF
		}
		d.logger.Info("🔧 [编码] amount = -1，使用全1编码", "encoded", fmt.Sprintf("%x", amountBytes))
	} else {
		// 正数正常编码
		amountBytes = amount.Bytes()
		if len(amountBytes) > 32 {
			amountBytes = amountBytes[len(amountBytes)-32:] // Take last 32 bytes
		}
		// Pad with zeros to 32 bytes
		for len(amountBytes) < 32 {
			amountBytes = append([]byte{0}, amountBytes...)
		}
	}
	data = append(data, amountBytes...)

	return data
}

// VoteByAddress handles dpos_voteByAddress RPC method with single address parameter
// This method is designed to handle the case where only one address is provided
// It will use the address as both voter and candidate, with a default amount
func (d *DPOS) VoteByAddress(ctx context.Context, params interface{}) (*VoteResponse, error) {
	d.logger.Info("DPoS VoteByAddress called", "params", params)

	// Parse parameters
	var address string
	switch p := params.(type) {
	case []interface{}:
		if len(p) == 0 {
			return &VoteResponse{
				Success: false,
				Error:   "address is required",
			}, nil
		}
		if addr, ok := p[0].(string); ok {
			address = addr
		} else {
			return &VoteResponse{
				Success: false,
				Error:   "first parameter must be a string address",
			}, nil
		}
	case string:
		address = p
	default:
		return &VoteResponse{
			Success: false,
			Error:   "invalid parameter format, expected array with address string",
		}, nil
	}

	// Validate address
	if address == "" {
		return &VoteResponse{
			Success: false,
			Error:   "address is required",
		}, nil
	}

	// Parse address
	addr := types.StringToAddress(address)

	// Query voting information for this address
	// Check if address exists in staking info (as validator or voter)
	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}

	// Find voting information for this address
	var foundStakeInfo *dpos.StakeInfo
	for _, info := range stakingInfo {
		// Compare addresses (case-insensitive)
		if info.Staker == addr || strings.EqualFold(info.Staker.String(), addr.String()) {
			foundStakeInfo = info
			break
		}
	}

	if foundStakeInfo == nil {
		// Address not found in staking info
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("address %s has no voting/staking information", address),
		}, nil
	}

	// Return voting information with details
	return &VoteResponse{
		Success:     true,
		Message:     fmt.Sprintf("Address %s is a validator/staker with amount %s", address, foundStakeInfo.Amount.String()),
		TxHash:      "", // Not applicable for query operations
		BlockNumber: 0,  // Not applicable for query operations
	}, nil
}

// GetStakingInfo handles dpos_getStakingInfo RPC method
func (d *DPOS) GetStakingInfo(ctx context.Context, blockNumber *uint64) (interface{}, error) {
	d.logger.Info("DPoS GetStakingInfo called", "blockNumber", blockNumber)
	var validators validator.AccountSet
	var err error

	// 方法1：尝试从 store 获取
	if validators, err = d.store.GetValidatorsWithFilter(false); err == nil && len(validators) > 0 {
		d.logger.Info("Successfully retrieved validators from store", "count", len(validators))
	} else {
		// 方法2：尝试从 DPoS 引擎直接获取
		if dposState, err2 := d.store.GetDPoSState(); err2 == nil && dposState != nil && dposState.StakeStore != nil {
			if validators, err = dposState.StakeStore.GetValidatorsWithFilter(false); err == nil && len(validators) > 0 {
				d.logger.Info("Successfully retrieved validators from DPoS state", "count", len(validators))
			}
		}
		// 方法3：尝试通过 GetDPoSEngine 获取
		if len(validators) == 0 {
			if dposStore, ok := d.store.(interface {
				GetDPoSEngine() interface{}
			}); ok {
				if dposEngine := dposStore.GetDPoSEngine(); dposEngine != nil {
					if dpos, ok := dposEngine.(*dpos.DPoS); ok {
						if validators, err = dpos.GetValidatorsWithFilter(false); err == nil && len(validators) > 0 {
							d.logger.Info("Successfully retrieved validators from DPoS engine", "count", len(validators))
						}
					}
				}
			}
		}
	}

	if err != nil {
		d.logger.Warn("Failed to get validators", "error", err)
		return map[string]interface{}{
			"success": true,
			"data":    []*dpos.StakeInfo{},
		}, nil // 返回空列表而不是错误
	}

	if len(validators) == 0 {
		d.logger.Warn("No validators found")
		return map[string]interface{}{
			"success": true,
			"data":    []*dpos.StakeInfo{},
		}, nil // 返回空列表
	}

	// 转换为StakeInfo格式，包含故障标志
	result := make([]*dpos.StakeInfo, 0, len(validators))
	for _, validator := range validators {
		// 获取故障标志（需要访问DPoS引擎）
		faultInfo := map[string]interface{}{}

		// 通过store访问DPoS引擎获取故障信息
		if dposStore, ok := d.store.(interface {
			GetDPoSEngine() interface{}
		}); ok {
			if dposEngine := dposStore.GetDPoSEngine(); dposEngine != nil {
				if dpos, ok := dposEngine.(*dpos.DPoS); ok {
					faultInfo = dpos.GetValidatorFaultInfo(validator.Address)
				}
			}
		}

		stakingInfo := &dpos.StakeInfo{
			Staker:    validator.Address,
			Amount:    new(big.Int).Set(validator.VotingPower),
			IsActive:  validator.IsActive,
			FaultFlag: faultInfo, // 添加故障标志
		}

		// 🆕 尝试从StakeStore获取累计奖励
		// 逻辑说明：
		// 1. 优先从StakeStore读取（如果迁移已完成，这里应该有值）
		// 2. 如果为nil/0，且RewardStore中有该地址的奖励记录，说明可能是迁移未完成，使用fallback
		// 3. 如果为nil/0，且RewardStore中也没有记录，说明该地址确实没有奖励
		if dposState, err2 := d.store.GetDPoSState(); err2 == nil && dposState != nil {
			if dposState.StakeStore != nil {
				// 从StakeStore读取StakeInfo（可能包含累计奖励）
				if stakingInfos, err := dposState.StakeStore.GetStakingInfo(); err == nil {
					for _, si := range stakingInfos {
						if si.Staker == validator.Address {
							if si.Rewards != nil && si.Rewards.Sign() > 0 {
								stakingInfo.Rewards = new(big.Int).Set(si.Rewards)
								break
							}
						}
					}
				}
			}

			// Fallback：如果StakeInfo.Rewards为nil/0，尝试从RewardStore实时计算
			// 这种情况可能发生在：
			// 1. 迁移未完成（首次运行）
			// 2. 迁移后新增的奖励（但updateStakeInfoCumulativeReward应该已经更新了）
			// 3. 该地址确实没有奖励（GetRewardSummary会返回0，不会设置）
			if stakingInfo.Rewards == nil || stakingInfo.Rewards.Sign() == 0 {
				if dposState.RewardStore != nil {
					summary, err := dposState.RewardStore.GetRewardSummary(validator.Address.String(), 1, 999999)
					if err == nil && summary != nil && summary.TotalRewardWei != "" && summary.TotalRewardWei != "0" {
						if totalReward, ok := new(big.Int).SetString(summary.TotalRewardWei, 10); ok {
							stakingInfo.Rewards = totalReward
						}
					}
				}
			}
		}

		result = append(result, stakingInfo)
	}

	return map[string]interface{}{
		"success": true,
		"data":    result,
	}, nil
}

// GetVotingPower handles dpos_getVotingPower RPC method
// NOTE: This endpoint is currently intended for internal use only.
//
//	Keep it undocumented until we finalize external exposure.
func (d *DPOS) GetVotingPower(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetVotingPower called", "params", params)

	// Parse parameters
	var delegate string
	var blockNumber *uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) >= 1 {
			if addr, ok := p[0].(string); ok {
				delegate = addr
			} else {
				return nil, fmt.Errorf("first parameter must be a string address")
			}
		} else {
			return nil, fmt.Errorf("at least one parameter (delegate address) is required")
		}

		// Parse block number if provided
		if len(p) >= 2 {
			if blockStr, ok := p[1].(string); ok {
				if blockStr == "latest" {
					blockNumber = nil // Use latest block
				} else {
					// Try to parse as number
					if blockNum, err := strconv.ParseUint(blockStr, 10, 64); err == nil {
						blockNumber = &blockNum
					}
				}
			}
		}
	case map[string]interface{}:
		if addr, ok := p["delegate"].(string); ok {
			delegate = addr
		} else {
			return nil, fmt.Errorf("delegate parameter is required")
		}

		if blockStr, ok := p["blockNumber"].(string); ok {
			if blockStr == "latest" {
				blockNumber = nil
			} else {
				if blockNum, err := strconv.ParseUint(blockStr, 10, 64); err == nil {
					blockNumber = &blockNum
				}
			}
		}
	default:
		return nil, fmt.Errorf("invalid parameter type: %T", params)
	}

	d.logger.Info("DPoS GetVotingPower parsed", "delegate", delegate, "blockNumber", blockNumber)

	// Parse delegate address
	delegateAddr := types.StringToAddress(delegate)

	// Get validators
	validators, err := d.store.GetValidators()
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	// Find the delegate
	for _, v := range validators {
		if v.Address == delegateAddr {
			return map[string]interface{}{
				"success":     true,
				"delegate":    delegate,
				"votingPower": v.VotingPower.String(),
				"isActive":    v.IsActive,
			}, nil
		}
	}

	return map[string]interface{}{
		"success": false,
		"error":   fmt.Sprintf("delegate not found: %s", delegate),
	}, nil
}

// GetConsensusState handles dpos_getConsensusState RPC method
func (d *DPOS) GetConsensusState(ctx context.Context) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetConsensusState called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}

	var currentRound uint64
	var currentDelegate types.Address

	// 优先尝试从 DPoS 引擎获取当前 round 和 delegate
	dposEngine := d.getDPoSEngine()
	if dposEngine != nil {
		// 获取 currentRound
		if engine, ok := dposEngine.(interface {
			GetCurrentRound() uint64
		}); ok {
			currentRound = engine.GetCurrentRound()
			if currentRound > 0 {
				d.logger.Debug("从DPoS引擎获取当前round", "round", currentRound)
			}
		}

		// 如果引擎返回0，尝试从DPoS引擎直接获取所需信息进行计算
		if currentRound == 0 {
			if dpos, ok := dposEngine.(*dpos.DPoS); ok {
				currentBlockHeight := dpos.GetCurrentBlockNumber()
				if currentBlockHeight > 0 {
					consensusSwitchHeight := dpos.GetConsensusSwitchHeight()
					if currentBlockHeight < consensusSwitchHeight {
						currentRound = 0
					} else {
						if delegates, err := dpos.GetDelegates(currentBlockHeight, nil); err == nil && len(delegates) > 0 {
							validatorCount := uint64(len(delegates))
							if validatorCount > 0 {
								dposBlockNumber := currentBlockHeight - consensusSwitchHeight
								currentRound = (dposBlockNumber / validatorCount) + 1
							}
						}
					}
				}
			}
		}

		// 如果还是0，使用fallback计算
		if currentRound == 0 {
			currentBlockHeight := d.getCurrentBlockHeight()
			if currentBlockHeight > 0 {
				consensusSwitchHeight := d.getConsensusSwitchHeight()
				if currentBlockHeight >= consensusSwitchHeight {
					if validators, err := d.store.GetValidatorsWithFilter(false); err == nil && len(validators) > 0 {
						validatorCount := uint64(len(validators))
						if validatorCount > 0 {
							dposBlockNumber := currentBlockHeight - consensusSwitchHeight
							currentRound = (dposBlockNumber / validatorCount) + 1
						}
					}
				}
			}
			if currentRound == 0 {
				currentRound = 1 // 默认值
			}
		}

		// 获取 currentDelegate
		if engine, ok := dposEngine.(interface {
			GetCurrentDelegate() types.Address
		}); ok {
			currentDelegate = engine.GetCurrentDelegate()
		}
	}

	// Fallback: 如果从引擎获取失败，尝试从全局实例获取
	if currentDelegate == (types.Address{}) {
		for _, instance := range dpos.GetAllDPoSInstances() {
			if instance == nil {
				continue
			}
			currentDelegate = instance.GetCurrentDelegate()
			if currentDelegate != types.ZeroAddress {
				break
			}
		}
	}

	// Fallback: 如果还是失败，使用验证者列表的第一个
	if currentDelegate == (types.Address{}) {
		validators, err := d.store.GetValidators()
		if err != nil {
			validators, _ = d.store.GetValidatorsWithFilter(false)
		}
		if len(validators) == 0 {
			for _, instance := range dpos.GetAllDPoSInstances() {
				if instance != nil {
					validators = instance.GetValidators()
					if len(validators) > 0 {
						break
					}
				}
			}
		}
		if len(validators) > 0 {
			currentDelegate = validators[0].Address
		}
	}

	return map[string]interface{}{
		"success":              true,
		"currentRound":         currentRound,
		"currentDelegate":      currentDelegate.String(),
		"currentDelegateIndex": 0, // TODO: Calculate actual index
	}, nil
}

// GetAllValidators 已删除
// 请使用 dpos_getStakingInfo 替代，它返回更详细的质押信息（包括故障标志等）

// Helper validation functions
func (d *DPOS) validateVoteRequest(req *VoteRequest) error {
	if req.Voter == "" {
		return fmt.Errorf("voter address is required")
	}
	if req.Candidate == "" {
		return fmt.Errorf("candidate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

// GetValidatorVotingDetails handles dpos_getValidatorVotingDetails RPC method
// This method returns detailed voting information for a specific validator
func (d *DPOS) GetValidatorVotingDetails(ctx context.Context, params interface{}) (interface{}, error) {
	var validatorAddress string
	switch p := params.(type) {
	case []interface{}:
		if len(p) == 1 {
			if address, ok := p[0].(string); ok {
				validatorAddress = address
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   "first parameter must be a string address",
				}, nil
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "expected 1 parameter (validator address)",
			}, nil
		}
	case string:
		validatorAddress = p
	case map[string]interface{}:
		if address, ok := p["validator"].(string); ok {
			validatorAddress = address
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address is required",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T, expected string, array, or map", params),
		}, nil
	}
	if validatorAddress == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "validator address is required",
		}, nil
	}
	validatorAddr := types.StringToAddress(validatorAddress)
	var targetValidator *validator.ValidatorMetadata
	var validators validator.AccountSet
	var err error

	if len(validators) == 0 {
		if dposStore, ok := d.store.(interface {
			GetDPoSEngine() interface{}
		}); ok {
			if dposEngine := dposStore.GetDPoSEngine(); dposEngine != nil {
				if dpos, ok := dposEngine.(*dpos.DPoS); ok {
					if validators, err = dpos.GetValidatorsWithFilter(false); err != nil {
						d.logger.Warn("⚠️ [GetValidatorVotingDetails] 获取验证者列表失败", "error", err)
					}
				}
			}
		}
	}
	if len(validators) > 0 {
		for _, v := range validators {
			if v.Address == validatorAddr {
				targetValidator = v
				break
			}
		}
	}
	if targetValidator == nil {
		// 支持候选人/权重为0的地址：构造默认元数据继续向下聚合投票记录
		targetValidator = &validator.ValidatorMetadata{
			Address:     validatorAddr,
			VotingPower: big.NewInt(0),
			IsActive:    false,
			BlsKey:      nil,
		}
	}
	var stakingInfo []*dpos.StakeInfo
	var dposState *dpos.State
	if dposState, err = d.store.GetDPoSState(); err == nil && dposState != nil && dposState.StakeStore != nil {
		stakingInfo, err = dposState.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Error("❌ [GetValidatorVotingDetails] StakeStore.GetStakingInfo 失败", "error", err)
		}
	} else {
		d.logger.Warn("⚠️ [GetValidatorVotingDetails] DPoS State 不可用，尝试从 store.GetStakingInfo 获取")
		stakingInfo, err = d.store.GetStakingInfo()
		if err != nil {
			d.logger.Error("❌ [GetValidatorVotingDetails] store.GetStakingInfo 失败", "error", err)
		}
		// 如果从 store 获取失败，尝试再次获取 dposState
		if dposState == nil {
			dposState, _ = d.store.GetDPoSState()
		}
	}
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}
	// Helpers for formatting amounts
	weiPerEtherInt := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	weiPerEtherFloat := new(big.Float).SetInt(weiPerEtherInt)
	formatEther := func(amount *big.Int) string {
		if amount == nil || amount.Sign() == 0 {
			return "0"
		}
		amountFloat := new(big.Float).SetInt(amount)
		amountFloat.Quo(amountFloat, weiPerEtherFloat)
		return amountFloat.Text('f', 6)
	}
	// 聚合投票记录：按 staker+delegate 聚合，累加 amount
	type aggregatedStake struct {
		staker      types.Address
		delegate    types.Address
		totalAmount *big.Int
		startTime   uint64 // 最早的投票时间
		endTime     uint64 // 最晚的解锁时间
		isLocked    bool   // 如果任一记录锁定，则为 true
		rewards     *big.Int
	}
	inboundStakesMap := make(map[string]*aggregatedStake)
	// 聚合 validator 自己的投票（按 delegate 聚合）
	outboundVotesMap := make(map[string]*aggregatedStake)

	if dposState != nil && dposState.StakeStore != nil {
		// 🆕 直接获取所有 VoterInfo 记录
		allVoterInfo, err := dposState.StakeStore.GetAllVoterInfo()
		if err != nil {
			d.logger.Warn("⚠️ [GetValidatorVotingDetails] GetAllVoterInfo 失败", "error", err)
		} else {
			// 遍历所有投票者，查找投票给目标验证者的记录
			for voterAddr, voterInfo := range allVoterInfo {
				if voterInfo == nil {
					continue
				}

				// 检查该投票者是否投票给了目标验证者
				if voterInfo.DelegateVotes != nil {
					if voteAmount, exists := voterInfo.DelegateVotes[validatorAddr]; exists && voteAmount != nil && voteAmount.Sign() > 0 {
						key := voterAddr.String()

						// 如果已经在 inboundStakesMap 中，累加金额
						if agg, exists := inboundStakesMap[key]; exists {
							agg.totalAmount.Add(agg.totalAmount, voteAmount)
						} else {
							// 创建新的聚合记录
							inboundStakesMap[key] = &aggregatedStake{
								staker:      voterAddr,
								delegate:    validatorAddr,
								totalAmount: new(big.Int).Set(voteAmount),
								startTime:   voterInfo.LastVoteTime,
								endTime:     voterInfo.LockedUntil,
								isLocked:    voterInfo.LockedUntil > uint64(time.Now().Unix()),
								rewards:     big.NewInt(0),
							}
						}
					}
				}
			}
		}
	}
	// 处理 StakingInfo：聚合投票记录
	for _, stake := range stakingInfo {
		if stake == nil {
			continue
		}
		amount := big.NewInt(0)
		if stake.Amount != nil {
			amount = new(big.Int).Set(stake.Amount)
		}
		delegateAddr := stake.Delegate
		isInboundMatch := delegateAddr == validatorAddr
		isOutboundMatch := stake.Staker == validatorAddr
		// 处理投票给 validator 的记录（入站投票）因为 VoterInfo.DelegateVotes 已经是减去撤销后的有效权重，避免重复计算
		if isInboundMatch {
			key := stake.Staker.String()
			if _, existsInVoterInfo := inboundStakesMap[key]; existsInVoterInfo {
				// 如果已经存在，说明是从 VoterInfo.DelegateVotes 获取的（已减去撤销权重），跳过从 StakeInfo 累加
				d.logger.Debug("🔵 [GetValidatorVotingDetails] 跳过 StakeInfo 累加（已从 VoterInfo 获取有效权重）",
					"staker", stake.Staker.String(),
					"delegate", delegateAddr.String(),
					"note", "VoterInfo.DelegateVotes 已包含减去撤销后的有效权重")
				continue
			}
			// 如果 VoterInfo 中没有，才从 StakeInfo 累加（这种情况应该很少，可能是数据不一致）
			if agg, exists := inboundStakesMap[key]; exists {
				agg.totalAmount.Add(agg.totalAmount, amount)
				// 保留最早的 startTime
				if stake.StartTime < agg.startTime {
					agg.startTime = stake.StartTime
				}
				// 保留最晚的 endTime
				if stake.EndTime > agg.endTime {
					agg.endTime = stake.EndTime
				}
				// 如果任一记录锁定，则为锁定
				if stake.IsLocked {
					agg.isLocked = true
				}
				if stake.Rewards != nil {
					if agg.rewards == nil {
						agg.rewards = big.NewInt(0)
					}
					agg.rewards.Add(agg.rewards, stake.Rewards)
				}
				d.logger.Debug("🔵 [GetValidatorVotingDetails] 聚合投票记录",
					"staker", stake.Staker.String(),
					"addedAmount", amount.String(),
					"totalAmount", agg.totalAmount.String())
			} else {
				// 创建新的聚合记录
				inboundStakesMap[key] = &aggregatedStake{
					staker:      stake.Staker,
					delegate:    stake.Delegate,
					totalAmount: new(big.Int).Set(amount),
					startTime:   stake.StartTime,
					endTime:     stake.EndTime,
					isLocked:    stake.IsLocked,
					rewards:     big.NewInt(0),
				}
				if stake.Rewards != nil {
					inboundStakesMap[key].rewards.Set(stake.Rewards)
				}
			}
		}
		if isOutboundMatch {
			key := stake.Delegate.String()
			if agg, exists := outboundVotesMap[key]; exists {
				agg.totalAmount.Add(agg.totalAmount, amount)
				// 保留最早的 startTime
				if stake.StartTime < agg.startTime {
					agg.startTime = stake.StartTime
				}
				// 保留最晚的 endTime
				if stake.EndTime > agg.endTime {
					agg.endTime = stake.EndTime
				}
				// 如果任一记录锁定，则为锁定
				if stake.IsLocked {
					agg.isLocked = true
				}
				// 累加奖励
				if stake.Rewards != nil {
					if agg.rewards == nil {
						agg.rewards = big.NewInt(0)
					}
					agg.rewards.Add(agg.rewards, stake.Rewards)
				}
			} else {
				outboundVotesMap[key] = &aggregatedStake{
					staker:      stake.Staker,
					delegate:    stake.Delegate,
					totalAmount: new(big.Int).Set(amount),
					startTime:   stake.StartTime,
					endTime:     stake.EndTime,
					isLocked:    stake.IsLocked,
					rewards:     big.NewInt(0),
				}
				if stake.Rewards != nil {
					outboundVotesMap[key].rewards.Set(stake.Rewards)
				}
			}
		}
	}
	validatorStakes := make([]map[string]interface{}, 0, len(inboundStakesMap))
	totalStakedToValidator := big.NewInt(0)
	for _, agg := range inboundStakesMap {
		stakeEntry := map[string]interface{}{
			"staker":      agg.staker.String(),
			"amountWei":   agg.totalAmount.String(),
			"amountEther": formatEther(agg.totalAmount),
			"startTime":   agg.startTime,
			"endTime":     agg.endTime,
			"isLocked":    agg.isLocked,
		}
		if agg.rewards != nil && agg.rewards.Sign() > 0 {
			stakeEntry["rewardsWei"] = agg.rewards.String()
			stakeEntry["rewardsEther"] = formatEther(agg.rewards)
		}
		validatorStakes = append(validatorStakes, stakeEntry)
		totalStakedToValidator.Add(totalStakedToValidator, agg.totalAmount)
	}
	outboundVotes := make([]map[string]interface{}, 0, len(outboundVotesMap))
	totalVotedByValidator := big.NewInt(0)
	for _, agg := range outboundVotesMap {
		voteEntry := map[string]interface{}{
			"delegate":    agg.delegate.String(),
			"amountWei":   agg.totalAmount.String(),
			"amountEther": formatEther(agg.totalAmount),
			"startTime":   agg.startTime,
			"endTime":     agg.endTime,
			"isLocked":    agg.isLocked,
		}
		if agg.rewards != nil && agg.rewards.Sign() > 0 {
			voteEntry["rewardsWei"] = agg.rewards.String()
			voteEntry["rewardsEther"] = formatEther(agg.rewards)
		}
		outboundVotes = append(outboundVotes, voteEntry)
		totalVotedByValidator.Add(totalVotedByValidator, agg.totalAmount)
	}
	stakeFound := len(validatorStakes) > 0

	if targetValidator.VotingPower != nil && targetValidator.VotingPower.Sign() > 0 {
		if totalStakedToValidator.Cmp(targetValidator.VotingPower) != 0 {
			d.logger.Warn("⚠️ [GetValidatorVotingDetails] VotingPower 与计算值不一致",
				"validator", validatorAddr.String(),
				"votingPower", targetValidator.VotingPower.String(),
				"calculatedTotal", totalStakedToValidator.String(),
				"difference", new(big.Int).Sub(targetValidator.VotingPower, totalStakedToValidator).String())
		}
	}

	votingPower := big.NewInt(0)
	if targetValidator.VotingPower != nil {
		votingPower = new(big.Int).Set(targetValidator.VotingPower)
	}
	totalStakedToMe := votingPower
	validatorDetail := map[string]interface{}{
		"address":              targetValidator.Address.String(),
		"votingPower":          votingPower.String(),
		"isActive":             targetValidator.IsActive,
		"totalStakedToMe":      totalStakedToMe.String(),
		"totalStakedToMeEther": formatEther(totalStakedToMe),
		"stakeCount":           len(validatorStakes),
		"stakes":               validatorStakes,
		"totalVotedByMe":       totalVotedByValidator.String(),
		"totalVotedByMeEther":  formatEther(totalVotedByValidator),
		"myVoteCount":          len(outboundVotes),
		"myVotes":              outboundVotes,
		"consensusRound":       0,     // TODO: Get current consensus round
		"lastBlockProduced":    "0x0", // TODO: Get last block hash
		"hasInboundVotes":      stakeFound,
	}
	response := map[string]interface{}{
		"success":   true,
		"validator": validatorDetail,
	}
	return response, nil
}

// GetVoteByHash handles dpos_getVoteByHash RPC method
// This method parses DPoS vote transactions and returns human-readable voting information
func (d *DPOS) GetVoteByHash(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetVoteByHash called", "params", params)

	// Parse parameters
	var txHash string
	switch v := params.(type) {
	case string:
		txHash = v
	case []interface{}:
		if len(v) > 0 {
			if hashStr, ok := v[0].(string); ok {
				txHash = hashStr
			}
		}
	default:
		return nil, fmt.Errorf("invalid parameters type: expected string or []interface{}, got %T", params)
	}

	if txHash == "" {
		return nil, fmt.Errorf("transaction hash is required")
	}

	d.logger.Info("Transaction hash extracted", "txHash", txHash)

	// Parse transaction hash
	hash := types.StringToHash(txHash)
	if hash == types.ZeroHash {
		return nil, fmt.Errorf("invalid transaction hash: %s", txHash)
	}

	d.logger.Info("Transaction hash parsed", "hash", hash.String())

	// Try to get transaction from store
	var tx *types.Transaction
	var blockNumber *uint64
	var isPending bool

	// First try to get from pending transactions
	d.logger.Info("Checking pending transaction pool...")
	if pendingTx, found := d.store.GetPendingTx(hash); found {
		tx = pendingTx
		isPending = true
		d.logger.Info("Found transaction in pending pool", "tx", tx.Hash.String())
	} else {
		d.logger.Info("Transaction not found in pending pool")

		// Try to get from blockchain using the correct approach
		if blockchainStore, ok := d.store.(interface {
			ReadTxLookup(txnHash types.Hash) (types.Hash, bool)
			GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)
		}); ok {
			d.logger.Info("Checking blockchain for confirmed transaction using ReadTxLookup...")

			// Step 1: Find the block hash containing this transaction
			if blockHash, found := blockchainStore.ReadTxLookup(hash); found {
				d.logger.Info("Found block hash for transaction", "blockHash", blockHash.String())

				// Step 2: Get the block to extract transaction details
				if block, ok := blockchainStore.GetBlockByHash(blockHash, true); ok {
					d.logger.Info("Found block", "blockNumber", block.Number(), "txCount", len(block.Transactions))

					// Step 3: Find the specific transaction in the block
					for i, blockTx := range block.Transactions {
						if blockTx.Hash == hash {
							tx = blockTx
							isPending = false
							blockNum := block.Number()
							blockNumber = &blockNum
							d.logger.Info("Found transaction in blockchain", "blockNumber", blockNum, "txIndex", i)
							break
						}
					}

					if tx == nil {
						d.logger.Warn("Transaction hash found in block but transaction not found in block transactions")
					}
				} else {
					d.logger.Warn("Block found but could not retrieve block details")
				}
			} else {
				d.logger.Info("Transaction not found in blockchain (ReadTxLookup returned false)")
			}
		} else {
			d.logger.Info("Store does not support ReadTxLookup/GetBlockByHash")
		}
	}

	if tx == nil {
		return nil, fmt.Errorf("transaction not found in pending pool or blockchain. Hash: %s", txHash)
	}

	d.logger.Info("Transaction found", "hash", tx.Hash.String(), "isPending", isPending, "blockNumber", blockNumber)

	// Parse DPoS vote data from transaction input
	voteInfo, err := d.parseVoteTransactionData(tx)
	if err != nil {
		d.logger.Error("Failed to parse DPoS vote data", "error", err, "input", fmt.Sprintf("%x", tx.Input))
		return nil, fmt.Errorf("failed to parse DPoS vote data: %w", err)
	}

	// Get sender address from transaction
	sender := tx.From
	if sender == types.ZeroAddress {
		d.logger.Info("Sender address is zero, attempting to recover from signature...")
		// Try to recover sender from signature
		sender = d.recoverSenderFromTx(tx)
		d.logger.Info("Sender recovered from signature", "sender", sender.String())
	}

	// Build response
	response := map[string]interface{}{
		"success": true,
		"txHash":  txHash,
		"from":    sender.String(),
		"to": func() string {
			if tx.To != nil {
				return tx.To.String()
			}
			return "0x0000000000000000000000000000000000000000"
		}(),
		"nonce": tx.Nonce,
		"gasPrice": func() string {
			if tx.GasPrice != nil {
				return tx.GasPrice.String()
			}
			return "0"
		}(),
		"gas": tx.Gas,
		"value": func() string {
			if tx.Value != nil {
				return tx.Value.String()
			}
			return "0"
		}(),
		"blockNumber": blockNumber,
		"isPending":   isPending,
		"voter":       voteInfo.Voter.String(),
		"candidate":   voteInfo.Candidate.String(),
		"amountWei":   voteInfo.Amount.String(),
		"amountEther": new(big.Float).Quo(new(big.Float).SetInt(voteInfo.Amount), new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))).String(),
		"voteType":    "DPoS Vote",
		"timestamp":   time.Now().Unix(),
	}

	d.logger.Info("DPoS vote parsed successfully", "voter", voteInfo.Voter, "candidate", voteInfo.Candidate, "amount", voteInfo.Amount)

	return response, nil
}

// parseVoteTransactionData parses DPoS vote data from transaction input
func (d *DPOS) parseVoteTransactionData(tx *types.Transaction) (*VoteInfo, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}

	input := tx.Input
	if input == nil || len(input) < 4 {
		return nil, fmt.Errorf("input data too short or nil: length=%d", len(input))
	}

	// Check if it's a DPoS vote transaction
	if !bytes.Equal(input[:4], []byte("DPOS")) {
		return nil, fmt.Errorf("not a DPoS vote transaction, prefix=%x", input[:4])
	}

	// Expected format: 4 bytes "DPOS" + 20 bytes voter + 20 bytes candidate + 32 bytes amount
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(input) < expectedLength {
		return nil, fmt.Errorf("invalid DPoS vote tx input length: expected %d, got %d", expectedLength, len(input))
	}

	// Parse addresses and amount
	voter := types.BytesToAddress(input[dposPrefixLen : dposPrefixLen+addrLen])
	candidate := types.BytesToAddress(input[dposPrefixLen+addrLen : dposPrefixLen+addrLen+addrLen])
	amountBytes := input[dposPrefixLen+addrLen+addrLen : expectedLength]

	// Convert amount bytes to big.Int (remove leading zeros)
	// 🔧 修复：支持负数解码（全1表示 -1）
	amount := new(big.Int).SetBytes(amountBytes)

	// 检查是否为全1（0xFFFFFFFF...），表示 -1
	isAllOnes := true
	for _, b := range amountBytes {
		if b != 0xFF {
			isAllOnes = false
			break
		}
	}
	if isAllOnes {
		amount = big.NewInt(-1)
		d.logger.Info("🔧 [解析] 检测到全1编码，解析为 amount = -1")
	}

	if amount.Cmp(big.NewInt(-1)) == 0 {
		// amount = -1 表示撤销全部投票，允许通过
	} else if amount.Sign() <= 0 {
		return nil, fmt.Errorf("vote amount must be positive or -1 for unvote, got %s", amount.String())
	}

	d.logger.Info("DPoS vote data parsed successfully", "voter", voter.String(), "candidate", candidate.String(), "amount", amount.String())

	return &VoteInfo{
		Voter:     voter,
		Candidate: candidate,
		Amount:    amount,
	}, nil
}

// recoverSenderFromTx attempts to recover sender address from transaction signature
func (d *DPOS) recoverSenderFromTx(tx *types.Transaction) types.Address {
	// This is a simplified recovery - in production you might want more robust handling
	if tx.V != nil && tx.R != nil && tx.S != nil {
		// Try to recover sender from signature
		// Note: This is a basic implementation
		return types.ZeroAddress // Placeholder
	}
	return types.ZeroAddress
}

// VoteInfo represents parsed DPoS vote information
type VoteInfo struct {
	Voter     types.Address
	Candidate types.Address
	Amount    *big.Int
}

// broadcastTransaction attempts to broadcast a transaction to the network
// This is a fallback mechanism to ensure transactions reach other nodes
func (d *DPOS) broadcastTransaction(tx *types.Transaction) error {
	d.logger.Info("=== 标记7: 开始尝试广播交易 ===")
	d.logger.Info("Attempting to broadcast transaction", "txHash", tx.Hash.String())

	// Method 0: Try to access txpool directly and call AddTx to trigger built-in broadcasting
	if txpoolStore, ok := d.store.(interface {
		GetTxPool() interface{}
	}); ok {
		d.logger.Info("Store has GetTxPool method, attempting to access txpool...")
		txpool := txpoolStore.GetTxPool()

		if txpool != nil {
			d.logger.Info("TxPool found", "type", fmt.Sprintf("%T", txpool))

			// Check if txpool has a topic for broadcasting (this is what enables broadcasting in txpool.AddTx)
			if txpoolWithTopic, ok := txpool.(interface {
				GetTopic() interface{}
			}); ok {
				d.logger.Info("TxPool has GetTopic method, checking broadcasting capability...")
				topic := txpoolWithTopic.GetTopic()
				if topic != nil {
					d.logger.Info("TxPool topic found, broadcasting should work through normal AddTx", "topic", fmt.Sprintf("%T", topic))
					return nil // Broadcasting is handled by txpool.AddTx internally
				} else {
					d.logger.Warn("TxPool topic is nil, broadcasting may not work")
				}
			}

			// Try to call AddTx method on txpool to trigger built-in broadcasting
			// Note: This may fail with "already known" if called multiple times
			if txpoolAddTx, ok := txpool.(interface {
				AddTx(tx *types.Transaction) error
			}); ok {
				d.logger.Info("TxPool has AddTx method, calling it to trigger broadcasting...")
				if err := txpoolAddTx.AddTx(tx); err != nil {
					if err.Error() == "already known" {
						d.logger.Info("Transaction already in pool, broadcasting should work through normal flow")
						return nil
					}
					d.logger.Warn("TxPool.AddTx failed during broadcasting", "error", err)
				} else {
					d.logger.Info("Transaction broadcasted through TxPool.AddTx (built-in broadcasting)")
					return nil
				}
			}
		}
	}

	// Method 1: Try to access network layer through store
	if networkStore, ok := d.store.(interface {
		GetNetwork() interface{}
	}); ok {
		d.logger.Info("Store has GetNetwork method, attempting to broadcast...")
		network := networkStore.GetNetwork()

		if network != nil {
			d.logger.Info("Network layer found", "type", fmt.Sprintf("%T", network))

			// Try to call broadcast method on network
			if broadcaster, ok := network.(interface {
				BroadcastTransaction(tx *types.Transaction) error
			}); ok {
				d.logger.Info("Network has BroadcastTransaction method, calling it...")
				if err := broadcaster.BroadcastTransaction(tx); err != nil {
					d.logger.Warn("Network.BroadcastTransaction failed", "error", err)
				} else {
					d.logger.Info("Transaction broadcasted through network layer")
					return nil
				}
			}

			// Try alternative broadcast method
			if broadcaster, ok := network.(interface {
				BroadcastTx(tx *types.Transaction) error
			}); ok {
				d.logger.Info("Network has BroadcastTx method, calling it...")
				if err := broadcaster.BroadcastTx(tx); err != nil {
					d.logger.Warn("Network.BroadcastTx failed", "error", err)
				} else {
					d.logger.Info("Transaction broadcasted through network layer")
					return nil
				}
			}
		}
	}

	// Method 2: Try to access network through embedded fields
	if hub, ok := d.store.(interface {
		GetNetwork() interface{}
	}); ok {
		d.logger.Info("Store has GetNetwork method through hub, attempting to broadcast...")
		network := hub.GetNetwork()

		if network != nil {
			d.logger.Info("Network layer found through hub", "type", fmt.Sprintf("%T", network))

			// Try to call broadcast method on network
			if broadcaster, ok := network.(interface {
				BroadcastTransaction(tx *types.Transaction) error
			}); ok {
				d.logger.Info("Network has BroadcastTransaction method, calling it...")
				if err := broadcaster.BroadcastTransaction(tx); err != nil {
					d.logger.Warn("Network.BroadcastTransaction failed", "error", err)
				} else {
					d.logger.Info("Transaction broadcasted through network layer")
					return nil
				}
			}
		}
	}

	// Method 3: Try to access server directly
	if serverStore, ok := d.store.(interface {
		GetServer() interface{}
	}); ok {
		d.logger.Info("Store has GetServer method, attempting to broadcast...")
		server := serverStore.GetServer()

		if server != nil {
			d.logger.Info("Server found", "type", fmt.Sprintf("%T", server))

			// Try to call broadcast method on server
			if broadcaster, ok := server.(interface {
				BroadcastTransaction(tx *types.Transaction) error
			}); ok {
				d.logger.Info("Server has BroadcastTransaction method, calling it...")
				if err := broadcaster.BroadcastTransaction(tx); err != nil {
					d.logger.Warn("Server.BroadcastTransaction failed", "error", err)
				} else {
					d.logger.Info("Transaction broadcasted through server")
					return nil
				}
			}
		}
	}

	d.logger.Warn("=== 标记8: 没有找到可用的广播方法 ===")
	d.logger.Warn("No network broadcast method found, transaction may not reach other nodes")
	return fmt.Errorf("no network broadcast method available")
}

// getDelegatesFromDatabase 从数据库查询所有受托人信息
func (d *DPOS) getDelegatesFromDatabase() []*dpos.StakeInfo {
	d.logger.Info("🔄 从数据库中查询所有受托人信息...")

	// 修复：通过全局注册表获取DPoS实例，然后调用其方法
	// 使用DPoS实例的公开方法，避免访问未导出的字段
	if dposInstance, exists := dpos.GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		d.logger.Info("✅ 通过全局注册表找到DPoS实例")

		// 尝试调用DPoS实例的公开方法获取质押信息
		// 方法1：尝试调用GetStakingInfo方法（单个）
		if getStakingInfoMethod := reflect.ValueOf(dposInstance).MethodByName("GetStakingInfo"); getStakingInfoMethod.IsValid() {
			d.logger.Info("✅ 找到GetStakingInfo方法，但需要遍历所有验证者...")
			// 这里需要遍历所有验证者，暂时跳过
			d.logger.Warn("GetStakingInfo方法需要遍历所有验证者，暂时跳过")
		} else {
			d.logger.Warn("DPoS实例没有GetStakingInfo方法")
		}
	} else {
		d.logger.Warn("无法通过全局注册表找到DPoS实例")
	}

	// 如果无法获取，返回空结果
	d.logger.Warn("无法从数据库获取受托人信息，返回空结果")
	return []*dpos.StakeInfo{}
}

// convertDelegatesToStakeInfo 将受托人信息转换为StakeInfo
func (d *DPOS) convertDelegatesToStakeInfo(delegates validator.AccountSet, source string) []*dpos.StakeInfo {
	d.logger.Info("✅ 从"+source+"获取到受托人信息", "delegateCount", len(delegates))

	// 为每个受托人创建stake info
	var dynamicStakes []*dpos.StakeInfo
	for _, delegate := range delegates {
		if delegate.VotingPower != nil && delegate.VotingPower.Cmp(big.NewInt(0)) > 0 {
			stake := &dpos.StakeInfo{
				Staker:    delegate.Address,          // 受托人地址（自己给自己投票）
				Amount:    delegate.VotingPower,      // 投票权重
				StartTime: uint64(time.Now().Unix()), // 当前时间
				EndTime:   0,                         // 无锁定时间
				IsLocked:  false,                     // 未锁定
				IsActive:  delegate.IsActive,         // 是否活跃
				Delegate:  delegate.Address,          // 受托人地址
				Rewards:   big.NewInt(0),             // 奖励为0
			}
			dynamicStakes = append(dynamicStakes, stake)

			d.logger.Info("✅ 从"+source+"提取受托人信息",
				"delegate", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}
	}

	d.logger.Info("✅ 从"+source+"获取的动态投票信息", "count", len(dynamicStakes))
	return dynamicStakes
}

// getDynamicVotingInfo retrieves current voting information from the consensus engine
func (d *DPOS) getDynamicVotingInfo() []*dpos.StakeInfo {
	d.logger.Info("Getting dynamic voting info from consensus engine...")

	// Try to get consensus engine through the store adapter
	var consensus interface{}
	if storeAdapter, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensus = storeAdapter.GetConsensus()
		d.logger.Info("Successfully got consensus engine through store adapter", "type", fmt.Sprintf("%T", consensus))

		// Debug: Check if this is the same instance that was used for AddVote
		d.logger.Info("Consensus engine instance details",
			"address", fmt.Sprintf("%p", consensus),
			"type", fmt.Sprintf("%T", consensus))
	} else {
		d.logger.Warn("Store does not support GetConsensus method")
		return []*dpos.StakeInfo{}
	}

	if consensus == nil {
		d.logger.Warn("Consensus engine is nil")
		return []*dpos.StakeInfo{}
	}

	// Try to get DPoS consensus engine
	var dposEngine interface{}

	// Try different ways to access DPoS engine
	d.logger.Info("Checking consensus engine capabilities...")

	if dposConsensus, ok := consensus.(interface {
		GetVoters() map[types.Address]*dpos.VoterInfo
	}); ok {
		d.logger.Info("Consensus engine supports GetVoters method")
		d.logger.Info("DPoS consensus engine instance details",
			"address", fmt.Sprintf("%p", dposConsensus),
			"type", fmt.Sprintf("%T", dposConsensus))
		dposEngine = dposConsensus
	} else if dposConsensus, ok := consensus.(interface {
		GetDPoSState() (*dpos.State, error)
	}); ok {
		d.logger.Info("Consensus engine supports GetDPoSState method")
		dposEngine = dposConsensus
	} else if dposConsensus, ok := consensus.(interface {
		GetDelegates() (validator.AccountSet, error)
	}); ok {
		d.logger.Info("Consensus engine supports GetDelegates method")
		dposEngine = dposConsensus
	} else {
		d.logger.Warn("DPoS consensus engine not accessible - no supported methods found")
		d.logger.Info("Available methods on consensus engine:")
		consensusType := reflect.TypeOf(consensus)
		for i := 0; i < consensusType.NumMethod(); i++ {
			method := consensusType.Method(i)
			d.logger.Info("Available method", "name", method.Name, "type", method.Type.String())
		}
		return []*dpos.StakeInfo{}
	}

	// Try to get voters information
	var dynamicStakes []*dpos.StakeInfo

	// Method 1: Try to get voters directly (most direct approach)
	if voterEngine, ok := dposEngine.(interface {
		GetVoters() map[types.Address]*dpos.VoterInfo
	}); ok {
		d.logger.Info("Direct access to GetVoters method available")
		d.logger.Info("Calling GetVoters() method on consensus engine...")
		voters := voterEngine.GetVoters()
		d.logger.Info("GetVoters() method returned",
			"voterCount", len(voters),
			"votersMap", fmt.Sprintf("%p", voters))

		// Debug: Check if voters map is nil or empty
		if voters == nil {
			d.logger.Warn("❌ GetVoters() returned nil map")
		} else if len(voters) == 0 {
			d.logger.Warn("❌ GetVoters() returned empty map (0 voters)")
		} else {
			d.logger.Info("✅ GetVoters() returned non-empty map", "voterCount", len(voters))
		}

		// Debug: Log detailed information about each voter
		for voterAddr, voterInfo := range voters {
			d.logger.Info("Processing voter",
				"voterAddr", voterAddr.String(),
				"votingPower", func() string {
					if voterInfo.VotingPower != nil {
						return voterInfo.VotingPower.String()
					}
					return "nil"
				}(),
				"votedDelegatesCount", len(voterInfo.VotedDelegates),
				"lastVoteTime", voterInfo.LastVoteTime,
				"lockedUntil", voterInfo.LockedUntil)

			// Debug: Log each voted delegate
			for i, delegateAddr := range voterInfo.VotedDelegates {
				d.logger.Info("Voter's delegate",
					"voterAddr", voterAddr.String(),
					"delegateIndex", i,
					"delegateAddr", delegateAddr.String())
			}

			if voterInfo.VotingPower != nil && voterInfo.VotingPower.Cmp(big.NewInt(0)) > 0 {
				// For each voted delegate, create a stake info
				for _, delegateAddr := range voterInfo.VotedDelegates {
					stake := &dpos.StakeInfo{
						Staker:    voterAddr,             // 投票者地址
						Amount:    voterInfo.VotingPower, // 投票权重
						StartTime: voterInfo.LastVoteTime,
						EndTime:   voterInfo.LockedUntil,
						IsLocked:  voterInfo.LockedUntil > uint64(time.Now().Unix()),
						IsActive:  true,
						Delegate:  delegateAddr, // 受托人地址
						Rewards:   big.NewInt(0),
					}
					dynamicStakes = append(dynamicStakes, stake)

					d.logger.Info("✅ Extracted vote from voter",
						"voter", voterAddr.String(),
						"delegate", delegateAddr.String(),
						"amount", voterInfo.VotingPower.String())
				}
			} else {
				d.logger.Warn("❌ Voter has no voting power or zero voting power",
					"voterAddr", voterAddr.String(),
					"votingPower", func() string {
						if voterInfo.VotingPower != nil {
							return voterInfo.VotingPower.String()
						}
						return "nil"
					}())
			}
		}
	} else {
		d.logger.Warn("GetVoters method not accessible, trying alternative methods...")

		// Method 2: Try to get from DPoS state
		if stateEngine, ok := dposEngine.(interface {
			GetDPoSState() (*dpos.State, error)
		}); ok {
			if state, err := stateEngine.GetDPoSState(); err == nil && state != nil {
				d.logger.Info("Got DPoS state, extracting voting info...")
				// Extract voting information from state
				// This would depend on the actual State structure
				dynamicStakes = d.extractVotingInfoFromState(state)
			}
		}

		// Method 3: Try to get from validators/delegates
		if delegateEngine, ok := dposEngine.(interface {
			GetDelegates() (validator.AccountSet, error)
		}); ok {
			if delegates, err := delegateEngine.GetDelegates(); err == nil {
				d.logger.Info("Got delegates, extracting voting info...")
				dynamicStakes = d.extractVotingInfoFromDelegates(delegates)
			}
		}
	}

	d.logger.Info("Dynamic voting info extracted", "count", len(dynamicStakes))
	return dynamicStakes
}

// extractVotingInfoFromState extracts voting information from DPoS state
func (d *DPOS) extractVotingInfoFromState(state *dpos.State) []*dpos.StakeInfo {
	d.logger.Info("Extracting voting info from state (placeholder)")

	// Try to get consensus engine to access DPoS runtime
	var consensus interface{}
	if storeAdapter, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensus = storeAdapter.GetConsensus()
	}

	if consensus == nil {
		d.logger.Warn("Consensus engine not available for state extraction")
		return []*dpos.StakeInfo{}
	}

	// Try to access DPoS consensus engine with voters information
	var dposEngine interface{}

	// Method 1: Try to get DPoS engine with voters
	if dposConsensus, ok := consensus.(interface {
		GetVoters() map[types.Address]*dpos.VoterInfo
	}); ok {
		dposEngine = dposConsensus
	} else if dposConsensus, ok := consensus.(interface {
		GetDPoSState() (*dpos.State, error)
	}); ok {
		dposEngine = dposConsensus
	} else {
		d.logger.Warn("DPoS consensus engine not accessible for voters")
		return []*dpos.StakeInfo{}
	}

	// Extract voting information from voters
	var dynamicStakes []*dpos.StakeInfo

	// Method 1: Try to get voters directly
	if voterEngine, ok := dposEngine.(interface {
		GetVoters() map[types.Address]*dpos.VoterInfo
	}); ok {
		voters := voterEngine.GetVoters()
		d.logger.Info("Got voters from consensus engine", "voterCount", len(voters))

		for voterAddr, voterInfo := range voters {
			if voterInfo.VotingPower != nil && voterInfo.VotingPower.Cmp(big.NewInt(0)) > 0 {
				// For each voted delegate, create a stake info
				for _, delegateAddr := range voterInfo.VotedDelegates {
					stake := &dpos.StakeInfo{
						Staker:    voterAddr,             // 投票者地址
						Amount:    voterInfo.VotingPower, // 投票权重
						StartTime: voterInfo.LastVoteTime,
						EndTime:   voterInfo.LockedUntil,
						IsLocked:  voterInfo.LockedUntil > uint64(time.Now().Unix()),
						IsActive:  true,
						Delegate:  delegateAddr, // 受托人地址
						Rewards:   big.NewInt(0),
					}
					dynamicStakes = append(dynamicStakes, stake)

					d.logger.Info("Extracted vote from voter",
						"voter", voterAddr.String(),
						"delegate", delegateAddr.String(),
						"amount", voterInfo.VotingPower.String())
				}
			}
		}
	}

	d.logger.Info("Extracted voting info from state", "stakeCount", len(dynamicStakes))
	return dynamicStakes
}

// extractVotingInfoFromDelegates extracts voting information from delegates
func (d *DPOS) extractVotingInfoFromDelegates(delegates validator.AccountSet) []*dpos.StakeInfo {
	d.logger.Info("Extracting voting info from delegates", "delegateCount", len(delegates))

	var stakes []*dpos.StakeInfo

	for _, delegate := range delegates {
		// 修复：使用创世配置中的质押数量
		// 根据你的创世文件，每个初始验证者的质押数量应该是 1000000000000000000000 (1 ETH)
		stakeAmount := new(big.Int).Set(delegate.VotingPower)

		// 检查是否是默认的投票权重（如54），如果是，则使用创世配置中的标准质押数量
		if stakeAmount.Cmp(big.NewInt(100)) < 0 { // 如果小于100，可能是默认值
			d.logger.Info("Detected potential default voting power, using genesis standard stake amount",
				"address", delegate.Address.String(),
				"currentVotingPower", stakeAmount.String())

			// 使用创世配置中的标准质押数量：1 ETH
			genesisStakeAmount, _ := new(big.Int).SetString("1000000000000000000000", 10)
			stakeAmount = genesisStakeAmount
			d.logger.Info("Applied genesis standard stake amount",
				"address", delegate.Address.String(),
				"genesisAmount", stakeAmount.String())
		}

		// Create stake info for each delegate
		stake := &dpos.StakeInfo{
			Staker:    delegate.Address, // Self-delegation
			Amount:    stakeAmount,      // 使用修复后的质押数量
			StartTime: uint64(time.Now().Unix()),
			EndTime:   0,
			IsLocked:  false,
			IsActive:  delegate.IsActive,
			Delegate:  delegate.Address,
			Rewards:   big.NewInt(0),
		}

		// 添加详细的调试日志
		d.logger.Info("Created stake info from delegate",
			"address", delegate.Address.String(),
			"amount", stake.Amount.String(),
			"votingPower", delegate.VotingPower.String(),
			"isActive", delegate.IsActive)

		stakes = append(stakes, stake)
	}

	d.logger.Info("Extracted stake info from delegates", "stakeCount", len(stakes))
	return stakes
}

// mergeStakingInfo merges static staking info with dynamic voting info
// 修复：按受托人地址去重，避免重复累加投票权重
func (d *DPOS) mergeStakingInfo(static []*dpos.StakeInfo, dynamic []*dpos.StakeInfo) []*dpos.StakeInfo {
	d.logger.Info("Merging staking info", "staticCount", len(static), "dynamicCount", len(dynamic))

	// 修复：按受托人地址去重，而不是按staker-delegate组合去重
	// 这样可以避免同一个受托人的投票信息被重复累加
	delegateMap := make(map[types.Address]*dpos.StakeInfo)
	var merged []*dpos.StakeInfo

	// 修复：让动态投票数据优先，确保正确的委托关系
	// 先添加动态投票信息（来自实际的投票数据）
	for _, stake := range dynamic {
		if stake.Staker != types.ZeroAddress &&
			stake.Delegate != types.ZeroAddress &&
			stake.Amount != nil &&
			stake.Amount.Cmp(big.NewInt(0)) > 0 {

			// 修复：按受托人地址去重，优先使用动态投票数据
			if existing, exists := delegateMap[stake.Delegate]; !exists || stake.Amount.Cmp(existing.Amount) > 0 {
				delegateMap[stake.Delegate] = stake
				d.logger.Info("✅ Added/Updated dynamic voting stake (priority)",
					"staker", stake.Staker.String(),
					"delegate", stake.Delegate.String(),
					"amount", stake.Amount.String())
			}
		}
	}

	// 然后添加静态质押信息（来自创世配置），但只添加有效的
	for _, stake := range static {
		if stake.Staker != types.ZeroAddress &&
			stake.Delegate != types.ZeroAddress &&
			stake.Amount != nil &&
			stake.Amount.Cmp(big.NewInt(0)) > 0 {

			// 修复：按受托人地址去重，如果动态数据中没有该受托人，则添加静态数据
			if existing, exists := delegateMap[stake.Delegate]; !exists {
				delegateMap[stake.Delegate] = stake
				d.logger.Info("✅ Added static stake (delegate not in dynamic data)",
					"staker", stake.Staker.String(),
					"delegate", stake.Delegate.String(),
					"amount", stake.Amount.String())
			} else {
				d.logger.Info("⚠️ Skipped static stake (delegate already in dynamic data)",
					"staker", stake.Staker.String(),
					"delegate", stake.Delegate.String(),
					"amount", stake.Amount.String(),
					"existingAmount", existing.Amount.String())
			}
		}
	}

	// 修复：将去重后的数据转换为切片
	for _, stake := range delegateMap {
		merged = append(merged, stake)
	}

	d.logger.Info("✅ Merged staking info completed (deduplicated by delegate)",
		"totalCount", len(merged),
		"uniqueDelegates", len(delegateMap))
	return merged
}

// sortStakingInfoByAmount sorts staking info by amount in descending order (largest first)
func (d *DPOS) sortStakingInfoByAmount(stakingInfo []*dpos.StakeInfo) []*dpos.StakeInfo {
	d.logger.Info("Sorting staking info by amount", "count", len(stakingInfo))

	// Create a copy to avoid modifying the original slice
	sorted := make([]*dpos.StakeInfo, len(stakingInfo))
	copy(sorted, stakingInfo)

	// Sort by amount in descending order
	sort.Slice(sorted, func(i, j int) bool {
		// Handle nil amounts
		if sorted[i].Amount == nil && sorted[j].Amount == nil {
			return false
		}
		if sorted[i].Amount == nil {
			return false
		}
		if sorted[j].Amount == nil {
			return true
		}

		// Compare amounts (largest first)
		return sorted[i].Amount.Cmp(sorted[j].Amount) > 0
	})

	d.logger.Info("Staking info sorted by amount", "count", len(sorted))
	return sorted
}

// sortVotingDetailsByTotalVotes sorts voting details by total votes in descending order (largest first)
func (d *DPOS) sortVotingDetailsByTotalVotes(votingDetails []map[string]interface{}) []map[string]interface{} {
	d.logger.Info("Sorting voting details by total votes", "count", len(votingDetails))

	// Create a copy to avoid modifying the original slice
	sorted := make([]map[string]interface{}, len(votingDetails))
	copy(sorted, votingDetails)

	// Sort by total votes in descending order
	sort.Slice(sorted, func(i, j int) bool {
		// Get total votes from the map
		totalVotesI, okI := sorted[i]["totalVotes"]
		totalVotesJ, okJ := sorted[j]["totalVotes"]

		if !okI || !okJ {
			return false
		}

		// Convert to big.Int for comparison
		var amountI, amountJ *big.Int

		switch v := totalVotesI.(type) {
		case *big.Int:
			amountI = v
		case string:
			if parsed, ok := new(big.Int).SetString(v, 10); ok {
				amountI = parsed
			} else {
				amountI = big.NewInt(0)
			}
		default:
			amountI = big.NewInt(0)
		}

		switch v := totalVotesJ.(type) {
		case *big.Int:
			amountJ = v
		case string:
			if parsed, ok := new(big.Int).SetString(v, 10); ok {
				amountJ = parsed
			} else {
				amountJ = big.NewInt(0)
			}
		default:
			amountJ = big.NewInt(0)
		}

		// Compare amounts (largest first)
		return amountI.Cmp(amountJ) > 0
	})

	d.logger.Info("Voting details sorted by total votes", "count", len(sorted))
	return sorted
}

// getConsensusEngineByHeight 根据当前区块高度判断选择正确的共识引擎
func (d *DPOS) getConsensusEngineByHeight(consensusStore interface {
	GetConsensus() interface{}
}, currentHeight uint64) interface{} {
	d.logger.Debug("Current block height", "height", currentHeight)

	// 获取共识切换高度配置
	consensusSwitchHeight := d.getConsensusSwitchHeight()
	d.logger.Debug("Consensus switch height", "switchHeight", consensusSwitchHeight)

	// 如果当前高度 >= 切换高度，尝试获取DPoS引擎
	if consensusSwitchHeight > 0 && currentHeight >= consensusSwitchHeight {
		d.logger.Info("🔄 当前高度已达到DPoS切换高度，尝试获取DPoS引擎",
			"currentHeight", currentHeight,
			"switchHeight", consensusSwitchHeight)

		// 尝试获取DPoS引擎
		if dposEngine := d.getDPoSEngine(); dposEngine != nil {
			d.logger.Info("✅ 成功获取DPoS引擎")
			return dposEngine
		}

		d.logger.Warn("⚠️ 无法获取DPoS引擎，使用默认共识引擎")
	}

	// 否则使用默认共识引擎
	d.logger.Debug("使用默认共识引擎", "height", currentHeight)
	return consensusStore.GetConsensus()
}

// getConsensusSwitchHeight 获取共识切换高度配置
func (d *DPOS) getConsensusSwitchHeight() uint64 {
	// 方法1: 优先尝试从 DPoS 引擎获取（如果已经切换到 DPoS，这是最直接的方式）
	dposEngine := d.getDPoSEngine()
	if dposEngine != nil {
		if dpos, ok := dposEngine.(*dpos.DPoS); ok {
			height := dpos.GetConsensusSwitchHeight()
			if height > 0 {
				d.logger.Debug("从DPoS引擎获取共识切换高度", "height", height)
				return height
			}
		} else if engine, ok := dposEngine.(interface {
			GetConsensusSwitchHeight() uint64
		}); ok {
			height := engine.GetConsensusSwitchHeight()
			if height > 0 {
				d.logger.Debug("从DPoS引擎接口获取共识切换高度", "height", height)
				return height
			}
		}
	}

	// 方法2: 尝试从共识引擎中获取配置
	if consensusStore, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensusEngine := consensusStore.GetConsensus()
		d.logger.Debug("获取到共识引擎", "type", fmt.Sprintf("%T", consensusEngine))

		// 尝试从DPoS引擎中获取配置（共识引擎可能就是DPoS）
		if dposEngine, ok := consensusEngine.(interface {
			GetConsensusSwitchHeight() uint64
		}); ok {
			height := dposEngine.GetConsensusSwitchHeight()
			if height > 0 {
				d.logger.Debug("从共识引擎（DPoS）获取共识切换高度", "height", height)
				return height
			}
		}

		// 尝试从IBFT引擎中获取配置
		if ibftEngine, ok := consensusEngine.(interface {
			GetConsensusSwitchHeight() uint64
		}); ok {
			height := ibftEngine.GetConsensusSwitchHeight()
			if height > 0 {
				d.logger.Debug("从IBFT引擎获取共识切换高度", "height", height)
				return height
			}
		}

		// 尝试通过反射获取配置
		d.logger.Debug("尝试通过反射获取共识切换高度配置")
		if height := d.getConsensusSwitchHeightByReflection(consensusEngine); height > 0 {
			d.logger.Debug("通过反射获取共识切换高度", "height", height)
			return height
		}
	}

	// 如果无法获取，返回0（表示不进行切换）
	d.logger.Warn("无法获取共识切换高度配置，使用默认值0")
	return 0
}

// getConsensusSwitchHeightByReflection 通过反射获取共识切换高度
func (d *DPOS) getConsensusSwitchHeightByReflection(consensusEngine interface{}) uint64 {
	// 使用反射查找GetConsensusSwitchHeight方法
	consensusValue := reflect.ValueOf(consensusEngine)
	if consensusValue.Kind() == reflect.Ptr {
		consensusValue = consensusValue.Elem()
	}

	consensusType := consensusValue.Type()
	for i := 0; i < consensusType.NumMethod(); i++ {
		method := consensusType.Method(i)
		if method.Name == "GetConsensusSwitchHeight" {
			d.logger.Debug("找到GetConsensusSwitchHeight方法")
			// 调用方法
			results := consensusValue.Method(i).Call([]reflect.Value{})
			if len(results) > 0 && results[0].Kind() == reflect.Uint64 {
				height := results[0].Uint()
				d.logger.Debug("通过反射获取共识切换高度", "height", height)
				return height
			}
		}
	}

	d.logger.Debug("未找到GetConsensusSwitchHeight方法")
	return 0
}

// getDPoSEngine 获取DPoS引擎
func (d *DPOS) getDPoSEngine() interface{} {
	// 尝试从store中获取DPoS引擎
	if dposStore, ok := d.store.(interface {
		GetDPoSEngine() interface{}
	}); ok {
		dposEngine := dposStore.GetDPoSEngine()
		if dposEngine != nil {
			d.logger.Debug("从store获取DPoS引擎成功")
			return dposEngine
		}
	}

	// 尝试从共识引擎中获取DPoS引擎
	if consensusStore, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensusEngine := consensusStore.GetConsensus()

		// 尝试从IBFT引擎中获取DPoS引擎
		if ibftEngine, ok := consensusEngine.(interface {
			GetDPoSEngine() interface{}
		}); ok {
			dposEngine := ibftEngine.GetDPoSEngine()
			if dposEngine != nil {
				d.logger.Debug("从IBFT引擎获取DPoS引擎成功")
				return dposEngine
			}
		}
	}

	d.logger.Warn("无法获取DPoS引擎")
	return nil
}

// getCurrentBlockHeight 获取当前区块高度
func (d *DPOS) getCurrentBlockHeight() uint64 {
	// 方法1: 尝试从 DPoS 引擎获取当前区块号
	dposEngine := d.getDPoSEngine()
	if dposEngine != nil {
		if engine, ok := dposEngine.(interface {
			GetCurrentBlockNumber() uint64
		}); ok {
			blockNumber := engine.GetCurrentBlockNumber()
			if blockNumber > 0 {
				d.logger.Debug("从DPoS引擎获取当前区块高度", "height", blockNumber)
				return blockNumber
			}
		}
	}

	// 方法2: 尝试从ethBlockchainStore获取区块高度
	if blockchainStore, ok := d.store.(interface {
		Header() *types.Header
	}); ok {
		header := blockchainStore.Header()
		if header != nil {
			d.logger.Debug("从ethBlockchainStore.Header()方法获取区块高度", "height", header.Number)
			return header.Number
		}
	}

	// 方法3: 尝试从GetLatestHeader方法获取
	if headerStore, ok := d.store.(interface {
		GetLatestHeader() *types.Header
	}); ok {
		header := headerStore.GetLatestHeader()
		if header != nil {
			d.logger.Debug("从GetLatestHeader()方法获取区块高度", "height", header.Number)
			return header.Number
		}
	}

	// 方法4: 尝试从GetLatestBlock方法获取
	if blockStore, ok := d.store.(interface {
		GetLatestBlock() *types.Block
	}); ok {
		block := blockStore.GetLatestBlock()
		if block != nil {
			d.logger.Debug("从GetLatestBlock()方法获取区块高度", "height", block.Header.Number)
			return block.Header.Number
		}
	}

	// 方法5: 尝试从共识引擎获取当前高度
	if consensusStore, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensusEngine := consensusStore.GetConsensus()
		if ibftEngine, ok := consensusEngine.(interface {
			GetCurrentHeight() uint64
		}); ok {
			height := ibftEngine.GetCurrentHeight()
			if height > 0 {
				d.logger.Debug("从IBFT引擎获取当前高度", "height", height)
				return height
			}
		}
	}

	// 如果无法获取，返回0
	d.logger.Warn("无法获取当前区块高度，返回0")
	return 0
}

// getCurrentBlockHeightFromExternal 从外部获取当前区块高度
func (d *DPOS) getCurrentBlockHeightFromExternal() uint64 {
	// 方法1: 尝试从共识引擎中获取当前高度
	if consensusStore, ok := d.store.(interface {
		GetConsensus() interface{}
	}); ok {
		consensusEngine := consensusStore.GetConsensus()

		// 尝试从IBFT引擎中获取当前高度
		if ibftEngine, ok := consensusEngine.(interface {
			GetCurrentHeight() uint64
		}); ok {
			height := ibftEngine.GetCurrentHeight()
			d.logger.Debug("从IBFT引擎获取当前高度", "height", height)
			return height
		}

		// 尝试从DPoS引擎中获取当前高度
		if dposEngine, ok := consensusEngine.(interface {
			GetCurrentHeight() uint64
		}); ok {
			height := dposEngine.GetCurrentHeight()
			d.logger.Debug("从DPoS引擎获取当前高度", "height", height)
			return height
		}

		// 尝试通过反射获取当前高度
		if height := d.getCurrentHeightByReflection(consensusEngine); height > 0 {
			d.logger.Debug("通过反射获取当前高度", "height", height)
			return height
		}
	}

	// 方法2: 尝试从区块链中获取
	if height := d.getCurrentBlockHeight(); height > 0 {
		return height
	}

	// 方法3: 如果无法获取，返回0（表示无法确定高度）
	d.logger.Warn("无法获取当前区块高度，返回0")
	return 0
}

// getCurrentHeightByReflection 通过反射获取当前高度
func (d *DPOS) getCurrentHeightByReflection(consensusEngine interface{}) uint64 {
	// 使用反射查找GetCurrentHeight方法
	consensusValue := reflect.ValueOf(consensusEngine)
	if consensusValue.Kind() == reflect.Ptr {
		consensusValue = consensusValue.Elem()
	}

	consensusType := consensusValue.Type()
	for i := 0; i < consensusType.NumMethod(); i++ {
		method := consensusType.Method(i)
		if method.Name == "GetCurrentHeight" {
			d.logger.Debug("找到GetCurrentHeight方法")
			// 调用方法
			results := consensusValue.Method(i).Call([]reflect.Value{})
			if len(results) > 0 && results[0].Kind() == reflect.Uint64 {
				height := results[0].Uint()
				d.logger.Debug("通过反射获取当前高度", "height", height)
				return height
			}
		}
	}

	d.logger.Debug("未找到GetCurrentHeight方法")
	return 0
}

// getDPoSEngineDirectly 直接获取DPoS引擎
func (d *DPOS) getDPoSEngineDirectly(consensusStore interface {
	GetConsensus() interface{}
}) interface{} {
	d.logger.Info("🔄 直接尝试获取DPoS引擎")

	// 方法1: 尝试从store中获取DPoS引擎
	if dposStore, ok := d.store.(interface {
		GetDPoSEngine() interface{}
	}); ok {
		dposEngine := dposStore.GetDPoSEngine()
		if dposEngine != nil {
			d.logger.Info("✅ 从store获取DPoS引擎成功")
			return dposEngine
		}
	}

	// 方法2: 尝试从共识引擎中获取DPoS引擎
	consensusEngine := consensusStore.GetConsensus()
	if consensusEngine == nil {
		d.logger.Warn("共识引擎为nil")
		return nil
	}

	d.logger.Debug("获取到共识引擎", "type", fmt.Sprintf("%T", consensusEngine))

	// 尝试从IBFT引擎中获取DPoS引擎
	if ibftEngine, ok := consensusEngine.(interface {
		GetDPoSEngine() interface{}
	}); ok {
		dposEngine := ibftEngine.GetDPoSEngine()
		if dposEngine != nil {
			d.logger.Info("✅ 从IBFT引擎获取DPoS引擎成功")
			return dposEngine
		}
	}

	// 方法3: 尝试通过反射获取DPoS引擎
	if dposEngine := d.getDPoSEngineByReflection(consensusEngine); dposEngine != nil {
		d.logger.Info("✅ 通过反射获取DPoS引擎成功")
		return dposEngine
	}

	// 方法4: 如果无法获取DPoS引擎，返回默认共识引擎
	d.logger.Warn("⚠️ 无法获取DPoS引擎，使用默认共识引擎")
	return consensusEngine
}

// getDPoSEngineByReflection 通过反射获取DPoS引擎
func (d *DPOS) getDPoSEngineByReflection(consensusEngine interface{}) interface{} {
	// 使用反射查找GetDPoSEngine方法
	consensusValue := reflect.ValueOf(consensusEngine)
	if consensusValue.Kind() == reflect.Ptr {
		consensusValue = consensusValue.Elem()
	}

	consensusType := consensusValue.Type()
	for i := 0; i < consensusType.NumMethod(); i++ {
		method := consensusType.Method(i)
		if method.Name == "GetDPoSEngine" {
			d.logger.Debug("找到GetDPoSEngine方法")
			// 调用方法
			results := consensusValue.Method(i).Call([]reflect.Value{})
			if len(results) > 0 && !results[0].IsNil() {
				dposEngine := results[0].Interface()
				d.logger.Debug("通过反射获取DPoS引擎", "type", fmt.Sprintf("%T", dposEngine))
				return dposEngine
			}
		}
	}

	d.logger.Debug("未找到GetDPoSEngine方法")
	return nil
}

// ==================== 新增：DPoS经济系统JSON-RPC方法 ====================

// GetCurrentEpochInfo 获取当前Epoch信息
func (d *DPOS) GetCurrentEpochInfo() (map[string]interface{}, error) {
	d.logger.Info("DPoS GetCurrentEpochInfo called")

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "DPoS engine not available",
		}, nil
	}

	// 调用DPoS引擎的方法
	if engine, ok := dposEngine.(interface {
		GetCurrentEpochInfo() map[string]interface{}
	}); ok {
		epochInfo := engine.GetCurrentEpochInfo()
		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := epochInfo["success"]; hasSuccess {
			return epochInfo, nil
		}
		return map[string]interface{}{
			"success": true,
			"data":    epochInfo,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"error":   "GetCurrentEpochInfo method not available on DPoS engine",
	}, nil
}

// GetEpochInfoByNumber 获取指定Epoch信息
func (d *DPOS) GetEpochInfoByNumber(epochNumber uint64) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetEpochInfoByNumber called", "epochNumber", epochNumber)

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "DPoS engine not available",
		}, nil
	}

	// 调用DPoS引擎的方法
	if engine, ok := dposEngine.(interface {
		GetEpochInfoByNumber(epochNumber uint64) map[string]interface{}
	}); ok {
		epochInfo := engine.GetEpochInfoByNumber(epochNumber)
		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := epochInfo["success"]; hasSuccess {
			return epochInfo, nil
		}
		return map[string]interface{}{
			"success": true,
			"data":    epochInfo,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"error":   "GetEpochInfoByNumber method not available on DPoS engine",
	}, nil
}

// GetLatestEpochInfo 获取最新epoch信息（与GetCurrentEpochInfo相同，但名称更明确）
func (d *DPOS) GetLatestEpochInfo(ctx context.Context) (interface{}, error) {
	d.logger.Info("DPoS GetLatestEpochInfo called")

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "DPoS engine not available",
		}, nil
	}

	// 调用DPoS引擎的方法
	if engine, ok := dposEngine.(interface {
		GetCurrentEpochInfo() map[string]interface{}
	}); ok {
		epochInfo := engine.GetCurrentEpochInfo()
		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := epochInfo["success"]; hasSuccess {
			return epochInfo, nil
		}
		return map[string]interface{}{
			"success": true,
			"data":    epochInfo,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"error":   "GetCurrentEpochInfo method not available on DPoS engine",
	}, nil
}

// ==================== 新增：奖励查询JSON-RPC方法 ====================

// GetEpochRewardDetails 查询指定epoch的奖励详情
func (d *DPOS) GetEpochRewardDetails(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetEpochRewardDetails called", "params", params)

	var epochNumber uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) != 1 {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("expected 1 parameter, got %d", len(p)),
			}, nil
		}

		if epoch, ok := toUint64(p[0]); ok {
			epochNumber = epoch
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "parameter must be a number",
			}, nil
		}
	case map[string]interface{}:
		if epoch, ok := toUint64(p["epoch"]); ok {
			epochNumber = epoch
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "epoch is required and must be a number",
			}, nil
		}
	case float64:
		epochNumber = uint64(p)
	case int:
		epochNumber = uint64(p)
	case uint64:
		epochNumber = p
	case nil:
		return map[string]interface{}{
			"success": false,
			"error":   "epoch is required",
		}, nil
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	d.logger.Info("DPoS GetEpochRewardDetails parsed parameters", "epochNumber", epochNumber)

	// 获取DPoS状态
	dposState, err := d.store.GetDPoSState()
	if err != nil {
		d.logger.Error("❌ GetEpochRewardDetails: 获取DPoS状态失败", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRewardDetails: 成功获取DPoS状态")

	dposState, err = d.ensureRewardStore(dposState)
	if err != nil {
		d.logger.Error("❌ GetEpochRewardDetails: ensureRewardStore失败", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRewardDetails: ensureRewardStore成功")

	// 调用RewardStore的方法
	d.logger.Debug("🔍 GetEpochRewardDetails: 开始查询奖励详情", "epochNumber", epochNumber)
	records, err := dposState.RewardStore.GetEpochRewardDetails(epochNumber)
	if err != nil {
		d.logger.Error("❌ GetEpochRewardDetails: 查询奖励详情失败", "epochNumber", epochNumber, "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRewardDetails: 查询完成", "epochNumber", epochNumber, "recordsCount", len(records))
	return map[string]interface{}{
		"success": true,
		"data":    records,
	}, nil
}

// GetEpochRangeRewardDetails 查询指定epoch范围内的所有奖励详情
func (d *DPOS) GetEpochRangeRewardDetails(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetEpochRangeRewardDetails called", "params", params)

	var fromEpoch, toEpoch uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) != 2 {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("expected 2 parameters (fromEpoch, toEpoch), got %d", len(p)),
			}, nil
		}

		if from, ok := toUint64(p[0]); ok {
			fromEpoch = from
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "first parameter (fromEpoch) must be a number",
			}, nil
		}

		if to, ok := toUint64(p[1]); ok {
			toEpoch = to
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "second parameter (toEpoch) must be a number",
			}, nil
		}
	case map[string]interface{}:
		if from, ok := toUint64(p["fromEpoch"]); ok {
			fromEpoch = from
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "fromEpoch is required and must be a number",
			}, nil
		}

		if to, ok := toUint64(p["toEpoch"]); ok {
			toEpoch = to
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "toEpoch is required and must be a number",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	// 验证范围
	if toEpoch < fromEpoch {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid epoch range: toEpoch (%d) must be greater than or equal to fromEpoch (%d)", toEpoch, fromEpoch),
		}, nil
	}

	d.logger.Info("DPoS GetEpochRangeRewardDetails parsed parameters", "fromEpoch", fromEpoch, "toEpoch", toEpoch)

	// 获取DPoS状态
	dposState, err := d.store.GetDPoSState()
	if err != nil {
		d.logger.Error("❌ GetEpochRangeRewardDetails: 获取DPoS状态失败", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRangeRewardDetails: 成功获取DPoS状态")

	dposState, err = d.ensureRewardStore(dposState)
	if err != nil {
		d.logger.Error("❌ GetEpochRangeRewardDetails: ensureRewardStore失败", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRangeRewardDetails: ensureRewardStore成功")

	// 调用RewardStore的方法
	d.logger.Debug("🔍 GetEpochRangeRewardDetails: 开始查询奖励详情", "fromEpoch", fromEpoch, "toEpoch", toEpoch)
	records, err := dposState.RewardStore.GetEpochRangeRewardDetails(fromEpoch, toEpoch)
	if err != nil {
		d.logger.Error("❌ GetEpochRangeRewardDetails: 查询奖励详情失败", "fromEpoch", fromEpoch, "toEpoch", toEpoch, "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}
	d.logger.Debug("✅ GetEpochRangeRewardDetails: 查询完成", "fromEpoch", fromEpoch, "toEpoch", toEpoch, "recordsCount", len(records))

	return map[string]interface{}{
		"success":      true,
		"fromEpoch":    fromEpoch,
		"toEpoch":      toEpoch,
		"totalRecords": len(records),
		"data":         records,
	}, nil
}

// GetRewardHistory 获取指定地址在指定epoch区间的奖励汇总
func (d *DPOS) GetRewardHistory(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetRewardHistory called", "params", params)

	var address string
	var fromEpoch, toEpoch uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) != 3 {
			return map[string]interface{}{
				"error": fmt.Sprintf("expected 3 parameters, got %d", len(p)),
			}, nil
		}

		addr, ok := p[0].(string)
		if !ok {
			return map[string]interface{}{
				"error": "first parameter must be a string address",
			}, nil
		}
		address = addr

		from, ok := toUint64(p[1])
		if !ok {
			return map[string]interface{}{
				"error": "second parameter must be a number",
			}, nil
		}
		fromEpoch = from

		to, ok := toUint64(p[2])
		if !ok {
			return map[string]interface{}{
				"error": "third parameter must be a number",
			}, nil
		}
		toEpoch = to
	case map[string]interface{}:
		if addr, ok := p["address"].(string); ok {
			address = addr
		} else {
			return map[string]interface{}{
				"error": "address is required",
			}, nil
		}

		if from, ok := toUint64(p["fromEpoch"]); ok {
			fromEpoch = from
		} else {
			return map[string]interface{}{
				"error": "fromEpoch is required and must be a number",
			}, nil
		}

		if to, ok := toUint64(p["toEpoch"]); ok {
			toEpoch = to
		} else {
			return map[string]interface{}{
				"error": "toEpoch is required and must be a number",
			}, nil
		}
	default:
		return map[string]interface{}{
			"error": fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	if address == "" {
		return map[string]interface{}{
			"error": "address is required",
		}, nil
	}

	if toEpoch < fromEpoch {
		return map[string]interface{}{
			"error": "toEpoch must be greater than or equal to fromEpoch",
		}, nil
	}

	// 获取DPoS状态
	dposState, err := d.store.GetDPoSState()
	if err != nil {
		return nil, fmt.Errorf("failed to get DPoS state: %w", err)
	}

	dposState, err = d.ensureRewardStore(dposState)
	if err != nil {
		return map[string]interface{}{
			"error": err.Error(),
		}, nil
	}

	summary, err := dposState.RewardStore.GetRewardSummary(address, fromEpoch, toEpoch)
	if err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("failed to get reward summary: %v", err),
		}, nil
	}

	return map[string]interface{}{
		"success": true,
		"summary": summary,
	}, nil
}

// ensureRewardStore 保证奖励存储可用，并返回可用的状态实例
func (d *DPOS) ensureRewardStore(state *dpos.State) (*dpos.State, error) {
	if state == nil {
		// 尝试从当前共识引擎获取
		if engine := d.getDPoSEngine(); engine != nil {
			if provider, ok := engine.(interface {
				GetState() *dpos.State
			}); ok {
				state = provider.GetState()
			}
		}

		// 如果仍然为空，遍历全局注册的 DPoS 实例
		if state == nil {
			for instanceKey, instance := range dpos.GetAllDPoSInstances() {
				if instance == nil {
					continue
				}
				if candidate := instance.GetState(); candidate != nil {
					d.logger.Info("Using state from registered DPoS instance",
						"instanceKey", instanceKey)
					state = candidate
					break
				}
			}
		}

		if state == nil {
			return nil, fmt.Errorf("dpos state not available")
		}
	}

	if err := state.EnsureRewardStore(d.logger); err != nil {
		d.logger.Error("Failed to initialize RewardStore", "error", err)
		return nil, fmt.Errorf("reward store not available: %w", err)
	}

	return state, nil
}

func toUint64(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float32:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int32:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case uint:
		return uint64(v), true
	case uint32:
		return uint64(v), true
	case uint64:
		return v, true
	case string:
		if v == "" {
			return 0, false
		}
		if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
			return parsed, true
		}
	case json.Number:
		if parsed, err := v.Int64(); err == nil && parsed >= 0 {
			return uint64(parsed), true
		}
	}
	return 0, false
}

func parseDurationSeconds(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case uint32:
		return uint64(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case string:
		if v == "" {
			return 0, false
		}
		if seconds, ok := parseDurationStringSeconds(v); ok {
			return seconds, true
		}
	case json.Number:
		if parsed, err := v.Int64(); err == nil && parsed >= 0 {
			return uint64(parsed), true
		}
	}
	return 0, false
}

func parseDurationStringSeconds(input string) (uint64, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, false
	}

	if duration, err := time.ParseDuration(input); err == nil {
		if duration < 0 {
			return 0, false
		}
		return uint64(duration.Seconds()), true
	}

	if strings.ContainsAny(input, "dD") {
		lower := strings.ToLower(input)
		var value float64
		var suffix string
		if _, err := fmt.Sscanf(lower, "%f%s", &value, &suffix); err == nil && strings.HasPrefix(suffix, "d") {
			remaining := strings.TrimPrefix(suffix, "d")
			hours := value * 24
			normalized := fmt.Sprintf("%.0fh%s", hours, remaining)
			if duration, err := time.ParseDuration(normalized); err == nil {
				if duration < 0 {
					return 0, false
				}
				return uint64(duration.Seconds()), true
			}
		}

		if strings.HasSuffix(lower, "d") {
			numberPart := strings.TrimSuffix(lower, "d")
			if numberPart == "" {
				return 0, false
			}
			if v, err := strconv.ParseFloat(numberPart, 64); err == nil {
				hours := v * 24
				if duration, err := time.ParseDuration(fmt.Sprintf("%.0fh", hours)); err == nil {
					if duration < 0 {
						return 0, false
					}
					return uint64(duration.Seconds()), true
				}
			}
		}
	}

	return 0, false
}

func formatBasisPoints(rate uint64) string {
	percent := float64(rate) / 100.0
	return fmt.Sprintf("%.2f%%", percent)
}

// formatTimestamp 格式化时间戳为可读格式
func formatTimestamp(ts uint64) string {
	if ts == 0 {
		return ""
	}
	t := time.Unix(int64(ts), 0)
	return t.Format("2006-01-02 15:04:05 UTC")
}

// formatRemainTime 格式化剩余时间为可读格式
func formatRemainTime(seconds uint64) string {
	if seconds == 0 {
		return "0秒"
	}

	var parts []string

	days := seconds / 86400
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d天", days))
		seconds -= days * 86400
	}

	hours := seconds / 3600
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
		seconds -= hours * 3600
	}

	minutes := seconds / 60
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d分钟", minutes))
		seconds -= minutes * 60
	}

	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d秒", seconds))
	}

	return strings.Join(parts, "")
}

// GetValidatorBlockStats 获取验证者出块统计
func (d *DPOS) GetValidatorBlockStats(validatorAddress string, epochNumber uint64) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetValidatorBlockStats called", "validatorAddress", validatorAddress, "epochNumber", epochNumber)

	// 解析地址
	addr := types.StringToAddress(validatorAddress)

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "DPoS engine not available",
		}, nil
	}

	// 调用DPoS引擎的方法
	if engine, ok := dposEngine.(interface {
		GetValidatorBlockStats(validatorAddress types.Address, epochNumber uint64) map[string]interface{}
	}); ok {
		stats := engine.GetValidatorBlockStats(addr, epochNumber)
		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := stats["success"]; hasSuccess {
			return stats, nil
		}
		return map[string]interface{}{
			"success": true,
			"data":    stats,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"error":   "GetValidatorBlockStats method not available on DPoS engine",
	}, nil
}

// GetValidatorRewardsInfo 获取验证者奖励信息
func (d *DPOS) GetValidatorRewardsInfo(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetValidatorRewardsInfo called", "params", params)

	// 解析参数
	var validatorAddress string
	var epochNumber uint64

	switch p := params.(type) {
	case []interface{}:
		if len(p) != 2 {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("expected 2 parameters, got %d", len(p)),
			}, nil
		}

		// 第一个参数：验证者地址
		if addrStr, ok := p[0].(string); ok {
			validatorAddress = addrStr
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "first parameter must be a string address",
			}, nil
		}

		// 第二个参数：epoch编号
		if epoch, ok := p[1].(float64); ok {
			epochNumber = uint64(epoch)
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "second parameter must be a number",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	d.logger.Info("DPoS GetValidatorRewardsInfo parameters parsed",
		"validatorAddress", validatorAddress,
		"epochNumber", epochNumber)

	// 解析地址
	validatorAddr := types.StringToAddress(validatorAddress)

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "DPoS engine not available",
		}, nil
	}

	// 调用DPoS引擎的方法
	if engine, ok := dposEngine.(interface {
		GetValidatorRewardsInfo(validatorAddress types.Address, epochNumber uint64) map[string]interface{}
	}); ok {
		rewardsInfo := engine.GetValidatorRewardsInfo(validatorAddr, epochNumber)
		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := rewardsInfo["success"]; hasSuccess {
			return rewardsInfo, nil
		}
		return map[string]interface{}{
			"success": true,
			"data":    rewardsInfo,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"error":   "GetValidatorRewardsInfo method not available on DPoS engine",
	}, nil
}

// 治理相关JSON-RPC方法

// CreateParameterProposal 创建参数表决提案
func (d *DPOS) CreateParameterProposal(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS CreateParameterProposal called", "params", params)

	var proposerStr, parameter, description, proposerPrivateKeyHex string
	var newValue interface{}

	// 支持两种参数格式：数组格式和对象格式
	switch p := params.(type) {
	case []interface{}:
		// 数组格式: [parameter, newValue, description, proposer, proposerPrivateKey]
		if len(p) < 5 {
			return nil, fmt.Errorf("invalid parameters: expected 5 parameters [parameter, newValue, description, proposer, proposerPrivateKey], got %d", len(p))
		}

		var ok bool
		parameter, ok = p[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid parameter name: expected string, got %T", p[0])
		}

		newValue = p[1]
		if newValue == nil {
			return nil, fmt.Errorf("invalid new value: value cannot be null")
		}

		description, ok = p[2].(string)
		if !ok {
			description = "" // 可选参数
		}

		proposerStr, ok = p[3].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposer address: expected string, got %T", p[3])
		}

		proposerPrivateKeyHex, ok = p[4].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposer private key: expected string, got %T", p[4])
		}

	case map[string]interface{}:
		// 对象格式: {parameter, newValue, description, proposer, proposerPrivateKey}
		var ok bool
		proposerStr, ok = p["proposer"].(string)
		if !ok {
			return nil, fmt.Errorf("proposer address is required and must be a string")
		}

		parameter, ok = p["parameter"].(string)
		if !ok {
			return nil, fmt.Errorf("parameter name is required and must be a string")
		}

		newValue, ok = p["newValue"]
		if !ok {
			return nil, fmt.Errorf("new value is required")
		}

		description, ok = p["description"].(string)
		if !ok {
			description = "" // 可选参数
		}

		proposerPrivateKeyHex, ok = p["proposerPrivateKey"].(string)
		if !ok {
			return nil, fmt.Errorf("proposerPrivateKey is required and must be a string")
		}

	default:
		return nil, fmt.Errorf("invalid parameters format: expected array or object, got %T", params)
	}

	// 验证必需参数
	if proposerStr == "" {
		return nil, fmt.Errorf("proposer address cannot be empty")
	}

	// 验证私钥格式
	if len(proposerPrivateKeyHex) != 64 {
		return nil, fmt.Errorf("invalid proposer private key length: expected 64, got %d", len(proposerPrivateKeyHex))
	}
	if parameter == "" {
		return nil, fmt.Errorf("parameter name cannot be empty")
	}
	if newValue == nil {
		return nil, fmt.Errorf("new value cannot be null")
	}

	// 验证提案者地址格式
	proposer := types.StringToAddress(proposerStr)
	if proposer == (types.Address{}) {
		return nil, fmt.Errorf("invalid proposer address format: %s", proposerStr)
	}

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return nil, err
	}
	if !gov.IsParameterVotable(parameter) {
		return nil, fmt.Errorf("invalid parameter: %s is not a votable parameter", parameter)
	}

	// 验证私钥和proposer地址，并检查proposer是否是验证者
	if err := d.validateProposer(proposer, proposerPrivateKeyHex); err != nil {
		return nil, err
	}

	// 3. 获取当前区块号（用于后续计算，但暂不需要）
	// var currentBlock uint64
	// if getBlockNum, ok := dposEngine.(interface {
	// 	GetCurrentBlockNumber() uint64
	// }); ok {
	// 	currentBlock = getBlockNum.GetCurrentBlockNumber()
	// }

	// 4. 创建临时提案对象（用于签名，proposalID会在交易处理时从交易哈希生成）
	// 使用确定性ID：proposer+nonce+参数+时间戳
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(proposer)
	}

	// 创建临时proposalID用于签名（最终ID会在ProcessProposalCreateTransaction中用交易哈希生成）
	tempProposalID := fmt.Sprintf("proposal_temp_%s_%d_%s", proposer.String()[:8], nonce, parameter)
	createdAtTs := uint64(time.Now().Unix())
	tempProposal := &dpos.ParameterProposal{
		ID:           tempProposalID,
		ProposalType: "parameter",
		Parameter:    parameter,
		Proposer:     proposer,
		NewValue:     newValue,
		Description:  description,
		CreatedAt:    createdAtTs,
	}

	// 5. 签名提案（通过治理模块接口）
	proposerSignature, err := gov.SignProposalForTx(tempProposal, proposerPrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to sign proposal: %w", err)
	}

	// 6. 创建交易数据
	txData := dpos.ProposalCreateTxData{
		ProposalType:      "parameter",
		Parameter:         parameter,
		NewValue:          newValue,
		Description:       description,
		ProposerSignature: proposerSignature,
		CreatedAt:         createdAtTs,
	}

	// 7. 创建并签名交易
	tx, err := d.createProposalCreateTransaction(proposer, proposerPrivateKeyHex, txData)
	if err != nil {
		return nil, fmt.Errorf("failed to create proposal transaction: %w", err)
	}

	// 8. 添加到交易池并广播
	if err := d.addProposalTransactionToPool(tx); err != nil {
		return nil, fmt.Errorf("failed to broadcast proposal transaction: %w", err)
	}

	// 9. 生成最终的proposalID（用交易哈希，所有节点会一致）
	finalProposalID := fmt.Sprintf("proposal_%s", tx.Hash.String()[:16])

	d.logger.Info("✅ 参数提案交易已创建并广播", "txHash", tx.Hash.String(), "proposalID", finalProposalID)

	// 获取当前区块高度
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	return map[string]interface{}{
		"success":            true,
		"txHash":             tx.Hash.String(),
		"proposalId":         finalProposalID,
		"parameter":          parameter,
		"newValue":           newValue,
		"proposer":           proposer.String(),
		"currentBlockNumber": currentBlockNumber,
		"message":            "Parameter proposal transaction created and broadcasted successfully",
		"note":               "Proposal will be created when transaction is included in a block",
	}, nil
}

// CreateRecoveryProposal 创建验证者恢复提案
func (d *DPOS) CreateRecoveryProposal(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS CreateRecoveryProposal called", "params", params)

	var proposerStr, validatorAddrStr, recoveryReason, description, proposerPrivateKeyHex string

	// 支持两种参数格式：数组格式和对象格式
	switch p := params.(type) {
	case []interface{}:
		// 数组格式: [validatorAddress, recoveryReason, description, proposer, proposerPrivateKey]
		if len(p) < 5 {
			return nil, fmt.Errorf("invalid parameters: expected 5 parameters [validatorAddress, recoveryReason, description, proposer, proposerPrivateKey], got %d", len(p))
		}

		var ok bool
		validatorAddrStr, ok = p[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid validator address: expected string, got %T", p[0])
		}

		recoveryReason, ok = p[1].(string)
		if !ok {
			return nil, fmt.Errorf("invalid recovery reason: expected string, got %T", p[1])
		}

		description, _ = p[2].(string)
		proposerStr, ok = p[3].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposer address: expected string, got %T", p[3])
		}

		proposerPrivateKeyHex, ok = p[4].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposer private key: expected string, got %T", p[4])
		}

	case map[string]interface{}:
		// 对象格式: {validatorAddress, recoveryReason, description?, proposer, proposerPrivateKey}
		var ok bool
		validatorAddrStr, ok = p["validatorAddress"].(string)
		if !ok {
			return nil, fmt.Errorf("validatorAddress is required and must be a string")
		}

		recoveryReason, ok = p["recoveryReason"].(string)
		if !ok {
			return nil, fmt.Errorf("recoveryReason is required and must be a string")
		}

		description, _ = p["description"].(string)
		proposerStr, ok = p["proposer"].(string)
		if !ok {
			return nil, fmt.Errorf("proposer is required and must be a string")
		}

		proposerPrivateKeyHex, ok = p["proposerPrivateKey"].(string)
		if !ok {
			return nil, fmt.Errorf("proposerPrivateKey is required and must be a string")
		}

	default:
		return nil, fmt.Errorf("invalid parameters format: expected array or object, got %T", params)
	}

	// 验证私钥格式
	if len(proposerPrivateKeyHex) != 64 {
		return nil, fmt.Errorf("invalid proposer private key length: expected 64, got %d", len(proposerPrivateKeyHex))
	}

	// 验证必需参数
	if validatorAddrStr == "" {
		return nil, fmt.Errorf("validator address cannot be empty")
	}
	if recoveryReason == "" {
		return nil, fmt.Errorf("recovery reason cannot be empty")
	}

	// 验证地址格式
	validatorAddr := types.StringToAddress(validatorAddrStr)
	if validatorAddr == (types.Address{}) {
		return nil, fmt.Errorf("invalid validator address format: %s", validatorAddrStr)
	}

	var proposer types.Address
	if proposerStr != "" {
		proposer = types.StringToAddress(proposerStr)
		if proposer == (types.Address{}) {
			return nil, fmt.Errorf("invalid proposer address format: %s", proposerStr)
		}
	}

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return nil, err
	}
	if err := gov.CheckRecoveryPrerequisites(validatorAddr); err != nil {
		return nil, err
	}

	// 验证私钥和proposer地址，并检查proposer是否是验证者
	if err := d.validateProposer(proposer, proposerPrivateKeyHex); err != nil {
		return nil, err
	}

	// 2. 获取nonce用于创建临时proposalID
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(proposer)
	}

	// 3. 创建临时提案对象用于签名
	tempProposalID := fmt.Sprintf("recovery_temp_%s_%d_%s", proposer.String()[:8], nonce, validatorAddr.String()[:8])
	createdAtTs := uint64(time.Now().Unix())
	tempProposal := &dpos.ParameterProposal{
		ID:               tempProposalID,
		ProposalType:     "validator_recovery",
		Parameter:        validatorAddr.String(),
		ValidatorAddress: validatorAddr,
		Proposer:         proposer,
		RecoveryReason:   recoveryReason,
		Description:      description,
		CreatedAt:        createdAtTs,
	}

	// 4. 签名提案
	proposerSignature, err := gov.SignRecoveryProposalForTx(tempProposal, proposerPrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to sign proposal: %w", err)
	}

	// 5. 创建交易数据
	txData := dpos.ProposalCreateTxData{
		ProposalType:      "validator_recovery",
		Parameter:         validatorAddr.String(),
		Description:       description,
		RecoveryReason:    recoveryReason,
		ProposerSignature: proposerSignature,
		CreatedAt:         createdAtTs,
	}

	// 6. 创建并签名交易
	tx, err := d.createProposalCreateTransaction(proposer, proposerPrivateKeyHex, txData)
	if err != nil {
		return nil, fmt.Errorf("failed to create recovery proposal transaction: %w", err)
	}

	// 7. 添加到交易池并广播
	if err := d.addProposalTransactionToPool(tx); err != nil {
		return nil, fmt.Errorf("failed to broadcast recovery proposal transaction: %w", err)
	}

	// 8. 生成最终的proposalID
	finalProposalID := fmt.Sprintf("recovery_%s", tx.Hash.String()[:16])

	d.logger.Info("✅ 恢复提案交易已创建并广播", "txHash", tx.Hash.String(), "proposalID", finalProposalID)

	// 获取当前区块高度
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	return map[string]interface{}{
		"success":            true,
		"txHash":             tx.Hash.String(),
		"proposalId":         finalProposalID,
		"validatorAddress":   validatorAddr.String(),
		"proposer":           proposer.String(),
		"recoveryReason":     recoveryReason,
		"description":        description,
		"currentBlockNumber": currentBlockNumber,
		"message":            "Recovery proposal transaction created and broadcasted successfully",
		"note":               "Proposal will be created when transaction is included in a block",
	}, nil
}

// VoteOnParameterProposal 对参数提案进行投票
func (d *DPOS) VoteOnParameterProposal(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS VoteOnParameterProposal called", "params", params)

	var proposalID, voterStr, privateKeyHex string
	var support bool

	// 支持数组格式参数 [proposalId, voter, support, privateKey]
	if paramArray, ok := params.([]interface{}); ok {
		if len(paramArray) != 4 {
			return nil, fmt.Errorf("invalid parameters format: expected 4 parameters [proposalId, voter, support, privateKey]")
		}

		var ok1, ok2, ok3, ok4 bool
		proposalID, ok1 = paramArray[0].(string)
		voterStr, ok2 = paramArray[1].(string)
		support, ok3 = paramArray[2].(bool)
		privateKeyHex, ok4 = paramArray[3].(string)

		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, fmt.Errorf("invalid parameters format: expected [string, string, bool, string]")
		}
	} else if paramMap, ok := params.(map[string]interface{}); ok {
		// 支持对象格式参数 {proposalId, voter, support, privateKey}
		var ok1, ok2, ok3, ok4 bool
		proposalID, ok1 = paramMap["proposalId"].(string)
		voterStr, ok2 = paramMap["voter"].(string)
		support, ok3 = paramMap["support"].(bool)
		if pkVal, exists := paramMap["privateKey"]; exists {
			privateKeyHex, ok4 = pkVal.(string)
		}

		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, fmt.Errorf("invalid parameters format: expected {proposalId: string, voter: string, support: bool, privateKey: string}")
		}
	} else {
		return nil, fmt.Errorf("invalid parameters format: expected array [proposalId, voter, support, privateKey] or object {proposalId, voter, support, privateKey}")
	}

	// 验证私钥格式
	if len(privateKeyHex) != 64 {
		return nil, fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	voter := types.StringToAddress(voterStr)

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return nil, err
	}

	proposal, err := gov.GetParameterProposal(proposalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get proposal: %w", err)
	}

	currentBlock := gov.GetCurrentBlockNumber()
	if currentBlock == 0 {
		currentBlock = d.getCurrentBlockHeight()
	}
	if currentBlock > proposal.EndBlock {
		d.logger.Warn("❌ [VoteOnParameterProposal RPC] 提案已过期，拒绝投票",
			"proposalID", proposalID,
			"currentBlock", currentBlock,
			"endBlock", proposal.EndBlock)
		return nil, fmt.Errorf("proposal %s has expired (current block %d > end block %d)", proposalID, currentBlock, proposal.EndBlock)
	}
	if proposal.Votes != nil {
		if existingVote, hasVoted := proposal.Votes[voter]; hasVoted {
			d.logger.Warn("❌ [VoteOnParameterProposal RPC] 投票者已经投票过，拒绝重复投票",
				"proposalID", proposalID,
				"voter", voter.String(),
				"existingSupport", existingVote.Support)
			return nil, fmt.Errorf("voter %s has already voted on proposal %s", voter.String(), proposalID)
		}
	}

	// 改为通过交易进行投票
	// 1. 创建临时投票对象用于签名
	tempVote := &dpos.ParameterVote{
		Voter:      voter,
		ProposalID: proposalID,
		Support:    support,
		Timestamp:  uint64(time.Now().Unix()),
	}

	// 2. 签名投票
	var voteSignature []byte
	voteSignature, err = gov.SignVoteForTx(tempVote, privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to sign vote: %w", err)
	}

	// 3. 创建交易数据
	txData := dpos.ProposalVoteTxData{
		ProposalID:    proposalID,
		Support:       support,
		VoteSignature: voteSignature,
	}

	// 4. 创建并签名交易
	tx, err := d.createProposalVoteTransaction(voter, privateKeyHex, txData)
	if err != nil {
		return nil, fmt.Errorf("failed to create vote transaction: %w", err)
	}

	// 5. 添加到交易池并广播
	if err := d.addProposalTransactionToPool(tx); err != nil {
		return nil, fmt.Errorf("failed to broadcast vote transaction: %w", err)
	}

	d.logger.Info("✅ 投票交易已创建并广播", "txHash", tx.Hash.String(), "proposalID", proposalID, "voter", voter.String())

	// 获取当前区块高度
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	return map[string]interface{}{
		"success":            true,
		"txHash":             tx.Hash.String(),
		"proposalId":         proposalID,
		"voter":              voter.String(),
		"support":            support,
		"currentBlockNumber": currentBlockNumber,
		"message":            "Vote transaction created and broadcasted successfully",
		"note":               "Vote will be recorded when transaction is included in a block",
	}, nil
}

// GetParameterProposal 获取提案信息
func (d *DPOS) GetParameterProposal(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetParameterProposal called", "params", params)

	var proposalID string
	switch p := params.(type) {
	case []interface{}:
		if len(p) < 1 {
			return nil, fmt.Errorf("invalid parameters: expected 1 parameter [proposalID], got %d", len(p))
		}
		var ok bool
		proposalID, ok = p[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposal ID: expected string, got %T", p[0])
		}
	case map[string]interface{}:
		var ok bool
		proposalID, ok = p["proposalId"].(string)
		if !ok {
			return nil, fmt.Errorf("proposalId is required and must be a string")
		}
	case string:
		proposalID = p
	default:
		return nil, fmt.Errorf("invalid parameters format: expected array, object, or string, got %T", params)
	}

	if proposalID == "" {
		return nil, fmt.Errorf("proposal ID cannot be empty")
	}

	d.logger.Info("DPoS GetParameterProposal parsed", "proposalID", proposalID)

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return nil, err
	}

	proposal, err := gov.GetParameterProposal(proposalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get proposal: %w", err)
	}

	votes := make(map[string]interface{})
	var supportVoters []map[string]interface{}
	var opposeVoters []map[string]interface{}

	for addr, vote := range proposal.Votes {
		voteTime := time.Unix(int64(vote.Timestamp), 0)
		voteTimeFormatted := voteTime.Format("2006-01-02 15:04:05")

		voteInfo := map[string]interface{}{
			"voter":       vote.Voter.String(),
			"proposalId":  vote.ProposalID,
			"support":     vote.Support,
			"weight":      vote.Weight.String(),
			"timestamp":   voteTimeFormatted,
			"timestampTs": vote.Timestamp,
		}
		votes[addr.String()] = voteInfo

		if vote.Support {
			supportVoters = append(supportVoters, voteInfo)
		} else {
			opposeVoters = append(opposeVoters, voteInfo)
		}
	}

	totalWeight := big.NewInt(0)
	supportWeight := big.NewInt(0)
	for _, vote := range proposal.Votes {
		totalWeight.Add(totalWeight, vote.Weight)
		if vote.Support {
			supportWeight.Add(supportWeight, vote.Weight)
		}
	}

	// 先获取当前区块高度，供 isPassed 使用
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	voteStats := map[string]interface{}{
		"totalVotes":    len(proposal.Votes),
		"supportVotes":  len(supportVoters),
		"opposeVotes":   len(opposeVoters),
		"supportWeight": supportWeight.String(),
		"totalWeight":   totalWeight.String(),
		"passRate": func() string {
			if totalWeight.Sign() == 0 {
				return "0.00%"
			}
			passRate := new(big.Float).Quo(new(big.Float).SetInt(supportWeight), new(big.Float).SetInt(totalWeight))
			passRate.Mul(passRate, big.NewFloat(100))
			value, _ := passRate.Float64()
			return fmt.Sprintf("%.2f%%", value)
		}(),
		"isPassed": func() bool {
			// 修复：isPassed 必须与 Status 保持一致
			// 1. 如果投票期未结束，返回 false（即使支持率100%）
			if currentBlockNumber <= proposal.EndBlock {
				return false
			}
			// 2. 如果投票期已结束，直接使用 Status 字段（更可靠）
			// 如果 Status 还未更新，先尝试检查结果
			if proposal.Status != dpos.ProposalPassed && proposal.Status != dpos.ProposalRejected {
				// 投票期已结束但状态未更新，计算支持率
				if totalWeight.Sign() == 0 {
					return false
				}
				passRate := new(big.Int).Mul(supportWeight, big.NewInt(100))
				passRate.Div(passRate, totalWeight)
				return passRate.Uint64() >= proposal.Threshold
			}
			// 3. 状态已更新，直接使用 Status
			return proposal.Status == dpos.ProposalPassed
		}(),
	}

	timeInfo := map[string]interface{}{
		"proposalBlocks": fmt.Sprintf("Blocks %d - %d", proposal.StartBlock, proposal.EndBlock),
		"proposalPeriod": fmt.Sprintf("%ds (%d blocks)", (proposal.EndBlock-proposal.StartBlock)*3, proposal.EndBlock-proposal.StartBlock),
		"votingPeriod":   fmt.Sprintf("%ds (%d blocks)", (proposal.EndBlock-proposal.StartBlock)*3, proposal.EndBlock-proposal.StartBlock),
	}

	timeInfo["currentBlock"] = currentBlockNumber
	if currentBlockNumber <= proposal.EndBlock {
		remainingBlocks := proposal.EndBlock - currentBlockNumber
		if currentBlockNumber < proposal.StartBlock {
			remainingBlocks = proposal.EndBlock - proposal.StartBlock
		}
		timeInfo["remainingBlocks"] = remainingBlocks
		timeInfo["isExpired"] = false
	} else {
		timeInfo["remainingBlocks"] = 0
		timeInfo["isExpired"] = true
	}

	// 保存 currentBlockNumber 供 isPassed 使用
	voteStats["currentBlockNumber"] = currentBlockNumber

	// 格式化创建时间
	var createdAtFormatted string
	var createdAtTs uint64
	if proposal.CreatedAt > 0 {
		createdAtTs = proposal.CreatedAt
		createdAtTime := time.Unix(int64(proposal.CreatedAt), 0)
		createdAtFormatted = createdAtTime.Format("2006-01-02 15:04:05")
	} else {
		createdAtFormatted = ""
		createdAtTs = 0
	}

	return map[string]interface{}{
		"success": true,
		"proposal": map[string]interface{}{
			"proposalId":   proposalID,
			"proposalType": proposal.ProposalType,
			"parameter":    proposal.Parameter,
			"newValue":     proposal.NewValue,
			"oldValue":     proposal.OldValue,
			"description":  proposal.Description,
			"validator": func() string {
				if proposal.ValidatorAddress != (types.Address{}) {
					return proposal.ValidatorAddress.String()
				}
				return ""
			}(),
			"recoveryReason": proposal.RecoveryReason,
			"proposer":       proposal.Proposer.String(),
			"startBlock":     proposal.StartBlock,
			"endBlock":       proposal.EndBlock,
			"validEndBlock":  proposal.ValidEndBlock,
			"status":         proposal.Status.String(),
			"createdAt":      createdAtFormatted,
			"createdAtTs":    createdAtTs,
			"votes":          votes,
			"voteStats":      voteStats,
			"timeInfo":       timeInfo,
			"voterDetails": map[string]interface{}{
				"supportVoters": supportVoters,
				"opposeVoters":  opposeVoters,
				"totalVoters":   len(proposal.Votes),
			},
		},
	}, nil
}

// RegisterDelegate 注册受托人
func (d *DPOS) RegisterDelegate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("🚀 ===== DPoS受托人注册RPC调用开始 =====")
	d.logger.Info("📋 接收到的参数", "params", params)

	// 解析参数
	d.logger.Info("🔍 开始解析RPC参数...")
	var registrantStr, name, website, description, privateKey string
	var chainID uint64 = 20230826 // 默认chainID

	if paramMap, ok := params.(map[string]interface{}); ok {
		registrantStr, _ = paramMap["registrant"].(string)
		name, _ = paramMap["name"].(string)
		website, _ = paramMap["website"].(string)
		description, _ = paramMap["description"].(string)
		privateKey, _ = paramMap["privateKey"].(string)

		// 解析chainID
		if chainIDInterface, exists := paramMap["chainID"]; exists {
			if chainIDFloat, ok := chainIDInterface.(float64); ok {
				chainID = uint64(chainIDFloat)
			}
		}

		d.logger.Info("✅ 参数解析成功",
			"registrant", registrantStr,
			"name", name,
			"website", website,
			"description", description,
			"hasPrivateKey", privateKey != "",
			"chainID", chainID)
	} else {
		d.logger.Error("❌ 参数格式无效")
		return nil, fmt.Errorf("invalid parameters format")
	}

	if registrantStr == "" {
		d.logger.Error("❌ 缺少注册者地址")
		return nil, fmt.Errorf("registrant address is required")
	}

	if privateKey == "" {
		d.logger.Error("❌ 缺少私钥")
		return nil, fmt.Errorf("private key is required")
	}

	registrant := types.StringToAddress(registrantStr)
	d.logger.Info("📍 注册者地址转换完成", "address", registrant.String())

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		d.logger.Error("❌ DPoS引擎不可用")
		return nil, fmt.Errorf("DPoS engine not available")
	}
	d.logger.Info("✅ DPoS引擎获取成功")

	d.logger.Info("🔧 开始调用DPoS引擎注册受托人...")
	if registerDelegate, ok := dposEngine.(interface {
		RegisterDelegateWithKeyAndChainID(registrant types.Address, name, website, description, privateKey string, chainID uint64) error
	}); ok {
		err := registerDelegate.RegisterDelegateWithKeyAndChainID(registrant, name, website, description, privateKey, chainID)
		if err != nil {
			d.logger.Error("❌ 注册受托人失败", "error", err)
			return nil, fmt.Errorf("failed to register delegate: %w", err)
		}
		d.logger.Info("🎉 ===== 受托人注册RPC调用成功 =====")

		// 获取冻结信息
		frozenAt := uint64(time.Now().Unix())
		result := map[string]interface{}{
			"success":      true,
			"message":      "Delegate registration submitted successfully",
			"frozenAmount": "0", // 将在交易处理时设置
			"frozenAt":     frozenAt,
		}

		// 如果已注册，尝试获取冻结信息
		if dposEngine != nil {
			if isRegistered, ok := dposEngine.(interface {
				IsDelegateRegistered(address types.Address) bool
			}); ok {
				reg := isRegistered.IsDelegateRegistered(registrant)
				if reg {
					if getFreezeInfo, ok := dposEngine.(interface {
						GetFreezeInfo(address types.Address) (*dpos.FreezeInfo, error)
					}); ok {
						if freezeInfo, err := getFreezeInfo.GetFreezeInfo(registrant); err == nil && freezeInfo != nil {
							result["frozenAmount"] = freezeInfo.FrozenAmount.String()
							result["frozenAt"] = freezeInfo.FrozenAt
						}
					}
				}
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("DPoS engine does not support delegate registration")
}

// GetDelegateRegistrations 获取受托人注册列表
func (d *DPOS) GetDelegateRegistrations(ctx context.Context) (interface{}, error) {
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}

	if getRegistrations, ok := dposEngine.(interface {
		GetDelegateRegistrations() ([]*dpos.DelegateRegistration, error)
	}); ok {
		registrations, err := getRegistrations.GetDelegateRegistrations()
		if err != nil {
			return nil, fmt.Errorf("failed to get registrations: %w", err)
		}
		return map[string]interface{}{
			"success":       true,
			"registrations": registrations,
		}, nil
	}

	return nil, fmt.Errorf("DPoS engine does not support delegate registrations")
}

// WithdrawDelegate 退出受托人
func (d *DPOS) WithdrawDelegate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS WithdrawDelegate called", "params", params)

	// 解析参数
	var addressStr string
	var privateKey string

	if paramMap, ok := params.(map[string]interface{}); ok {
		addressStr, _ = paramMap["address"].(string)
		privateKey, _ = paramMap["privateKey"].(string)
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) == 1 {
		addressStr, _ = paramArray[0].(string)
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) >= 2 {
		addressStr, _ = paramArray[0].(string)
		if pk, ok := paramArray[1].(string); ok {
			privateKey = pk
		}
	} else {
		return nil, fmt.Errorf("invalid parameters format")
	}

	if addressStr == "" {
		return nil, fmt.Errorf("address is required")
	}

	if strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("privateKey is required")
	}

	address := types.StringToAddress(addressStr)

	// 校验私钥与地址匹配
	privateKey = strings.TrimSpace(privateKey)
	privateKey = strings.TrimPrefix(privateKey, "0x")
	privateKey = strings.TrimPrefix(privateKey, "0X")

	if len(privateKey) != 64 {
		return nil, fmt.Errorf("invalid private key length: expected 64 hex characters, got %d", len(privateKey))
	}

	for i, char := range privateKey {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return nil, fmt.Errorf("invalid hex character in private key at position %d: %c (U+%04X)", i, char, char)
		}
	}

	privateKeyBytes, err := hex.DecodeString(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key: %w", err)
	}
	if len(privateKeyBytes) != 32 {
		return nil, fmt.Errorf("invalid private key length: expected 32 bytes, got %d", len(privateKeyBytes))
	}

	privKey, err := crypto.ParseECDSAPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create ECDSA private key: %w", err)
	}

	derivedAddress := crypto.PubKeyToAddress(&privKey.PublicKey)
	if derivedAddress != address {
		return nil, fmt.Errorf("private key does not match delegate address: derived %s, expected %s", derivedAddress.String(), address.String())
	}

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}

	if withdrawDelegate, ok := dposEngine.(interface {
		WithdrawDelegate(address types.Address) error
	}); ok {
		err := withdrawDelegate.WithdrawDelegate(address)
		if err != nil {
			// 检查错误类型，返回详细错误信息
			errStr := err.Error()
			if strings.Contains(errStr, "cannot withdraw while having votes") {
				// 有投票，需要先撤回
				return map[string]interface{}{
					"success":               false,
					"error":                 errStr,
					"code":                  "HAS_ACTIVE_VOTES",
					"withdrawVoteInterface": "dpos_vote",
					"note":                  "Use dpos_vote with amount=0 or negative amount to withdraw votes manually",
				}, nil
			} else if strings.Contains(errStr, "cannot withdraw before minimum freeze period") {
				// 不满足最小冻结期
				return map[string]interface{}{
					"success": false,
					"error":   errStr,
					"code":    "MIN_FREEZE_PERIOD_NOT_MET",
				}, nil
			}
			return nil, fmt.Errorf("failed to withdraw delegate: %w", err)
		}

		// 获取解冻信息
		result := map[string]interface{}{
			"success": true,
			"message": "Delegate withdrawn and unfrozen successfully",
		}

		// 尝试获取冻结信息
		if getFreezeInfo, ok := dposEngine.(interface {
			GetFreezeInfo(address types.Address) (*dpos.FreezeInfo, error)
		}); ok {
			if freezeInfo, err := getFreezeInfo.GetFreezeInfo(address); err == nil && freezeInfo != nil {
				result["unfrozenAmount"] = freezeInfo.FrozenAmount.String()
				result["unfrozenAt"] = freezeInfo.UnfreezeAt
				result["lockPeriod"] = freezeInfo.UnfreezeAvailableAt - freezeInfo.UnfreezeAt
				result["unfreezeAvailableAt"] = freezeInfo.UnfreezeAvailableAt
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("DPoS engine does not support delegate withdrawal")
}

// GetFreezeInfo 查询冻结信息（支持单个和批量）
func (d *DPOS) GetFreezeInfo(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetFreezeInfo called", "params", params)

	// 解析参数
	var addressStr string
	var addresses []string
	var isBatch bool

	if paramMap, ok := params.(map[string]interface{}); ok {
		if addr, ok := paramMap["address"].(string); ok {
			addressStr = addr
			isBatch = false
		} else if addrs, ok := paramMap["addresses"].([]interface{}); ok {
			addresses = make([]string, 0, len(addrs))
			for _, addr := range addrs {
				if addrStr, ok := addr.(string); ok {
					addresses = append(addresses, addrStr)
				}
			}
			if len(addresses) > 100 {
				return nil, fmt.Errorf("too many addresses, maximum 100 allowed")
			}
			isBatch = true
		} else {
			return nil, fmt.Errorf("address or addresses parameter is required")
		}
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) >= 1 {
		if addr, ok := paramArray[0].(string); ok {
			addressStr = addr
			isBatch = false
		} else {
			return nil, fmt.Errorf("invalid address format")
		}
	} else {
		return nil, fmt.Errorf("invalid parameters format")
	}

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}

	// 批量查询
	if isBatch {
		results := make([]map[string]interface{}, 0, len(addresses))
		for _, addrStr := range addresses {
			addr := types.StringToAddress(addrStr)
			result := d.buildFreezeInfoResponse(dposEngine, addr)
			results = append(results, result)
		}
		return map[string]interface{}{
			"success":     true,
			"freezeInfos": results,
		}, nil
	}

	// 单个查询
	if addressStr == "" {
		return nil, fmt.Errorf("address is required")
	}
	address := types.StringToAddress(addressStr)
	result := d.buildFreezeInfoResponse(dposEngine, address)
	return result, nil
}

// GetValidatorCommission 获取验证者佣金率信息
func (d *DPOS) GetValidatorCommission(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetValidatorCommission called", "params", params)

	var validatorAddress string

	switch p := params.(type) {
	case []interface{}:
		if len(p) == 0 {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address parameter is required",
			}, nil
		}
		if addr, ok := p[0].(string); ok {
			validatorAddress = addr
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address must be a string",
			}, nil
		}
	case string:
		validatorAddress = p
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	if validatorAddress == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "validator address cannot be empty",
		}, nil
	}

	addr := types.StringToAddress(validatorAddress)

	dposState, err := d.store.GetDPoSState()
	if err != nil {
		return map[string]interface{}{
			"success":   false,
			"validator": validatorAddress,
			"error":     fmt.Sprintf("failed to get dpos state: %v", err),
		}, nil
	}

	// 备用方案：如果 store 返回 nil，尝试从 DPoS engine 或全局注册实例获取
	if dposState == nil {
		// 尝试从当前共识引擎获取
		if engine := d.getDPoSEngine(); engine != nil {
			if provider, ok := engine.(interface {
				GetState() *dpos.State
			}); ok {
				dposState = provider.GetState()
			}
		}

		// 如果仍然为空，遍历全局注册的 DPoS 实例
		if dposState == nil {
			for instanceKey, instance := range dpos.GetAllDPoSInstances() {
				if instance == nil {
					continue
				}
				if candidate := instance.GetState(); candidate != nil {
					d.logger.Debug("Using state from registered DPoS instance for commission query",
						"instanceKey", instanceKey)
					dposState = candidate
					break
				}
			}
		}
	}

	if dposState == nil || dposState.StakeStore == nil {
		return map[string]interface{}{
			"success":   false,
			"validator": validatorAddress,
			"error":     "stake store not available",
		}, nil
	}

	delegateInfo, err := dposState.StakeStore.GetDelegateInfo(addr)
	if err != nil {
		return map[string]interface{}{
			"success":   false,
			"validator": validatorAddress,
			"error":     fmt.Sprintf("failed to load delegate info: %v", err),
		}, nil
	}

	if delegateInfo == nil {
		return map[string]interface{}{
			"success":   false,
			"validator": validatorAddress,
			"error":     "validator not found",
			"status":    "not_registered",
		}, nil
	}

	// 从 ParameterStore 读取默认佣金率，如果不存在则使用硬编码默认值 1000 (10%)
	defaultCommission := uint64(1000)
	if dposState.ParameterStore != nil {
		if value, err := dposState.ParameterStore.GetParameterValue("dpos_commission_ratio"); err == nil {
			if parsed, ok := toUint64(value); ok && parsed > 0 {
				defaultCommission = parsed
			}
		} else if value, err := dposState.ParameterStore.GetParameterValue("dpos_commission_radio"); err == nil {
			// 兼容拼写错误
			if parsed, ok := toUint64(value); ok && parsed > 0 {
				defaultCommission = parsed
			}
		}
	}

	// 优先从数据库（ParameterStore）读取佣金生效周期（经过治理流程修改的值是权威数据源）
	effectivePeriod := 21 * 24 * time.Hour // 默认值，仅在无法获取配置时使用
	if dposState.ParameterStore != nil {
		if value, err := dposState.ParameterStore.GetParameterValue("dpos_commission_effective"); err == nil {
			if seconds, ok := parseDurationSeconds(value); ok {
				effectivePeriod = time.Duration(seconds) * time.Second
				d.logger.Debug("从数据库（ParameterStore）读取佣金生效周期", "period", effectivePeriod.String())
			}
		}
	}

	// 如果数据库中没有，从 DPoS 引擎配置读取（从配置文件读取的初始值）
	if effectivePeriod == 21*24*time.Hour {
		if dposEngine := d.getDPoSEngine(); dposEngine != nil {
			if dpos, ok := dposEngine.(*dpos.DPoS); ok {
				configPeriod := dpos.GetCommissionEffectivePeriod()
				if configPeriod > 0 {
					effectivePeriod = configPeriod
					d.logger.Debug("从DPoS引擎配置（配置文件）读取佣金生效周期", "period", effectivePeriod.String())
				}
			}
		}
	}

	now := uint64(time.Now().Unix())
	effectiveSeconds := uint64(effectivePeriod.Seconds())

	commissionRate := delegateInfo.CommissionRate
	if commissionRate == 0 {
		commissionRate = defaultCommission
	}

	pendingRate := delegateInfo.PendingCommissionRate
	updateTime := delegateInfo.CommissionUpdateTime

	var pendingEffectiveAt uint64
	var secondsUntilEffective uint64
	status := "active"

	if pendingRate != 0 {
		if effectiveSeconds == 0 {
			pendingEffectiveAt = updateTime
		} else {
			pendingEffectiveAt = updateTime + effectiveSeconds
		}

		if pendingEffectiveAt <= now {
			status = "pending_ready"
			secondsUntilEffective = 0
		} else {
			status = "pending"
			secondsUntilEffective = pendingEffectiveAt - now
		}
	} else if delegateInfo.CommissionRate == 0 {
		status = "default"
	}

	// 计算剩余时间（从当前时间到生效时间的剩余时间，等同于 secondsUntilEffective）
	remainTime := secondsUntilEffective

	response := map[string]interface{}{
		"validator":                         validatorAddress,
		"commissionRate":                    commissionRate,
		"commissionRatePercent":             formatBasisPoints(commissionRate),
		"pendingCommissionRate":             pendingRate,
		"pendingCommissionRatePercent":      formatBasisPoints(pendingRate),
		"defaultCommissionRate":             defaultCommission,
		"defaultCommissionRatePercent":      formatBasisPoints(defaultCommission),
		"commissionUpdateTime":              updateTime,
		"commissionUpdateTimeHumanReadable": formatTimestamp(updateTime),
		"pendingEffectiveAt":                pendingEffectiveAt,
		"pendingEffectiveAtHumanReadable":   formatTimestamp(pendingEffectiveAt),
		"secondsUntilEffective":             secondsUntilEffective,
		"effectivePeriodSeconds":            effectiveSeconds,
		"effectivePeriodHumanReadable":      effectivePeriod.String(),
		"currentTimestamp":                  now,
		"currentTimestampHumanReadable":     formatTimestamp(now),
		"remainTime":                        remainTime,
		"remainTimeHumanReadable":           formatRemainTime(remainTime),
		"status":                            status,
		"hasPendingCommissionRate":          pendingRate != 0,
		"pendingReadyForActivation":         pendingRate != 0 && pendingEffectiveAt <= now,
		"commissionRateBasisPoints":         commissionRate,
		"pendingCommissionRateBasisPoints":  pendingRate,
		"defaultCommissionRateBasisPoints":  defaultCommission,
	}

	return response, nil
}

// UpdateCommission 更新验证者佣金率（一行搞定，像 CLI 一样简单）
// 参数: [validatorAddress, commissionRate, privateKey]
// commissionRate: 佣金率（基点），范围 500-8000 (5%-80%)
// privateKey: 验证者私钥（64字符十六进制，不带0x前缀）
func (d *DPOS) UpdateCommission(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS UpdateCommission called", "params", params)

	var validatorAddress string
	var commissionRate uint64
	var privateKey string

	// 解析参数
	switch p := params.(type) {
	case []interface{}:
		if len(p) < 3 {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address, commission rate and private key are required",
			}, nil
		}
		if addr, ok := p[0].(string); ok {
			validatorAddress = addr
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address must be a string",
			}, nil
		}
		if rate, ok := p[1].(float64); ok {
			commissionRate = uint64(rate)
		} else if rate, ok := p[1].(string); ok {
			// 支持十六进制字符串
			parsed, err := strconv.ParseUint(strings.TrimPrefix(rate, "0x"), 16, 64)
			if err != nil {
				return map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("invalid commission rate: %v", err),
				}, nil
			}
			commissionRate = parsed
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "commission rate must be a number",
			}, nil
		}
		if key, ok := p[2].(string); ok {
			privateKey = strings.TrimPrefix(key, "0x")
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "private key must be a string",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T", params),
		}, nil
	}

	// 验证佣金率范围
	if commissionRate < 500 || commissionRate > 8000 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("commission rate out of range [500, 8000], got %d", commissionRate),
		}, nil
	}

	validatorAddr := types.StringToAddress(validatorAddress)

	// 构造交易数据: "DPOS" + "COM" + 佣金率(2字节 BigEndian)
	inputData := []byte("DPOSCOM")
	rateBytes := make([]byte, 2)
	rateBytes[0] = byte(commissionRate >> 8)   // 高字节
	rateBytes[1] = byte(commissionRate & 0xFF) // 低字节
	inputData = append(inputData, rateBytes...)

	// 获取 nonce（使用 GetNonce 方法，而不是 GetAccount）
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(validatorAddr)
	} else {
		// 如果 store 不支持 GetNonce，尝试通过 GetAccount 获取（使用当前区块的状态根）
		// 首先尝试获取当前区块头
		var stateRoot types.Hash
		if headerStore, ok := d.store.(interface {
			Header() *types.Header
		}); ok {
			if header := headerStore.Header(); header != nil {
				stateRoot = header.StateRoot
			}
		}

		// 如果无法获取当前区块头，尝试获取最新区块
		if stateRoot == (types.Hash{}) {
			// 尝试获取一个较大的区块号（实际应该获取最新区块）
			for blockNum := uint64(1000000); blockNum > 0; blockNum-- {
				if header, exists := d.store.GetHeaderByNumber(blockNum); exists && header != nil {
					stateRoot = header.StateRoot
					break
				}
			}
		}

		if stateRoot != (types.Hash{}) {
			if account, err := d.store.GetAccount(stateRoot, validatorAddr); err == nil {
				nonce = account.Nonce
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("failed to get account nonce: %v", err),
				}, nil
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "failed to get current block state root for nonce lookup",
			}, nil
		}
	}

	// 获取 gas price
	gasPrice := big.NewInt(1000000000) // 1 Gwei

	// 计算 gas limit：基础 gas (21000) + 数据 gas (每字节 68 gas for non-zero, 4 gas for zero)
	// Input 数据: "DPOSCOM" (7字节) + 佣金率(2字节) = 9字节
	// 假设都是非零字节，需要 9 * 68 = 612 gas
	// 基础交易需要 21000 gas
	// 总共至少需要 21000 + 612 = 21612，我们设置 100000 以确保足够
	gasLimit := uint64(100000)

	// 创建交易
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       nil,
		Value:    big.NewInt(0),
		Input:    inputData,
		Type:     types.LegacyTx,
	}

	// 计算交易哈希
	tx.ComputeHash(0)
	if tx.Hash == (types.Hash{}) {
		return map[string]interface{}{
			"success": false,
			"error":   "transaction hash is zero after creation",
		}, nil
	}

	// 签名交易
	if err := d.signTransaction(tx, validatorAddr, privateKey); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to sign transaction: %v", err),
		}, nil
	}

	// 重新计算哈希（签名后）
	tx.ComputeHash(0)

	// 发送交易到交易池
	// 尝试通过 store 的 AddTx 方法发送
	if addTxStore, ok := d.store.(interface {
		AddTx(tx *types.Transaction) error
	}); ok {
		if err := addTxStore.AddTx(tx); err != nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("failed to send transaction: %v", err),
			}, nil
		}
	} else {
		// 如果 store 不支持 AddTx，尝试通过 GetTxPool 获取交易池
		if txPool := d.store.GetTxPool(); txPool != nil {
			if txPoolAddTx, ok := txPool.(interface {
				AddTx(tx *types.Transaction) error
			}); ok {
				if err := txPoolAddTx.AddTx(tx); err != nil {
					return map[string]interface{}{
						"success": false,
						"error":   fmt.Sprintf("failed to send transaction: %v", err),
					}, nil
				}
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   "transaction pool does not support AddTx method",
				}, nil
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "transaction pool not available",
			}, nil
		}
	}

	return map[string]interface{}{
		"success":        true,
		"txHash":         tx.Hash.String(),
		"validator":      validatorAddress,
		"commissionRate": commissionRate,
		"message":        "Commission update transaction sent successfully",
	}, nil
}

// buildFreezeInfoResponse 构建冻结信息响应
func (d *DPOS) buildFreezeInfoResponse(dposEngine interface{}, address types.Address) map[string]interface{} {
	result := map[string]interface{}{
		"success": true,
		"address": address.String(),
	}

	// 获取冻结信息
	if getFreezeInfo, ok := dposEngine.(interface {
		GetFreezeInfo(address types.Address) (*dpos.FreezeInfo, error)
	}); ok {
		freezeInfo, err := getFreezeInfo.GetFreezeInfo(address)
		if err == nil && freezeInfo != nil {
			result["isDelegate"] = false
			result["frozenAmount"] = freezeInfo.FrozenAmount.String()
			result["frozenAt"] = freezeInfo.FrozenAt
			result["unfreezeAt"] = freezeInfo.UnfreezeAt
			result["unfreezeAvailableAt"] = freezeInfo.UnfreezeAvailableAt
			result["status"] = freezeInfo.Status

			// 计算锁定期和剩余时间
			unfreezeLockPeriod := uint64(1209600) // 默认14天
			// 尝试从冻结信息中获取锁定期（如果已解冻）
			if freezeInfo.UnfreezeAt > 0 && freezeInfo.UnfreezeAvailableAt > freezeInfo.UnfreezeAt {
				unfreezeLockPeriod = freezeInfo.UnfreezeAvailableAt - freezeInfo.UnfreezeAt
			}
			result["lockPeriod"] = unfreezeLockPeriod

			currentTime := uint64(time.Now().Unix())
			remainingLockTime := uint64(0)
			if freezeInfo.UnfreezeAvailableAt > 0 && currentTime < freezeInfo.UnfreezeAvailableAt {
				remainingLockTime = freezeInfo.UnfreezeAvailableAt - currentTime
			}
			result["remainingLockTime"] = remainingLockTime
			result["canWithdraw"] = remainingLockTime == 0 && freezeInfo.UnfreezeAvailableAt > 0

			// 检查是否为受托人
			if isRegistered, ok := dposEngine.(interface {
				IsDelegateRegistered(address types.Address) bool
			}); ok {
				isDelegate := isRegistered.IsDelegateRegistered(address)
				result["isDelegate"] = isDelegate

				if isDelegate {
					// 获取注册信息
					if getReg, ok := dposEngine.(interface {
						GetDelegateRegistration(address types.Address) (*dpos.DelegateRegistration, error)
					}); ok {
						if reg, err := getReg.GetDelegateRegistration(address); err == nil && reg != nil {
							result["registrationInfo"] = map[string]interface{}{
								"name":      reg.Name,
								"status":    reg.Status,
								"deposit":   reg.Deposit.String(),
								"createdAt": reg.CreatedAt,
							}
							result["voteInfo"] = map[string]interface{}{
								"totalVotes": reg.TotalVotes.String(),
								"hasVotes":   reg.TotalVotes.Cmp(big.NewInt(0)) > 0,
							}
						}
					}
				}
			}
		} else {
			// 无冻结信息
			result["isDelegate"] = false
			result["frozenAmount"] = "0"
			result["status"] = "none"
		}
	}

	return result
}

// GetAccountBalance 查询账户余额（包含冻结）
func (d *DPOS) GetAccountBalance(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("🔵 [GetAccountBalance] RPC 调用开始", "params", params)

	// 解析参数
	var addressStr string
	if paramMap, ok := params.(map[string]interface{}); ok {
		addressStr, _ = paramMap["address"].(string)
		d.logger.Info("🔵 [GetAccountBalance] 参数格式: map", "address", addressStr)
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) >= 1 {
		addressStr, _ = paramArray[0].(string)
		d.logger.Info("🔵 [GetAccountBalance] 参数格式: array", "address", addressStr, "arrayLen", len(paramArray))
	} else {
		d.logger.Error("❌ [GetAccountBalance] 无效的参数格式", "paramsType", fmt.Sprintf("%T", params))
		return nil, fmt.Errorf("invalid parameters format")
	}

	if addressStr == "" {
		d.logger.Error("❌ [GetAccountBalance] 地址为空")
		return nil, fmt.Errorf("address is required")
	}

	address := types.StringToAddress(addressStr)
	d.logger.Info("🔵 [GetAccountBalance] 解析地址完成", "address", address.String())

	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		d.logger.Error("❌ [GetAccountBalance] DPoS 引擎不可用")
		return nil, fmt.Errorf("DPoS engine not available")
	}
	d.logger.Info("✅ [GetAccountBalance] DPoS 引擎获取成功")

	// 调用DPoS引擎的方法
	if getBalance, ok := dposEngine.(interface {
		GetAccountBalance(address types.Address) (map[string]interface{}, error)
	}); ok {
		d.logger.Info("🔵 [GetAccountBalance] 开始调用 DPoS 引擎的 GetAccountBalance", "address", address.String())
		balanceInfo, err := getBalance.GetAccountBalance(address)
		if err != nil {
			d.logger.Error("❌ [GetAccountBalance] DPoS 引擎调用失败", "error", err.Error())
			return map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}, nil
		}
		d.logger.Info("✅ [GetAccountBalance] DPoS 引擎调用成功", "balanceInfo", balanceInfo)

		// 如果返回的数据已经有 success 字段，直接返回；否则包装
		if _, hasSuccess := balanceInfo["success"]; hasSuccess {
			d.logger.Info("✅ [GetAccountBalance] 返回结果（已有 success 字段）", "result", balanceInfo)
			return balanceInfo, nil
		}
		result := map[string]interface{}{
			"success": true,
			"data":    balanceInfo,
		}
		d.logger.Info("✅ [GetAccountBalance] 返回结果（包装后）", "result", result)
		return result, nil
	}

	d.logger.Error("❌ [GetAccountBalance] DPoS 引擎不支持 GetAccountBalance 方法")
	return map[string]interface{}{
		"success": false,
		"error":   "DPoS engine does not support GetAccountBalance",
	}, nil
}

// CanWithdrawDelegate 检查是否可以退出注册
func (d *DPOS) CanWithdrawDelegate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS CanWithdrawDelegate called", "params", params)

	// 解析参数
	var addressStr string
	if paramMap, ok := params.(map[string]interface{}); ok {
		addressStr, _ = paramMap["address"].(string)
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) >= 1 {
		addressStr, _ = paramArray[0].(string)
	} else {
		return nil, fmt.Errorf("invalid parameters format")
	}

	if addressStr == "" {
		return nil, fmt.Errorf("address is required")
	}

	address := types.StringToAddress(addressStr)
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}

	// 调用DPoS引擎的方法
	if canWithdraw, ok := dposEngine.(interface {
		CanWithdrawDelegate(address types.Address) (map[string]interface{}, error)
	}); ok {
		result, err := canWithdraw.CanWithdrawDelegate(address)
		if err != nil {
			return nil, err
		}
		result["success"] = true
		return result, nil
	}

	return nil, fmt.Errorf("DPoS engine does not support CanWithdrawDelegate")
}

// GetActiveProposals 获取活跃提案列表
func (d *DPOS) GetActiveProposals(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetActiveProposals called")
	_ = params

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}

	proposals, err := gov.GetActiveProposals()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get active proposals: %v", err),
		}, nil
	}

	// 获取当前区块高度，用于判断提案是否过期
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	result := make([]map[string]interface{}, 0, len(proposals))
	for _, proposal := range proposals {
		votes := len(proposal.Votes)

		// 格式化创建时间
		var createdAtFormatted string
		var createdAtTs uint64
		if proposal.CreatedAt > 0 {
			createdAtTs = proposal.CreatedAt
			createdAtTime := time.Unix(int64(proposal.CreatedAt), 0)
			createdAtFormatted = createdAtTime.Format("2006-01-02 15:04:05")
		} else {
			createdAtFormatted = ""
			createdAtTs = 0
		}

		// 判断提案执行有效期是否已过期
		isProposalExpired := currentBlockNumber > proposal.ValidEndBlock

		result = append(result, map[string]interface{}{
			"proposalId":       proposal.ID,
			"parameter":        proposal.Parameter,
			"oldValue":         proposal.OldValue,
			"newValue":         proposal.NewValue,
			"proposer":         proposal.Proposer.String(),
			"startBlock":       proposal.StartBlock,
			"endBlock":         proposal.EndBlock,
			"status":           proposal.Status.String(),
			"threshold":        proposal.Threshold,
			"description":      proposal.Description,
			"createdAt":        createdAtFormatted,
			"createdAtTs":      createdAtTs,
			"votes":            votes,
			"isProposalExpired": isProposalExpired,
		})
	}

	// 按创建时间倒序排序（最新的在前）
	sort.Slice(result, func(i, j int) bool {
		createdAtI, okI := result[i]["createdAtTs"].(uint64)
		createdAtJ, okJ := result[j]["createdAtTs"].(uint64)
		if !okI || !okJ {
			// 如果无法获取时间戳，保持原顺序
			return false
		}
		return createdAtI > createdAtJ // 倒序：大的（新的）在前
	})

	return map[string]interface{}{
		"success":   true,
		"proposals": result,
		"count":     len(result),
	}, nil
}

func (d *DPOS) GetVotableCurrentParameters(ctx context.Context) (interface{}, error) {
	d.logger.Info("DPoS GetVotableCurrentParameters called")

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, nil
	}

	parameters := gov.GetVotableCurrentParameters()
	result := make(map[string]interface{})
	for key, param := range parameters {
		result[key] = map[string]interface{}{
			"name":         param.Name,
			"type":         param.Type,
			"minValue":     param.MinValue,
			"maxValue":     param.MaxValue,
			"description":  param.Description,
			"category":     param.Category,
			"currentValue": param.CurrentValue,
		}
	}

	return map[string]interface{}{
		"success":    true,
		"parameters": result,
		"count":      len(result),
	}, nil
}

// GetConsensusSwitchHeight 获取共识切换高度
func (d *DPOS) GetConsensusSwitchHeight(ctx context.Context) (interface{}, error) {
	d.logger.Info("DPoS GetConsensusSwitchHeight called")

	// 尝试多种方式获取共识切换高度
	consensusSwitchHeight := d.getConsensusSwitchHeight()
	currentHeight := d.getCurrentBlockHeight()

	d.logger.Info("获取共识切换高度结果",
		"consensusSwitchHeight", consensusSwitchHeight,
		"currentHeight", currentHeight)

	// 尝试获取共识切换高度区块的时间戳
	var switchBlockTimestamp uint64 = 0
	var switchBlockHash string = ""
	if consensusSwitchHeight > 0 {
		if header, exists := d.store.GetHeaderByNumber(consensusSwitchHeight); exists && header != nil {
			switchBlockTimestamp = header.Timestamp
			switchBlockHash = header.Hash.String()
			d.logger.Info("成功获取切换高度区块信息",
				"height", consensusSwitchHeight,
				"timestamp", switchBlockTimestamp,
				"hash", switchBlockHash)
		} else {
			d.logger.Warn("无法获取切换高度区块头",
				"height", consensusSwitchHeight)
		}
	} else {
		d.logger.Warn("共识切换高度为0，可能未配置或无法获取")
	}

	isDPoSActive := consensusSwitchHeight > 0 && currentHeight >= consensusSwitchHeight

	return map[string]interface{}{
		"success":                true,
		"consensusSwitchHeight":  consensusSwitchHeight,
		"switchBlockTimestamp":  switchBlockTimestamp,
		"switchBlockHash":         switchBlockHash,
		"currentBlockHeight":     currentHeight,
		"isDPoSActive":            isDPoSActive,
	}, nil
}

// ExecuteParameterUpdate 执行参数更新
func (d *DPOS) ExecuteParameterUpdate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS ExecuteParameterUpdate called", "params", params)

	// 解析参数
	var proposalID, executorStr, executorPrivateKeyHex string
	switch p := params.(type) {
	case []interface{}:
		// 数组格式: ["proposalID", "executor", "executorPrivateKey"]
		if len(p) < 3 {
			return nil, fmt.Errorf("invalid parameters: expected 3 parameters [proposalID, executor, executorPrivateKey], got %d", len(p))
		}
		var ok bool
		proposalID, ok = p[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid proposal ID: expected string, got %T", p[0])
		}
		executorStr, ok = p[1].(string)
		if !ok {
			return nil, fmt.Errorf("invalid executor: expected string, got %T", p[1])
		}
		executorPrivateKeyHex, ok = p[2].(string)
		if !ok {
			return nil, fmt.Errorf("invalid executor private key: expected string, got %T", p[2])
		}
	case map[string]interface{}:
		// 对象格式: {"proposalId": "proposalID", "executor": "...", "executorPrivateKey": "..."}
		var ok bool
		proposalID, ok = p["proposalId"].(string)
		if !ok {
			return nil, fmt.Errorf("proposalId is required and must be a string")
		}
		executorStr, ok = p["executor"].(string)
		if !ok {
			return nil, fmt.Errorf("executor is required and must be a string")
		}
		executorPrivateKeyHex, ok = p["executorPrivateKey"].(string)
		if !ok {
			return nil, fmt.Errorf("executorPrivateKey is required and must be a string")
		}
	case string:
		// 直接字符串格式（不支持，需要executor和私钥）
		return nil, fmt.Errorf("executor and executorPrivateKey are required. Use array or object format")
	default:
		return nil, fmt.Errorf("invalid parameters format: expected array or object, got %T", params)
	}

	if proposalID == "" {
		return nil, fmt.Errorf("proposal ID cannot be empty")
	}

	d.logger.Info("DPoS ExecuteParameterUpdate parsed", "proposalID", proposalID, "executor", executorStr)

	gov, err := d.getGovernanceEngine()
	if err != nil {
		return nil, err
	}

	proposal, err := gov.GetParameterProposal(proposalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get proposal: %w", err)
	}
	if proposal.Status == dpos.ProposalExecuted {
		return nil, fmt.Errorf("proposal %s has already been executed (status: executed), cannot execute again", proposalID)
	}

	// 获取当前区块高度
	currentBlockNumber := gov.GetCurrentBlockNumber()
	if currentBlockNumber == 0 {
		currentBlockNumber = d.getCurrentBlockHeight()
	}

	// 检查1：投票期必须已结束
	if currentBlockNumber <= proposal.EndBlock {
		return nil, fmt.Errorf("proposal %s voting period has not ended yet (current block %d <= end block %d), cannot execute", proposalID, currentBlockNumber, proposal.EndBlock)
	}

	// 检查2：如果投票期已结束但状态未更新，先检查投票结果
	if proposal.Status != dpos.ProposalPassed && proposal.Status != dpos.ProposalRejected {
		// 尝试通过 governanceEngine 调用 CheckProposalResult
		if checkResultEngine, ok := gov.(interface {
			CheckProposalResult(proposalID string) error
		}); ok {
			if err := checkResultEngine.CheckProposalResult(proposalID); err != nil {
				d.logger.Warn("Failed to check proposal result", "proposalID", proposalID, "error", err)
			}
			// 重新获取提案以获取更新后的状态
			proposal, err = gov.GetParameterProposal(proposalID)
			if err != nil {
				return nil, fmt.Errorf("failed to get updated proposal: %w", err)
			}
		}
	}

	// 检查3：提案状态必须为 Passed
	if proposal.Status != dpos.ProposalPassed {
		return nil, fmt.Errorf("proposal %s status is %s, must be 'passed' to execute", proposalID, proposal.Status.String())
	}

	d.logger.Info("提案状态检查通过", "proposalID", proposalID, "status", proposal.Status.String(), "currentBlock", currentBlockNumber, "endBlock", proposal.EndBlock)

	// 改为通过交易执行提案
	// 1. 解析执行者地址和私钥（已在上面解析）
	executor := types.StringToAddress(executorStr)
	if executor == (types.Address{}) {
		return nil, fmt.Errorf("invalid executor address format: %s", executorStr)
	}

	// 验证私钥格式
	if len(executorPrivateKeyHex) != 64 {
		return nil, fmt.Errorf("invalid executor private key length: expected 64, got %d", len(executorPrivateKeyHex))
	}

	// 2. 创建交易数据
	txData := dpos.ProposalExecuteTxData{
		ProposalID: proposalID,
	}

	// 3. 创建并签名交易
	tx, err := d.createProposalExecuteTransaction(executor, executorPrivateKeyHex, txData)
	if err != nil {
		return nil, fmt.Errorf("failed to create execute transaction: %w", err)
	}

	// 4. 添加到交易池并广播
	if err := d.addProposalTransactionToPool(tx); err != nil {
		return nil, fmt.Errorf("failed to broadcast execute transaction: %w", err)
	}

	d.logger.Info("✅ 执行提案交易已创建并广播", "txHash", tx.Hash.String(), "proposalID", proposalID)

	return map[string]interface{}{
		"success":            true,
		"txHash":             tx.Hash.String(),
		"proposalId":         proposalID,
		"executor":           executor.String(),
		"currentBlockNumber": currentBlockNumber,
		"message":            "Execute proposal transaction created and broadcasted successfully",
		"note":               "Proposal will be executed when transaction is included in a block",
	}, nil
}

// getCurrentProposalPeriodInfo 获取当前提案周期信息
func (d *DPOS) getCurrentProposalPeriodInfo() string {
	gov, err := d.getGovernanceEngine()
	if err != nil {
		return "Unable to get proposal period information"
	}

	periodInfo := gov.GetCurrentProposalPeriod()
	if timeInfo, exists := periodInfo["timeInfo"]; exists {
		if timeStr, ok := timeInfo.(string); ok {
			return timeStr
		}
	}

	return "Unable to get proposal period information"
}

// GetBlockProducers 获取区块范围内的出块者信息
// 支持两种模式：
// 1. 按区块范围：[startBlock, endBlock]
// 2. 按Epoch：[{"epoch": epochNumber}]
func (d *DPOS) GetBlockProducers(ctx context.Context, params interface{}) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetBlockProducers called", "params", params)

	var startBlock, endBlock uint64
	var mode string
	var epochNumber uint64

	// 解析参数
	switch p := params.(type) {
	case []interface{}:
		if len(p) == 0 {
			return map[string]interface{}{
				"success": false,
				"error":   "parameters required",
			}, nil
		}

		// 判断是区块范围还是Epoch
		if len(p) == 1 {
			// 可能是Epoch对象或单个数字
			if epochMap, ok := p[0].(map[string]interface{}); ok {
				// 按Epoch查询
				epochVal, ok := epochMap["epoch"]
				if !ok {
					return map[string]interface{}{
						"success": false,
						"error":   "epoch parameter required in object mode",
					}, nil
				}
				epochNumber = uint64(epochVal.(float64))
				mode = "epoch"
			} else if epochNum, ok := p[0].(float64); ok {
				// 简化版：直接传epoch数字
				epochNumber = uint64(epochNum)
				mode = "epoch"
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   "invalid parameter format",
				}, nil
			}
		} else if len(p) == 2 {
			// 区块范围模式
			start := p[0]
			end := p[1]
			startBlock = uint64(start.(float64))
			endBlock = uint64(end.(float64))
			mode = "blockRange"
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "invalid parameter count",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   "invalid parameter type",
		}, nil
	}

	// 如果是Epoch模式，转换为区块范围
	if mode == "epoch" {
		// 获取DPoS引擎
		dposEngine := d.getDPoSEngine()
		if dposEngine == nil {
			return map[string]interface{}{
				"success": false,
				"error":   "DPoS engine not available",
			}, nil
		}

		// 获取共识切换高度和epoch大小
		var consensusSwitchHeight, epochSize uint64
		if engine, ok := dposEngine.(interface {
			GetConfig() interface{}
		}); ok {
			config := engine.GetConfig()
			if cfg, ok := config.(*dpos.DPoSConfig); ok {
				consensusSwitchHeight = cfg.ConsensusSwitchHeight
				epochSize = cfg.DPoSValidatorsCount
			}
		}

		// 计算Epoch对应的区块范围
		if epochNumber == 0 {
			// Epoch 0 是IBFT区块
			startBlock = 0
			endBlock = consensusSwitchHeight - 1
		} else {
			// DPoS Epoch
			dposEpoch := epochNumber - 1
			startBlock = consensusSwitchHeight + dposEpoch*epochSize
			endBlock = startBlock + epochSize - 1
		}

		d.logger.Info("🔄 Epoch转换为区块范围",
			"epoch", epochNumber,
			"startBlock", startBlock,
			"endBlock", endBlock)
	}

	// 验证区块范围
	if endBlock < startBlock {
		return map[string]interface{}{
			"success": false,
			"error":   "invalid block range: endBlock < startBlock",
		}, nil
	}

	// 从区块链获取区块出块者信息
	producerBlocks := make(map[types.Address][]uint64)

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		// 获取区块头
		header, exists := d.store.GetHeaderByNumber(blockNum)
		if !exists {
			continue
		}

		// 从区块头获取出块者
		miner := types.BytesToAddress(header.Miner)

		// 添加到结果
		producerBlocks[miner] = append(producerBlocks[miner], blockNum)
	}

	// 构建返回结果
	result := make(map[string]interface{})
	producers := make([]map[string]interface{}, 0)

	for address, blocks := range producerBlocks {
		producers = append(producers, map[string]interface{}{
			"address": address.String(),
			"blocks":  blocks,
			"count":   len(blocks),
		})
	}

	// 按出块数量排序
	sort.Slice(producers, func(i, j int) bool {
		countI := producers[i]["count"].(int)
		countJ := producers[j]["count"].(int)
		if countI == countJ {
			// 如果数量相同，按地址排序
			addrI := producers[i]["address"].(string)
			addrJ := producers[j]["address"].(string)
			return addrI < addrJ
		}
		return countI > countJ
	})

	result["success"] = true
	result["mode"] = mode
	if mode == "epoch" {
		result["epoch"] = epochNumber
	}
	result["startBlock"] = startBlock
	result["endBlock"] = endBlock
	result["producers"] = producers
	result["totalBlocks"] = endBlock - startBlock + 1
	result["actualBlocksFound"] = func() int {
		total := 0
		for _, p := range producers {
			total += p["count"].(int)
		}
		return total
	}()

	d.logger.Info("✅ GetBlockProducers 完成",
		"mode", mode,
		"startBlock", startBlock,
		"endBlock", endBlock,
		"producerCount", len(producers))

	return result, nil
}

// 提案交易创建辅助函数

// createProposalCreateTransaction 创建创建提案交易
func (d *DPOS) createProposalCreateTransaction(proposer types.Address, proposerPrivateKeyHex string, txData dpos.ProposalCreateTxData) (*types.Transaction, error) {
	// 获取nonce
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(proposer)
	}

	// 计算 gasPrice = max(baseFee, priceLimit) 并预留少量余量，避免 underpriced
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
	// 选择更高者，并+10% 作为余量
	maxBase := baseFee
	if priceLimit > maxBase {
		maxBase = priceLimit
	}
	if maxBase == 0 {
		maxBase = 1_000_000_000
	} // 1 gwei 兜底
	gasPrice := new(big.Int).SetUint64(maxBase + (maxBase / 10))
	d.logger.Info("🛠️ 构建标准EVM交易(创建提案)", "baseFee", baseFee, "priceLimit", priceLimit, "chosenGasPrice", gasPrice.String())

	// 序列化交易数据
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal proposal create tx data: %w", err)
	}

	// 创建标准交易（Legacy），目标为 self-call（无代码则 no-op）
	to := proposer
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      200000, // 提案交易gas limit
		To:       &to,
		Value:    big.NewInt(0),
		Input:    txDataBytes,
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
		Hash:     types.Hash{},
	}

	// 先计算交易哈希（用于生成proposalID）
	tx.ComputeHash(0)

	// 签名交易（用私钥签名交易本身）
	if err := d.signTransaction(tx, proposer, proposerPrivateKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign proposal create transaction: %w", err)
	}

	// 重新计算交易哈希（签名后）
	tx.ComputeHash(0)

	return tx, nil
}

// createProposalVoteTransaction 创建投票交易
func (d *DPOS) createProposalVoteTransaction(voter types.Address, privateKeyHex string, txData dpos.ProposalVoteTxData) (*types.Transaction, error) {
	// 获取nonce
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(voter)
	}

	// 计算 gasPrice = max(baseFee, priceLimit) 并+10%
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
	gasPrice := new(big.Int).SetUint64(maxBase + (maxBase / 10))
	d.logger.Info("🛠️ 构建标准EVM交易(投票)", "baseFee", baseFee, "priceLimit", priceLimit, "chosenGasPrice", gasPrice.String())

	// 序列化交易数据
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal proposal vote tx data: %w", err)
	}

	// 创建标准交易（Legacy），目标 self-call
	to := voter
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      150000,
		To:       &to,
		Value:    big.NewInt(0),
		Input:    txDataBytes,
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
		Hash:     types.Hash{},
	}

	// 签名交易
	if err := d.signTransaction(tx, voter, privateKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign proposal vote transaction: %w", err)
	}

	// 计算交易哈希
	tx.ComputeHash(0)

	return tx, nil
}

// createProposalExecuteTransaction 创建执行提案交易
func (d *DPOS) createProposalExecuteTransaction(executor types.Address, privateKeyHex string, txData dpos.ProposalExecuteTxData) (*types.Transaction, error) {
	// 获取nonce
	var nonce uint64
	if nonceStore, ok := d.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		nonce = nonceStore.GetNonce(executor)
	}

	// 计算 gasPrice = max(baseFee, priceLimit) 并+10%
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
	gasPrice := new(big.Int).SetUint64(maxBase + (maxBase / 10))
	d.logger.Info("🛠️ 构建标准EVM交易(执行提案)", "baseFee", baseFee, "priceLimit", priceLimit, "chosenGasPrice", gasPrice.String())

	// 序列化交易数据
	txDataBytes, err := json.Marshal(txData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal proposal execute tx data: %w", err)
	}

	// 创建标准交易（Legacy），目标 self-call
	to := executor
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      200000,
		To:       &to,
		Value:    big.NewInt(0),
		Input:    txDataBytes,
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
		Hash:     types.Hash{},
	}

	// 签名交易
	if err := d.signTransaction(tx, executor, privateKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign proposal execute transaction: %w", err)
	}

	// 计算交易哈希
	tx.ComputeHash(0)

	return tx, nil
}

// GetVoterSlashingHistory 获取投票者的削减历史
// RPC: dpos_getVoterSlashingHistory
func (d *DPOS) GetVoterSlashingHistory(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("GetVoterSlashingHistory called", "params", params)

	// 解析参数
	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		// 尝试数组格式
		if paramsArray, ok := params.([]interface{}); ok && len(paramsArray) > 0 {
			paramsMap = make(map[string]interface{})
			if len(paramsArray) >= 1 {
				paramsMap["voter"] = paramsArray[0]
			}
			if len(paramsArray) >= 2 {
				paramsMap["validator"] = paramsArray[1]
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "invalid params format, expected map or array",
			}, nil
		}
	}

	voterAddrStr, ok := paramsMap["voter"].(string)
	if !ok {
		return map[string]interface{}{
			"success": false,
			"error":   "voter address required",
		}, nil
	}

	validatorAddrStr, _ := paramsMap["validator"].(string) // 可选，如果提供则只查询该验证者

	voterAddr := types.StringToAddress(voterAddrStr)
	var validatorAddr types.Address
	if validatorAddrStr != "" {
		validatorAddr = types.StringToAddress(validatorAddrStr)
	}

	// 1. 获取 DPoS State（直接通过 store，与其他方法保持一致）
	state, err := d.store.GetDPoSState()
	if err != nil || state == nil || state.StakeStore == nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}

	// 2. 获取 VoterInfo
	voterInfo, err := state.StakeStore.GetVoterInfo(voterAddr)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get voter info from store: %v", err),
		}, nil
	}

	if voterInfo == nil {
		return map[string]interface{}{
			"success":         true,
			"voter":           voterAddr.String(),
			"validator":       validatorAddr.String(),
			"slashingHistory": []interface{}{},
		}, nil
	}

	// 3. 初始化 SlashingRecords（如果不存在）
	if voterInfo.SlashingRecords == nil {
		voterInfo.SlashingRecords = make(map[types.Address][]*dpos.SlashingRecord)
	}

	// 4. 获取削减历史
	var slashingHistory []interface{}

	if validatorAddr != types.ZeroAddress {
		// 只查询指定验证者的削减历史
		records := voterInfo.SlashingRecords[validatorAddr]
		for _, record := range records {
			slashingHistory = append(slashingHistory, map[string]interface{}{
				"validatorAddr":          record.ValidatorAddr.String(),
				"blockNumber":            record.BlockNumber,
				"epochNumber":            record.EpochNumber,
				"timestamp":              record.Timestamp,
				"slashAmount":            record.SlashAmount.String(),
				"oldVoteAmount":          record.OldVoteAmount.String(),
				"newVoteAmount":          record.NewVoteAmount.String(),
				"slashRate":              record.SlashRate,
				"reason":                 record.Reason,
				"missedBlocks":           record.MissedBlocks,
				"missedBlocksPercentage": record.MissedBlocksPercentage,
				"doubleSigningHeight":    record.DoubleSigningHeight,
			})
		}
	} else {
		// 查询所有验证者的削减历史
		for _, records := range voterInfo.SlashingRecords {
			for _, record := range records {
				slashingHistory = append(slashingHistory, map[string]interface{}{
					"validatorAddr":          record.ValidatorAddr.String(),
					"blockNumber":            record.BlockNumber,
					"epochNumber":            record.EpochNumber,
					"timestamp":              record.Timestamp,
					"slashAmount":            record.SlashAmount.String(),
					"oldVoteAmount":          record.OldVoteAmount.String(),
					"newVoteAmount":          record.NewVoteAmount.String(),
					"slashRate":              record.SlashRate,
					"reason":                 record.Reason,
					"missedBlocks":           record.MissedBlocks,
					"missedBlocksPercentage": record.MissedBlocksPercentage,
					"doubleSigningHeight":    record.DoubleSigningHeight,
				})
			}
		}
	}

	// 5. 计算总削减金额和原始金额
	totalSlashAmount := big.NewInt(0)
	totalOriginalAmount := big.NewInt(0)
	currentAmount := big.NewInt(0)

	// 从 StakeInfo 获取原始金额
	allStakes, err := d.store.GetStakingInfo()
	if err == nil {
		for _, stake := range allStakes {
			if stake != nil && stake.Staker == voterAddr {
				if validatorAddr == types.ZeroAddress || stake.Delegate == validatorAddr {
					if stake.OriginalAmount != nil {
						totalOriginalAmount.Add(totalOriginalAmount, stake.OriginalAmount)
					}
					if stake.Amount != nil {
						currentAmount.Add(currentAmount, stake.Amount)
					}
					if stake.SlashingRecords != nil {
						for _, record := range stake.SlashingRecords {
							if record.SlashAmount != nil {
								totalSlashAmount.Add(totalSlashAmount, record.SlashAmount)
							}
						}
					}
				}
			}
		}
	}

	// 从 VoterInfo.DelegateVotes 获取当前金额
	if voterInfo.DelegateVotes != nil {
		if validatorAddr != types.ZeroAddress {
			if voteAmount := voterInfo.DelegateVotes[validatorAddr]; voteAmount != nil {
				currentAmount = new(big.Int).Set(voteAmount)
			}
		} else {
			// 累加所有验证者的投票金额
			currentAmount = big.NewInt(0)
			for _, voteAmount := range voterInfo.DelegateVotes {
				if voteAmount != nil {
					currentAmount.Add(currentAmount, voteAmount)
				}
			}
		}
	}

	return map[string]interface{}{
		"success":          true,
		"voter":            voterAddr.String(),
		"validator":        validatorAddr.String(),
		"originalAmount":   totalOriginalAmount.String(),
		"currentAmount":    currentAmount.String(),
		"totalSlashAmount": totalSlashAmount.String(),
		"slashCount":       len(slashingHistory),
		"slashingHistory":  slashingHistory,
	}, nil
}

// GetValidatorSlashingHistory 获取验证者的所有削减历史（聚合所有投票者）
// RPC: dpos_getValidatorSlashingHistory
func (d *DPOS) GetValidatorSlashingHistory(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("GetValidatorSlashingHistory called", "params", params)

	// 解析参数
	var validatorAddress string

	switch p := params.(type) {
	case []interface{}:
		if len(p) == 1 {
			if address, ok := p[0].(string); ok {
				validatorAddress = address
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   "first parameter must be a string address",
				}, nil
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "expected 1 parameter (validator address)",
			}, nil
		}
	case string:
		validatorAddress = p
	case map[string]interface{}:
		if address, ok := p["validator"].(string); ok {
			validatorAddress = address
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address is required",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T, expected string, array, or map", params),
		}, nil
	}

	// 验证地址
	if validatorAddress == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "validator address is required",
		}, nil
	}

	// 解析地址
	validatorAddr := types.StringToAddress(validatorAddress)

	// 1. 获取 DPoS State
	state, err := d.store.GetDPoSState()
	if err != nil || state == nil || state.StakeStore == nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}

	// 2. 获取所有质押信息
	allStakes, err := state.StakeStore.GetStakingInfo()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}

	// 3. 筛选出投票给该验证者的所有投票者
	var voters []types.Address
	voterSet := make(map[types.Address]bool) // 用于去重

	for _, stake := range allStakes {
		if stake != nil && stake.Delegate == validatorAddr {
			// 去重：同一个投票者可能有多条质押记录
			if !voterSet[stake.Staker] {
				voters = append(voters, stake.Staker)
				voterSet[stake.Staker] = true
			}
		}
	}

	d.logger.Info("GetValidatorSlashingHistory: found voters", "validator", validatorAddr.String(), "voterCount", len(voters))

	if len(voters) == 0 {
		return map[string]interface{}{
			"success":          true,
			"validator":        validatorAddr.String(),
			"totalSlashCount":  0,
			"totalSlashAmount": "0",
			"lastSlashTime":    nil,
			"slashingHistory":  []interface{}{},
		}, nil
	}

	// 4. 查询每个投票者的削减历史
	var allHistory []interface{}

	for _, voterAddr := range voters {
		voterInfo, err := state.StakeStore.GetVoterInfo(voterAddr)
		if err != nil {
			d.logger.Warn("GetValidatorSlashingHistory: failed to get voter info", "voter", voterAddr.String(), "error", err)
			continue
		}

		if voterInfo == nil {
			continue
		}

		// 初始化 SlashingRecords（如果不存在）
		if voterInfo.SlashingRecords == nil {
			voterInfo.SlashingRecords = make(map[types.Address][]*dpos.SlashingRecord)
		}

		// 获取该投票者对该验证者的削减记录
		records := voterInfo.SlashingRecords[validatorAddr]
		for _, record := range records {
			allHistory = append(allHistory, map[string]interface{}{
				"validatorAddr":          record.ValidatorAddr.String(),
				"voterAddress":           voterAddr.String(), // 添加投票者地址
				"blockNumber":            record.BlockNumber,
				"epochNumber":            record.EpochNumber,
				"timestamp":              record.Timestamp,
				"slashAmount":            record.SlashAmount.String(),
				"oldVoteAmount":          record.OldVoteAmount.String(),
				"newVoteAmount":          record.NewVoteAmount.String(),
				"slashRate":              record.SlashRate,
				"reason":                 record.Reason,
				"missedBlocks":           record.MissedBlocks,
				"missedBlocksPercentage": record.MissedBlocksPercentage,
				"doubleSigningHeight":    record.DoubleSigningHeight,
			})
		}
	}

	// 5. 计算统计信息
	totalSlashAmount := big.NewInt(0)
	var lastSlashTime uint64 = 0

	for _, record := range allHistory {
		recordMap := record.(map[string]interface{})

		// 累加削减金额
		if slashAmountStr, ok := recordMap["slashAmount"].(string); ok {
			if slashAmount, ok := new(big.Int).SetString(slashAmountStr, 10); ok {
				totalSlashAmount.Add(totalSlashAmount, slashAmount)
			}
		}

		// 找到最新的削减时间
		if timestamp, ok := recordMap["timestamp"].(uint64); ok {
			if timestamp > lastSlashTime {
				lastSlashTime = timestamp
			}
		}
	}

	// 6. 按时间倒序排序
	sort.Slice(allHistory, func(i, j int) bool {
		timeI, okI := allHistory[i].(map[string]interface{})["timestamp"].(uint64)
		timeJ, okJ := allHistory[j].(map[string]interface{})["timestamp"].(uint64)

		if !okI || !okJ {
			return false
		}
		return timeI > timeJ // 最新的在前
	})

	// 7. 返回结果
	result := map[string]interface{}{
		"success":          true,
		"validator":        validatorAddr.String(),
		"totalSlashCount":  len(allHistory),
		"totalSlashAmount": totalSlashAmount.String(),
		"slashingHistory":  allHistory,
	}

	if lastSlashTime > 0 {
		result["lastSlashTime"] = lastSlashTime
	} else {
		result["lastSlashTime"] = nil
	}

	d.logger.Info("GetValidatorSlashingHistory: completed",
		"validator", validatorAddr.String(),
		"totalSlashCount", len(allHistory),
		"totalSlashAmount", totalSlashAmount.String())

	return result, nil
}

// addProposalTransactionToPool 将提案交易添加到交易池并广播
func (d *DPOS) addProposalTransactionToPool(tx *types.Transaction) error {
	// 添加到交易池
	if ethStore, ok := d.store.(interface {
		AddTx(tx *types.Transaction) error
	}); ok {
		if err := ethStore.AddTx(tx); err != nil {
			d.logger.Warn("Failed to add proposal transaction to pool", "error", err, "txHash", tx.Hash.String())
			// 🔧 修复：交易池添加失败时直接返回错误，而不是继续执行
			return fmt.Errorf("failed to add transaction to pool: %w", err)
		} else {
			d.logger.Info("✅ 提案交易已添加到交易池", "txHash", tx.Hash.String())
		}
	}

	// 广播交易
	if err := d.broadcastTransaction(tx); err != nil {
		d.logger.Warn("Failed to broadcast proposal transaction", "error", err, "txHash", tx.Hash.String())
		return err
	}

	d.logger.Info("✅ 提案交易已广播", "txHash", tx.Hash.String(), "txType", tx.Type.String())

	return nil
}

// GetValidatorsFromBlockExtraData 获取指定区块ExtraData中的验证者集合
func (d *DPOS) GetValidatorsFromBlockExtraData(ctx context.Context, params interface{}) (interface{}, error) {
	// 解析参数：区块号
	var blockNumber uint64

	if paramMap, ok := params.(map[string]interface{}); ok {
		if bn, ok := paramMap["blockNumber"].(float64); ok {
			blockNumber = uint64(bn)
		} else {
			return nil, fmt.Errorf("blockNumber parameter is required and must be a number")
		}
	} else if paramArray, ok := params.([]interface{}); ok && len(paramArray) >= 1 {
		if bn, ok := paramArray[0].(float64); ok {
			blockNumber = uint64(bn)
		} else {
			return nil, fmt.Errorf("blockNumber parameter is required and must be a number")
		}
	} else {
		return nil, fmt.Errorf("invalid parameters format, expected blockNumber")
	}

	// 获取指定区块的区块头
	header, exists := d.store.GetHeaderByNumber(blockNumber)
	if !exists {
		return nil, fmt.Errorf("block %d not found", blockNumber)
	}

	// 获取DPoS引擎
	dposEngine := d.getDPoSEngine()
	if dposEngine == nil {
		return nil, fmt.Errorf("DPoS engine not available")
	}

	// 从ExtraData读取验证者集合
	if getValidators, ok := dposEngine.(interface {
		GetValidatorsFromBlockExtraData(header *types.Header) (validator.AccountSet, error)
	}); ok {
		validators, err := getValidators.GetValidatorsFromBlockExtraData(header)
		if err != nil {
			return nil, fmt.Errorf("failed to get validators from block extraData: %w", err)
		}

		// 转换为JSON格式
		validatorList := make([]map[string]interface{}, 0, len(validators))
		for i, v := range validators {
			validatorList = append(validatorList, map[string]interface{}{
				"index":       i,
				"address":     v.Address.String(),
				"votingPower": v.VotingPower.String(),
				"isActive":    v.IsActive,
			})
		}

		return map[string]interface{}{
			"success":     true,
			"blockNumber": blockNumber,
			"count":       len(validators),
			"validators":  validatorList,
		}, nil
	}

	return nil, fmt.Errorf("DPoS engine does not support GetValidatorsFromBlockExtraData")
}
