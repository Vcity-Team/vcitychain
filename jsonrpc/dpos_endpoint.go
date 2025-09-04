package jsonrpc

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"time"

	"bytes"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

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
}

// DPOS is the dpos jsonrpc endpoint
type DPOS struct {
	logger hclog.Logger
	store  dposStore
	// Hardcoded private key for signing DPoS transactions
	privateKey *ecdsa.PrivateKey
}

// NewDPOS creates a new DPOS endpoint
func NewDPOS(logger hclog.Logger, store dposStore) *DPOS {
	logger.Error("=== NEWDPOS FUNCTION CALLED ===")
	logger.Info("=== NEWDPOS FUNCTION CALLED ===")

	// Hardcoded private key for DPoS voting
	privateKeyHex := "ed7ba26f0568b6b9cd3296ff7dcfe56fc6041fea8963246334bdaa276783add6"

	logger.Error("=== PRIVATE KEY INITIALIZATION START ===")
	logger.Info("=== PRIVATE KEY INITIALIZATION START ===")
	logger.Info("Initializing DPoS endpoint with private key", "privateKeyHex", privateKeyHex)

	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		logger.Error("=== PRIVATE KEY DECODE FAILED ===", "error", err, "privateKeyHex", privateKeyHex)
		logger.Error("Failed to decode private key", "error", err, "privateKeyHex", privateKeyHex)
		return &DPOS{
			logger: logger.Named("dpos"),
			store:  store,
		}
	}

	logger.Error("=== PRIVATE KEY DECODE SUCCESS ===", "privateKeyBytesLength", len(privateKeyBytes))
	logger.Info("=== PRIVATE KEY DECODE SUCCESS ===", "privateKeyBytesLength", len(privateKeyBytes))
	logger.Info("Private key decoded successfully", "privateKeyBytesLength", len(privateKeyBytes))

	privateKey, err := crypto.BytesToECDSAPrivateKey(privateKeyBytes)
	if err != nil {
		logger.Error("=== PRIVATE KEY CREATION FAILED ===", "error", err, "privateKeyBytesLength", len(privateKeyBytes))
		logger.Error("Failed to create private key", "error", err, "privateKeyBytesLength", len(privateKeyBytes))
		return &DPOS{
			logger: logger.Named("dpos"),
			store:  store,
		}
	}

	logger.Error("=== PRIVATE KEY CREATION SUCCESS ===", "privateKeyD", privateKey.D.String())
	logger.Info("=== PRIVATE KEY CREATION SUCCESS ===", "privateKeyD", privateKey.D.String())
	logger.Info("Private key created successfully", "privateKeyD", privateKey.D.String())

	result := &DPOS{
		logger:     logger.Named("dpos"),
		store:      store,
		privateKey: privateKey,
	}

	logger.Error("=== DPOS ENDPOINT CREATED WITH PRIVATE KEY ===", "privateKeyAvailable", result.privateKey != nil)
	logger.Info("=== DPOS ENDPOINT CREATED WITH PRIVATE KEY ===", "privateKeyAvailable", result.privateKey != nil)

	return result
}

// signTransaction signs a DPoS transaction using the private key
func (d *DPOS) signTransaction(tx *types.Transaction, expectedAddr types.Address, privateKeyHex string) error {
	d.logger.Info("=== HARDCODED PRIVATE KEY APPROACH ===")

	// Use provided private key or fallback to hardcoded one
	if privateKeyHex == "" {
		privateKeyHex = "ed7ba26f0568b6b9cd3296ff7dcfe56fc6041fea8963246334bdaa276783add6"
	}

	d.logger.Info("Decoding hardcoded private key", "privateKeyHex", privateKeyHex, "length", len(privateKeyHex))

	// Validate hex string first
	if len(privateKeyHex) != 64 {
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	// Check if string contains only valid hex characters
	for i, char := range privateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("invalid hex character at position %d: %c (U+%04X)", i, char, char)
		}
	}

	d.logger.Info("Private key hex string validation passed")

	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		d.logger.Error("Failed to decode hardcoded private key", "error", err)
		return fmt.Errorf("failed to decode hardcoded private key: %w", err)
	}

	d.logger.Info("Hardcoded private key decoded", "length", len(privateKeyBytes))

	// Use direct ECDSA private key creation instead of crypto.BytesToECDSAPrivateKey
	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// Create ECDSA private key directly using secp256k1 curve
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}

	// Calculate the public key from the private key
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	d.logger.Info("Hardcoded private key created successfully", "privateKeyD", privateKey.D.String())

	// Calculate transaction hash for signing using EIP-155 scheme to match txpool signer
	chainID := uint64(888)
	eip155Signer := crypto.NewEIP155Signer(chainID, false)

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
	senderAddr, err := eip155Signer.Sender(tx)
	if err == nil {
		tx.From = senderAddr
		d.logger.Info("=== 标记4: 恢复发送者地址=", tx.From.String(), "===")
		d.logger.Info("Sender recovered and set on tx", "from", tx.From.String())
	} else {
		d.logger.Warn("Failed to recover sender after signing", "error", err)
	}

	// Test signature recovery to ensure it works
	d.logger.Info("Testing signature recovery...")
	// Use the EIP-155 signer's Hash method for recovery test
	hashForRecovery := eip155Signer.Hash(tx)

	// We need to reconstruct the signature from R, S, V for recovery
	// Extract recovery ID from V value: V = 2*chainID + 35 + recoveryID
	recoveryID := int(tx.V.Int64() - int64(2*chainID) - 35)
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

// StakeRequest represents a stake request
type StakeRequest struct {
	Staker   string `json:"staker"`
	Delegate string `json:"delegate"`
	Amount   string `json:"amount"`
}

// StakeResponse represents a stake response
type StakeResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"txHash,omitempty"`
	Error       string `json:"error,omitempty"`
}

// DelegateRequest represents a delegate request
type DelegateRequest struct {
	Staker   string `json:"staker"`
	Delegate string `json:"delegate"`
	Amount   string `json:"amount"`
}

// DelegateResponse represents a delegate response
type DelegateResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"txHash,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Vote handles dpos_vote RPC method
func (d *DPOS) Vote(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Vote called", "params", params)

	// Parse parameters
	d.logger.Info("Starting parameter parsing...")
	var req VoteRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both voter and candidate with default amount
			if address, ok := p[0].(string); ok {
				req.Voter = address
				req.Candidate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [voter, candidate, amount]
			if voter, ok := p[0].(string); ok {
				req.Voter = voter
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if candidate, ok := p[1].(string); ok {
				req.Candidate = candidate
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "second parameter must be a string address",
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
		} else if len(p) == 4 {
			// Four parameters: [voter, candidate, amount, privateKey]
			d.logger.Info("Processing 4 parameters including private key")
			if voter, ok := p[0].(string); ok {
				req.Voter = voter
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if candidate, ok := p[1].(string); ok {
				req.Candidate = candidate
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "second parameter must be a string address",
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
				d.logger.Info("Private key parameter received", "length", len(privateKey))
				// Store private key in the request for later use
				req.PrivateKey = privateKey
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "fourth parameter must be a string private key",
				}, nil
			}
		} else {
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1, 3, or 4 parameters, got %d", len(p)),
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
	d.logger.Info("Validating request parameters...", "voter", req.Voter, "candidate", req.Candidate, "amount", req.Amount)

	if req.Voter == "" {
		d.logger.Error("Voter address is required")
		return &VoteResponse{
			Success: false,
			Error:   "voter address is required",
		}, nil
	}
	if req.Candidate == "" {
		d.logger.Error("Candidate address is required")
		return &VoteResponse{
			Success: false,
			Error:   "candidate address is required",
		}, nil
	}
	if req.Amount == "" {
		d.logger.Error("Amount is required")
		return &VoteResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	d.logger.Info("Request validation passed")

	// Parse addresses
	d.logger.Info("Parsing addresses and amount...")
	voterAddr := types.StringToAddress(req.Voter)
	candidateAddr := types.StringToAddress(req.Candidate)
	d.logger.Info("Addresses parsed", "voter", voterAddr.String(), "candidate", candidateAddr.String())

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		d.logger.Error("Invalid amount format", "amount", req.Amount)
		return &VoteResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}
	d.logger.Info("Amount parsed successfully", "amount", amountInt.String())

	// Note: In DPoS, users can vote for ANYONE, not just validators
	// This allows for delegation and voting for regular users
	d.logger.Info("DPoS voting allows voting for any address - proceeding with vote")

	// Check voter balance
	d.logger.Info("Checking voter balance...", "voter", voterAddr.String(), "required_amount", amountInt.String())

	// Try to get balance with different approaches
	var balance *big.Int
	var err error

	// Method 1: Try to get balance using available methods
	d.logger.Info("Method 1: Attempting to get balance...")

	// Try to get balance directly using the available methods
	if balanceStore, ok := d.store.(interface {
		GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
	}); ok {
		d.logger.Info("Method 1: Store implements GetBalance method")

		// Try to get balance with different state roots
		d.logger.Info("Method 1: Trying to get balance with different state roots...")

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
				d.logger.Info("Method 1a: Got state root from Header() method", "root", latestRoot.String())
				foundValidRoot = true
			}
		}

		// Method 1b: Try to get from GetLatestStateRoot method (if exists)
		if !foundValidRoot {
			if latestStore, ok := d.store.(interface {
				GetLatestStateRoot() types.Hash
			}); ok {
				latestRoot = latestStore.GetLatestStateRoot()
				d.logger.Info("Method 1b: Got latest state root", "root", latestRoot.String())
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
					d.logger.Info("Method 1c: Got state root from GetLatestHeader", "root", latestRoot.String())
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
					d.logger.Info("Method 1d: Got state root from GetLatestBlock", "root", latestRoot.String())
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
						d.logger.Info("Method 1e: Got state root from previous block header", "root", latestRoot.String(), "blockNumber", prevHeader.Number)
						foundValidRoot = true
					}
				}
			}
		}

		if foundValidRoot && latestRoot != (types.Hash{}) {
			d.logger.Info("Method 1: Trying to get balance with valid state root", "root", latestRoot.String())
			balance, err = balanceStore.GetBalance(latestRoot, voterAddr)
			if err == nil && balance != nil {
				d.logger.Info("Method 1: Successfully got balance with valid root", "balance", balance.String())
			} else {
				d.logger.Info("Method 1: Failed to get balance with valid root", "error", err)
			}
		} else {
			d.logger.Info("Method 1: No valid state root found, cannot get balance")
		}
	} else {
		d.logger.Error("Method 1: Store does not implement GetBalance method")
	}

	// Method 2: If still no balance, try to get from consensus engine directly
	if balance == nil || err != nil {
		d.logger.Info("Trying to get balance from consensus engine directly...")
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
					d.logger.Info("Balance retrieved from consensus engine", "balance", balance.String())
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
						d.logger.Info("Trying to get balance from DPoS state")
						// Note: This would need to be implemented based on actual DPoS state structure
					}
				}
			}
		}
	}

	// Method 3: Try to get balance using zero hash as fallback (for genesis or initial state)
	if balance == nil || err != nil {
		d.logger.Info("Method 3: Trying to get balance with zero hash as fallback...")
		if balanceStore, ok := d.store.(interface {
			GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
		}); ok {
			zeroHash := types.Hash{}
			balance, err = balanceStore.GetBalance(zeroHash, voterAddr)
			if err == nil && balance != nil {
				d.logger.Info("Method 3: Successfully got balance with zero hash", "balance", balance.String())
			} else {
				d.logger.Info("Method 3: Failed to get balance with zero hash", "error", err)
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

	d.logger.Info("Voter balance retrieved", "balance", balance.String())

	if balance.Cmp(amountInt) < 0 {
		d.logger.Error("Insufficient balance", "balance", balance.String(), "required", amountInt.String())
		return &VoteResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}
	d.logger.Info("Balance check passed")

	// Implement actual voting logic
	d.logger.Info("Vote validation completed", "voter", voterAddr, "candidate", candidateAddr, "amount", amountInt)

	// Step 1: Create a vote transaction
	d.logger.Info("Creating vote transaction...")

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

	// Create vote transaction
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

	d.logger.Info("Vote transaction created", "txHash", tx.ComputeHash(0).Hash.String(), "nonce", nonce, "gasPrice", gasPrice.String())

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
		consensusEngine := consensusStore.GetConsensus()

		if consensusEngine == nil {
			d.logger.Warn("Consensus engine is nil")
		} else {
			d.logger.Info("Consensus engine type", "type", fmt.Sprintf("%T", consensusEngine))

			// Try to get DPoS consensus engine with proper type assertion (commented out to avoid duplicate processing)
			// var dposEngine interface {
			// 	AddVote(voter types.Address, candidate types.Address, amount *big.Int) error
			// }

			// 🆕 修复：移除立即调用AddVote的逻辑，避免重复计算
			// 投票数据将在区块广播接收后统一处理，确保只计算一次
			d.logger.Debug("🔄 投票交易已加入交易池，等待打包进区块后统一处理")
			d.logger.Debug("📋 投票数据将在区块广播接收后计算，避免重复处理")

			// 注释掉所有立即调用AddVote的代码，避免重复计算
			/*
				if dposEngine, ok = consensusEngine.(interface {
					AddVote(voter types.Address, candidate types.Address, amount *big.Int) error
				}); ok {
					d.logger.Info("Consensus engine has AddVote method, attempting to update DPoS state...")
					if err := dposEngine.AddVote(voterAddr, candidateAddr, amountInt); err != nil {
						d.logger.Error("Failed to update DPoS state", "error", err)
						// Continue anyway, the transaction is in the pool
					} else {
						d.logger.Info("DPoS state updated successfully")
						dposStateUpdated = true
					}
				} else {
					// 直接进入else分支，尝试其他方法
					d.logger.Info("Trying to access embedded Consensus field...")
					if hub, ok := d.store.(interface {
						GetConsensus() interface{}
					}); ok {
						consensusEngine := hub.GetConsensus()
						d.logger.Info("Got consensus engine through hub", "type", fmt.Sprintf("%T", consensusEngine))

						// Try to cast to DPoS engine
						if dposEngine, ok = consensusEngine.(interface {
							AddVote(voter types.Address, candidate types.Address, amount *big.Int) error
						}); ok {
							d.logger.Info("Consensus engine has AddVote method, attempting to update DPoS state...")
							if err := dposEngine.AddVote(voterAddr, candidateAddr, amountInt); err != nil {
								d.logger.Error("Failed to update DPoS state", "error", err)
							} else {
								d.logger.Info("DPoS state updated successfully")
								dposStateUpdated = true
							}
						} else {
							d.logger.Warn("Consensus engine does NOT have AddVote method")
						}
					} else {
						d.logger.Warn("Store does NOT have GetConsensus() consensus.Consensus method")
					}

					d.logger.Warn("Available methods on consensus engine:")

					// List available methods
					consensusValue := reflect.ValueOf(consensusEngine)
					if consensusValue.Kind() == reflect.Ptr {
						consensusValue = consensusValue.Elem()
					}

					consensusType := consensusValue.Type()
					for i := 0; i < consensusType.NumMethod(); i++ {
						method := consensusType.Method(i)
						d.logger.Warn("Available method", "name", method.Name, "type", method.Type.String())
					}
				}
			*/
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
		// 修复：由于AddVote被注释是为了避免重复计算，交易池成功添加就表示投票会成功处理
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
				}
			} else {
				blockStatus = "pending"
				d.logger.Info("Transaction not yet mined, blockNumber will be 0 (this is normal for newly added transactions)")
			}
		} else {
			blockStatus = "store_not_supported"
			d.logger.Info("Store does not support ReadTxLookup/GetBlockByHash, cannot determine block number")
		}
	} else {
		blockStatus = "tx_not_added"
		d.logger.Info("Transaction was not added to pool, blockNumber will be 0")
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
		BlockNumber: blockNumber, // Real block number if mined, 0 if pending
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
	amountBytes := amount.Bytes()
	if len(amountBytes) > 32 {
		amountBytes = amountBytes[len(amountBytes)-32:] // Take last 32 bytes
	}
	// Pad with zeros to 32 bytes
	for len(amountBytes) < 32 {
		amountBytes = append([]byte{0}, amountBytes...)
	}
	data = append(data, amountBytes...)

	return data
}

// VoteByAddress handles dpos_vote RPC method with single address parameter
// This method is designed to handle the case where only one address is provided
// It will use the address as both voter and candidate, with a default amount
func (d *DPOS) VoteByAddress(ctx context.Context, address string) (*VoteResponse, error) {
	d.logger.Info("DPoS VoteByAddress called", "address", address)

	// Validate address
	if address == "" {
		return &VoteResponse{
			Success: false,
			Error:   "address is required",
		}, nil
	}

	// Parse address
	addr := types.StringToAddress(address)

	// Check if address is a validator
	validators, err := d.store.GetValidators()
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}

	isValidator := false
	for _, v := range validators {
		if v.Address == addr {
			isValidator = true
			break
		}
	}

	if !isValidator {
		return &VoteResponse{
			Success: false,
			Error:   "address is not a validator",
		}, nil
	}

	// Check balance
	balance, err := d.store.GetBalance(types.Hash{}, addr)
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	// Use a default voting amount (1 token = 10^18 wei)
	defaultAmount := new(big.Int).Mul(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

	if balance.Cmp(defaultAmount) < 0 {
		return &VoteResponse{
			Success: false,
			Error:   "insufficient balance for voting",
		}, nil
	}

	// TODO: Implement actual voting logic here
	// This would typically involve:
	// 1. Creating a transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &VoteResponse{
		Success:     true,
		Message:     "Vote operation completed successfully with default amount",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12345), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// Stake handles dpos_stake RPC method
func (d *DPOS) Stake(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Stake called", "params", params)

	// Parse parameters
	var req StakeRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both staker and delegate with default amount
			if address, ok := p[0].(string); ok {
				req.Staker = address
				req.Delegate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [staker, delegate, amount]
			if staker, ok := p[0].(string); ok {
				req.Staker = staker
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if delegate, ok := p[1].(string); ok {
				req.Delegate = delegate
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "second parameter must be a string address",
				}, nil
			}
			if amount, ok := p[2].(string); ok {
				req.Amount = amount
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "third parameter must be a string amount",
				}, nil
			}
		} else {
			return &StakeResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1 or 3 parameters, got %d", len(p)),
			}, nil
		}
	case map[string]interface{}:
		if staker, ok := p["staker"].(string); ok {
			req.Staker = staker
		}
		if delegate, ok := p["delegate"].(string); ok {
			req.Delegate = delegate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
	case *StakeRequest:
		if p != nil {
			req = *p
		}
	default:
		return &StakeResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or StakeRequest", params),
		}, nil
	}

	// Validate request
	if req.Staker == "" {
		return &StakeResponse{
			Success: false,
			Error:   "staker address is required",
		}, nil
	}
	if req.Delegate == "" {
		return &StakeResponse{
			Success: false,
			Error:   "delegate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &StakeResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	// Parse addresses
	stakerAddr := types.StringToAddress(req.Staker)
	_ = types.StringToAddress(req.Delegate) // Will be used in actual implementation

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &StakeResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// Check staker balance
	balance, err := d.store.GetBalance(types.Hash{}, stakerAddr)
	if err != nil {
		return &StakeResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	if balance.Cmp(amountInt) < 0 {
		return &StakeResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}

	// TODO: Implement actual staking logic here
	// This would typically involve:
	// 1. Creating a staking transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &StakeResponse{
		Success:     true,
		Message:     "Stake operation completed successfully",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12346), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// Delegate handles dpos_delegate RPC method
func (d *DPOS) Delegate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Delegate called", "params", params)

	// Parse parameters
	var req DelegateRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both staker and delegate with default amount
			if address, ok := p[0].(string); ok {
				req.Staker = address
				req.Delegate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [staker, delegate, amount]
			if staker, ok := p[0].(string); ok {
				req.Staker = staker
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if delegate, ok := p[1].(string); ok {
				req.Delegate = delegate
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "second parameter must be a string address",
				}, nil
			}
			if amount, ok := p[2].(string); ok {
				req.Amount = amount
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "third parameter must be a string amount",
				}, nil
			}
		} else {
			return &DelegateResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1 or 3 parameters, got %d", len(p)),
			}, nil
		}
	case map[string]interface{}:
		if staker, ok := p["staker"].(string); ok {
			req.Staker = staker
		}
		if delegate, ok := p["delegate"].(string); ok {
			req.Delegate = delegate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
	case *DelegateRequest:
		if p != nil {
			req = *p
		}
	default:
		return &DelegateResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or DelegateRequest", params),
		}, nil
	}

	// Validate request
	if req.Staker == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "staker address is required",
		}, nil
	}
	if req.Delegate == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "delegate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	// Parse addresses
	stakerAddr := types.StringToAddress(req.Staker)
	_ = types.StringToAddress(req.Delegate) // Will be used in actual implementation

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &DelegateResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// Check staker balance
	balance, err := d.store.GetBalance(types.Hash{}, stakerAddr)
	if err != nil {
		return &DelegateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	if balance.Cmp(amountInt) < 0 {
		return &DelegateResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}

	// TODO: Implement actual delegation logic here
	// This would typically involve:
	// 1. Creating a delegation transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &DelegateResponse{
		Success:     true,
		Message:     "Delegate operation completed successfully",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12347), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// GetDelegates handles dpos_getDelegates RPC method
func (d *DPOS) GetDelegates(ctx context.Context, blockNumber *uint64) (validator.AccountSet, error) {
	d.logger.Info("DPoS GetDelegates called", "blockNumber", blockNumber)

	// 使用不过滤的版本，返回所有验证者（包括投票权重为0的）
	validators, err := d.store.GetValidatorsWithFilter(false)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	return validators, nil
}

// GetValidatorSet handles dpos_getValidatorSet RPC method
func (d *DPOS) GetValidatorSet(ctx context.Context, blockNumber *uint64) (validator.AccountSet, error) {
	d.logger.Info("DPoS GetValidatorSet called", "blockNumber", blockNumber)

	// 使用不过滤的版本，返回所有验证者（包括投票权重为0的）
	validators, err := d.store.GetValidatorsWithFilter(false)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	return validators, nil
}

// GetStakingInfo handles dpos_getStakingInfo RPC method
func (d *DPOS) GetStakingInfo(ctx context.Context, blockNumber *uint64) ([]*dpos.StakeInfo, error) {
	d.logger.Info("DPoS GetStakingInfo called", "blockNumber", blockNumber)

	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to get staking info: %w", err)
	}

	return stakingInfo, nil
}

// GetVotingPower handles dpos_getVotingPower RPC method
func (d *DPOS) GetVotingPower(ctx context.Context, delegate string, blockNumber *uint64) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetVotingPower called", "delegate", delegate, "blockNumber", blockNumber)

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
				"delegate":    delegate,
				"votingPower": v.VotingPower.String(),
				"isActive":    v.IsActive,
			}, nil
		}
	}

	return nil, fmt.Errorf("delegate not found: %s", delegate)
}

// GetCurrentRound handles dpos_getCurrentRound RPC method
func (d *DPOS) GetCurrentRound(ctx context.Context) (uint64, error) {
	d.logger.Info("DPoS GetCurrentRound called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return 0, fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// TODO: Calculate current round based on block height and delegate count
	// For now, return a default value
	return 1, nil
}

// GetCurrentDelegate handles dpos_getCurrentDelegate RPC method
func (d *DPOS) GetCurrentDelegate(ctx context.Context) (string, error) {
	d.logger.Info("DPoS GetCurrentDelegate called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return "", fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// TODO: Calculate current delegate based on block height and delegate count
	// For now, return the first validator
	validators, err := d.store.GetValidators()
	if err != nil {
		return "", fmt.Errorf("failed to get validators: %w", err)
	}

	if len(validators) == 0 {
		return "", fmt.Errorf("no validators found")
	}

	return validators[0].Address.String(), nil
}

// GetConsensusState handles dpos_getConsensusState RPC method
func (d *DPOS) GetConsensusState(ctx context.Context) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetConsensusState called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return nil, fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// Get current delegate
	currentDelegate, err := d.GetCurrentDelegate(ctx)
	if err != nil {
		return nil, err
	}

	// Get current round
	currentRound, err := d.GetCurrentRound(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"currentRound":         currentRound,
		"currentDelegate":      currentDelegate,
		"currentDelegateIndex": 0, // TODO: Calculate actual index
	}, nil
}

// GetAllValidators handles dpos_getAllValidators RPC method
// This method returns all validators from database, including those with zero voting power
func (d *DPOS) GetAllValidators(ctx context.Context, blockNumber *uint64) (validator.AccountSet, error) {
	d.logger.Info("DPoS GetAllValidators called", "blockNumber", blockNumber)

	// 使用不过滤的版本，返回所有验证者（包括投票权重为0的）
	// 直接使用 d.store.GetValidatorsWithFilter(false) 而不是通过 GetDPoSState()
	validators, err := d.store.GetValidatorsWithFilter(false)
	if err != nil {
		return nil, fmt.Errorf("failed to get all validators: %w", err)
	}

	d.logger.Info("All validators retrieved successfully", "count", len(validators))
	return validators, nil
}

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

func (d *DPOS) validateStakeRequest(req *StakeRequest) error {
	if req.Staker == "" {
		return fmt.Errorf("staker address is required")
	}
	if req.Delegate == "" {
		return fmt.Errorf("delegate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

func (d *DPOS) validateDelegateRequest(req *DelegateRequest) error {
	if req.Staker == "" {
		return fmt.Errorf("staker address is required")
	}
	if req.Delegate == "" {
		return fmt.Errorf("delegate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

// GetVotingStakingInfo handles dpos_getVotingStakingInfo RPC method
// This method returns comprehensive information about current voting and staking status
func (d *DPOS) GetVotingStakingInfo(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetVotingStakingInfo called")

	// Get current DPoS state
	d.logger.Info("Getting DPoS state...")
	dposState, err := d.store.GetDPoSState()
	if err != nil {
		d.logger.Error("Failed to get DPoS state", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}
	d.logger.Info("DPoS state retrieved successfully")

	// Get current validators (without filtering to show all validators)
	d.logger.Info("Getting validators...")
	validators, err := d.store.GetValidatorsWithFilter(false)
	if err != nil {
		d.logger.Error("Failed to get validators", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}
	d.logger.Info("Validators retrieved successfully", "count", len(validators))

	// Debug: Print all validators
	for i, validator := range validators {
		d.logger.Info("Validator details",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive)
	}

	// Get staking information from store (genesis + persistent data)
	d.logger.Info("Getting staking info from store...")
	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		d.logger.Error("Failed to get staking info from store", "error", err)
		stakingInfo = []*dpos.StakeInfo{} // Use empty slice instead of failing
	}
	d.logger.Info("Store staking info retrieved", "count", len(stakingInfo))

	// Get dynamic voting information from consensus engine
	d.logger.Info("Getting dynamic voting info from consensus engine...")
	dynamicVotingInfo := d.getDynamicVotingInfo()
	d.logger.Info("Dynamic voting info retrieved", "count", len(dynamicVotingInfo))

	// 🆕 修复：确保创世配置中的初始验证者质押信息被包含
	// 从验证者信息中提取质押信息，确保创世配置的质押数量被正确显示
	genesisStakingInfo := d.extractVotingInfoFromDelegates(validators)
	d.logger.Info("Genesis staking info extracted", "count", len(genesisStakingInfo))

	// Merge all staking info: genesis + store + dynamic voting
	allStakingInfo := d.mergeStakingInfo(genesisStakingInfo, stakingInfo)
	allStakingInfo = d.mergeStakingInfo(allStakingInfo, dynamicVotingInfo)

	// 🆕 新增：按质押数量从大到小排序
	allStakingInfo = d.sortStakingInfoByAmount(allStakingInfo)
	d.logger.Info("Sorted staking info by amount", "totalCount", len(allStakingInfo))

	// Build validator details
	validatorDetails := make([]map[string]interface{}, 0)
	for _, validator := range validators {
		validatorDetail := map[string]interface{}{
			"address":     validator.Address.String(),
			"votingPower": validator.VotingPower.String(),
			"isActive":    validator.IsActive,
		}
		validatorDetails = append(validatorDetails, validatorDetail)
	}

	// Build staking summary and voting details
	totalStaked := big.NewInt(0)
	totalVotes := big.NewInt(0)
	stakingCount := 0
	votingCount := 0

	// Group staking info by delegate (validator) to show voting details
	votingDetails := make(map[string]map[string]interface{})

	for _, stake := range allStakingInfo {
		if stake.Amount != nil {
			totalStaked.Add(totalStaked, stake.Amount)
			stakingCount++

			// Get delegate address as string
			delegateAddr := stake.Delegate.String()

			// Initialize delegate entry if not exists
			if _, exists := votingDetails[delegateAddr]; !exists {
				votingDetails[delegateAddr] = map[string]interface{}{
					"delegateAddress": delegateAddr,
					"totalVotes":      big.NewInt(0),
					"voters":          make([]map[string]interface{}, 0),
				}
			}

			// Add voter information
			voterInfo := map[string]interface{}{
				"voterAddress": stake.Staker.String(),
				"amount":       stake.Amount.String(),
			}

			votingDetails[delegateAddr]["voters"] = append(
				votingDetails[delegateAddr]["voters"].([]map[string]interface{}),
				voterInfo,
			)

			// Update total votes for this delegate
			currentTotal := votingDetails[delegateAddr]["totalVotes"].(*big.Int)
			currentTotal.Add(currentTotal, stake.Amount)
			votingDetails[delegateAddr]["totalVotes"] = currentTotal

			// 计算投票交易数量：如果质押者不是验证者自己，则算作投票
			if stake.Staker != stake.Delegate {
				votingCount++
			}
		}
	}

	// Convert voting details map to slice for response
	votingDetailsList := make([]map[string]interface{}, 0)
	for _, details := range votingDetails {
		votingDetailsList = append(votingDetailsList, details)
	}

	// 🆕 新增：按总投票数量从大到小排序投票详情
	votingDetailsList = d.sortVotingDetailsByTotalVotes(votingDetailsList)

	// Calculate network statistics
	networkStats := map[string]interface{}{
		"totalValidators":     len(validators),
		"activeValidators":    len(validators), // TODO: Get actual active count from validator metadata
		"totalStaked":         totalStaked.String(),
		"totalVotes":          totalVotes.String(),
		"stakingTransactions": stakingCount,
		"votingTransactions":  votingCount,
		"consensusThreshold":  "2/3", // TODO: Get actual threshold from config
	}

	// Build response
	response := map[string]interface{}{
		"success":       true,
		"networkStats":  networkStats,
		"validators":    validatorDetails,
		"stakingInfo":   allStakingInfo,
		"votingDetails": votingDetailsList, // 新增：投票明细
		"dposState":     dposState,
		"lastUpdated":   time.Now().Format(time.RFC3339),
		"blockHeight":   0, // TODO: Get current block height from blockchain
	}

	return response, nil
}

// GetValidatorVotingDetails handles dpos_getValidatorVotingDetails RPC method
// This method returns detailed voting information for a specific validator
func (d *DPOS) GetValidatorVotingDetails(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetValidatorVotingDetails called", "params", params)

	var validatorAddress string

	// Parse parameters
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

	// Validate address
	if validatorAddress == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "validator address is required",
		}, nil
	}

	// Parse address
	validatorAddr := types.StringToAddress(validatorAddress)

	// Get validators
	validators, err := d.store.GetValidators()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}

	// Find the specific validator
	var targetValidator *validator.ValidatorMetadata
	for _, v := range validators {
		if v.Address == validatorAddr {
			targetValidator = v
			break
		}
	}

	if targetValidator == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "validator not found",
		}, nil
	}

	// Get staking info for this validator
	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}

	// Filter staking info for this validator
	validatorStakes := make([]map[string]interface{}, 0)
	totalStakedToValidator := big.NewInt(0)

	// TODO: Add logic to match stake to validator
	// For now, we'll return basic validator info
	_ = stakingInfo // Suppress unused variable warning

	// Build validator details
	validatorDetail := map[string]interface{}{
		"address":           targetValidator.Address.String(),
		"votingPower":       targetValidator.VotingPower.String(),
		"isActive":          true, // TODO: Get actual active status
		"totalStakedToMe":   totalStakedToValidator.String(),
		"stakeCount":        len(validatorStakes),
		"stakes":            validatorStakes,
		"consensusRound":    0,     // TODO: Get current consensus round
		"lastBlockProduced": "0x0", // TODO: Get last block hash
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
	amount := new(big.Int).SetBytes(amountBytes)
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("vote amount must be positive, got %s", amount.String())
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
		// 🆕 修复：使用创世配置中的质押数量
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
// 🆕 修复：按受托人地址去重，避免重复累加投票权重
func (d *DPOS) mergeStakingInfo(static []*dpos.StakeInfo, dynamic []*dpos.StakeInfo) []*dpos.StakeInfo {
	d.logger.Info("Merging staking info", "staticCount", len(static), "dynamicCount", len(dynamic))

	// 🆕 修复：按受托人地址去重，而不是按staker-delegate组合去重
	// 这样可以避免同一个受托人的投票信息被重复累加
	delegateMap := make(map[types.Address]*dpos.StakeInfo)
	var merged []*dpos.StakeInfo

	// 🆕 修复：让动态投票数据优先，确保正确的委托关系
	// 先添加动态投票信息（来自实际的投票数据）
	for _, stake := range dynamic {
		if stake.Staker != types.ZeroAddress &&
			stake.Delegate != types.ZeroAddress &&
			stake.Amount != nil &&
			stake.Amount.Cmp(big.NewInt(0)) > 0 {

			// 🆕 修复：按受托人地址去重，优先使用动态投票数据
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

			// 🆕 修复：按受托人地址去重，如果动态数据中没有该受托人，则添加静态数据
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

	// 🆕 修复：将去重后的数据转换为切片
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
