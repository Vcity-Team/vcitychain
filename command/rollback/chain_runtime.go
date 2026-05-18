package rollback

import (
	"fmt"
	"path/filepath"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/blockchain/storage/leveldb"
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/command/server/config"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/state"
	itrie "github.com/Vcity-Team/vcitychain/state/immutable-trie"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

type rollbackChainRuntime struct {
	blockchain   *blockchain.Blockchain
	trieStorage  itrie.Storage
	stateStorage state.State
	logger       hclog.Logger
}

func (r *rollbackChainRuntime) close() {
	if r.trieStorage != nil {
		_ = r.trieStorage.Close()
	}
}

func openRollbackChainRuntime(dataDir, genesisPath string, logger hclog.Logger) (*rollbackChainRuntime, error) {
	if genesisPath == "" {
		return nil, fmt.Errorf("genesis chain config path is required for state heal (use --config or place genesis.json in data-dir)")
	}

	chainCfg, err := chain.ImportFromFile(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("load chain config: %w", err)
	}

	trieStorage, err := itrie.NewLevelDBStorage(filepath.Join(dataDir, "trie"), logger)
	if err != nil {
		return nil, fmt.Errorf("open trie db: %w", err)
	}

	st := itrie.NewState(trieStorage)
	executor := state.NewExecutor(chainCfg.Params, st, logger)

	blockchainDB, err := leveldb.NewLevelDBStorage(filepath.Join(dataDir, "blockchain"), logger)
	if err != nil {
		_ = trieStorage.Close()
		return nil, fmt.Errorf("open blockchain db: %w", err)
	}

	signer := crypto.NewLondonSigner(
		uint64(chainCfg.Params.ChainID),
		chainCfg.Params.Forks.IsActive(chain.Homestead, 0),
		crypto.NewEIP155Signer(
			uint64(chainCfg.Params.ChainID),
			chainCfg.Params.Forks.IsActive(chain.Homestead, 0),
		),
	)

	bc, err := blockchain.NewBlockchain(
		logger,
		blockchainDB,
		chainCfg,
		blockchain.NewNoopRollbackVerifier(),
		executor,
		signer,
	)
	if err != nil {
		_ = trieStorage.Close()
		return nil, fmt.Errorf("create blockchain: %w", err)
	}

	if err := bc.ComputeGenesis(); err != nil {
		_ = trieStorage.Close()
		return nil, fmt.Errorf("load chain head: %w", err)
	}

	executor.GetHash = bc.GetHashHelper

	return &rollbackChainRuntime{
		blockchain:   bc,
		trieStorage:  trieStorage,
		stateStorage: st,
		logger:       logger,
	}, nil
}

func resolveGenesisPath(dataDir, configPath string) (string, error) {
	if configPath != "" {
		cfg, err := config.ReadConfigFile(configPath)
		if err != nil {
			return "", err
		}

		if cfg.GenesisPath != "" {
			return cfg.GenesisPath, nil
		}
	}

	candidates := []string{
		filepath.Join(dataDir, "genesis.json"),
		filepath.Join(dataDir, "chain.json"),
	}

	for _, candidate := range candidates {
		if common.FileExists(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no genesis config found (set --config with chain_config or add genesis.json under data-dir)")
}

func (p *rollbackParams) collectBlocksAboveTarget(
	storageInstance interface {
		ReadCanonicalHash(uint64) (types.Hash, bool)
		ReadHeader(types.Hash) (*types.Header, error)
		ReadBody(types.Hash) (*types.Body, error)
	},
) ([]*types.Block, error) {
	var blocks []*types.Block

	for height := p.targetHeight + 1; height <= p.currentHeight; height++ {
		blockHash, ok := storageInstance.ReadCanonicalHash(height)
		if !ok {
			continue
		}

		header, err := storageInstance.ReadHeader(blockHash)
		if err != nil {
			continue
		}

		body, err := storageInstance.ReadBody(blockHash)
		if err != nil {
			continue
		}

		blocks = append(blocks, &types.Block{
			Header:       header,
			Transactions: body.Transactions,
			Uncles:       body.Uncles,
		})
	}

	return blocks, nil
}

func (p *rollbackParams) finalizeExecutionState(
	logger hclog.Logger,
	blocksToReplay []*types.Block,
) (*blockchain.FinalizeManualRollbackResult, error) {
	genesisPath, err := resolveGenesisPath(p.dataDir, p.configPath)
	if err != nil {
		return nil, err
	}

	runtime, err := openRollbackChainRuntime(p.dataDir, genesisPath, logger)
	if err != nil {
		return nil, err
	}
	defer runtime.close()

	return runtime.blockchain.FinalizeManualRollback(runtime.trieStorage, p.targetHeight, blocksToReplay)
}
