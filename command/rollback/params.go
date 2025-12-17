package rollback

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain/storage"
	"github.com/Vcity-Team/vcitychain/blockchain/storage/leveldb"
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/server/config"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	bolt "go.etcd.io/bbolt"
)

const (
	configFlag       = "config"
	dataDirFlag      = "data-dir"
	targetHeightFlag = "target-height"
	forceFlag        = "force"
	keepBlocksFlag   = "keep-blocks"
)

var (
	params = &rollbackParams{}
)

var (
	errInvalidDataDir      = errors.New("data directory is required")
	errInvalidTargetHeight = errors.New("target height is required")
	errNodeRunning         = errors.New("node appears to be running, please stop it first")
	errTargetHeightInvalid = errors.New("target height must be >= 0 and <= current height")
	errTargetBlockNotFound = errors.New("target block not found")
)

type rollbackParams struct {
	configPath      string
	dataDir         string
	targetHeightRaw string
	targetHeight    uint64
	force           bool
	keepBlocks      bool

	currentHeight uint64
	targetHash    types.Hash
	blocksDeleted uint64

	// Epoch计算相关参数
	consensusSwitchHeight uint64
	epochSize             uint64
	blockTime             time.Duration
}

func (p *rollbackParams) getRequiredFlags() []string {
	// 如果提供了 config，则 data-dir 不是必需的（可以从配置文件读取）
	required := []string{targetHeightFlag}
	if p.configPath == "" {
		required = append(required, dataDirFlag)
	}
	return required
}

func (p *rollbackParams) validateFlags() error {
	// 如果提供了配置文件，尝试从配置文件读取 data-dir
	if p.configPath != "" && p.dataDir == "" {
		if err := p.loadDataDirFromConfig(); err != nil {
			return fmt.Errorf("failed to load data-dir from config: %w", err)
		}
	}

	if p.dataDir == "" {
		return errInvalidDataDir
	}

	if p.targetHeightRaw == "" {
		return errInvalidTargetHeight
	}

	var parseErr error
	if p.targetHeight, parseErr = common.ParseUint64orHex(&p.targetHeightRaw); parseErr != nil {
		return fmt.Errorf("invalid target height: %w", parseErr)
	}

	return nil
}

func (p *rollbackParams) executeRollback() error {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:  "rollback",
		Level: hclog.LevelFromString("INFO"),
	})

	// 1. 检查节点是否正在运行
	if err := p.checkNodeRunning(); err != nil {
		return err
	}

	// 2. 打开数据库连接
	blockchainPath := filepath.Join(p.dataDir, "blockchain")
	storageInstance, err := leveldb.NewLevelDBStorage(blockchainPath, logger)
	if err != nil {
		return fmt.Errorf("failed to open blockchain storage: %w", err)
	}
	defer storageInstance.Close()

	// 3. 验证目标高度
	currentHeight, ok := storageInstance.ReadHeadNumber()
	if !ok {
		return errors.New("failed to read current chain height")
	}
	p.currentHeight = currentHeight

	if p.targetHeight > currentHeight {
		return fmt.Errorf("%w: target height %d > current height %d", errTargetHeightInvalid, p.targetHeight, currentHeight)
	}

	// 4. 获取目标区块信息
	targetHash, ok := storageInstance.ReadCanonicalHash(p.targetHeight)
	if !ok {
		return fmt.Errorf("%w: block at height %d", errTargetBlockNotFound, p.targetHeight)
	}
	p.targetHash = targetHash

	// 5. 显示回滚信息并确认
	if !p.force {
		if err := p.confirmRollback(); err != nil {
			return err
		}
	}

	// 6. 加载配置参数（用于epoch计算）
	if err := p.loadEpochConfig(p.dataDir, logger); err != nil {
		logger.Warn("Failed to load epoch config, consensus state cleanup may be skipped",
			"error", err)
		// 不中断流程，继续执行
	}

	// 7. 执行回滚
	logger.Info("Starting rollback...",
		"currentHeight", currentHeight,
		"targetHeight", p.targetHeight,
		"targetHash", targetHash.String(),
		"blocksToDelete", currentHeight-p.targetHeight)

	if err := p.performRollback(storageInstance, logger); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	p.blocksDeleted = currentHeight - p.targetHeight

	logger.Info("Rollback completed successfully",
		"newHeadHeight", p.targetHeight,
		"newHeadHash", targetHash.String(),
		"blocksDeleted", p.blocksDeleted)

	return nil
}

func (p *rollbackParams) checkNodeRunning() error {
	lockFile := filepath.Join(p.dataDir, "blockchain", "LOCK")
	if _, err := os.Stat(lockFile); err == nil {
		return fmt.Errorf("%w: lock file exists at %s", errNodeRunning, lockFile)
	}
	return nil
}

func (p *rollbackParams) confirmRollback() error {
	fmt.Printf("\n⚠️  WARNING: This operation is IRREVERSIBLE!\n\n")
	fmt.Printf("Current chain height: %d\n", p.currentHeight)
	fmt.Printf("Target rollback height: %d\n", p.targetHeight)
	fmt.Printf("Blocks to be deleted: %d\n", p.currentHeight-p.targetHeight)
	fmt.Printf("New chain head will be: Block #%d (%s)\n\n", p.targetHeight, p.targetHash.String())
	fmt.Print("Are you sure you want to proceed? (yes/no): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read confirmation: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))
	if response != "yes" && response != "y" {
		return errors.New("rollback cancelled by user")
	}

	return nil
}

func (p *rollbackParams) performRollback(storageInstance storage.Storage, logger hclog.Logger) error {
	// 创建批量写入器
	batchWriter := storage.NewBatchWriter(storageInstance)

	// 1. 验证目标区块存在
	_, err := storageInstance.ReadHeader(p.targetHash)
	if err != nil {
		return fmt.Errorf("failed to read target header: %w", err)
	}

	// 2. 更新链头信息
	batchWriter.PutHeadHash(p.targetHash)
	batchWriter.PutHeadNumber(p.targetHeight)
	batchWriter.PutCanonicalHash(p.targetHeight, p.targetHash)

	logger.Info("Updated chain head",
		"height", p.targetHeight,
		"hash", p.targetHash.String())

	// 3. 删除目标高度之后的规范链映射
	for height := p.targetHeight + 1; height <= p.currentHeight; height++ {
		canonicalHash, ok := storageInstance.ReadCanonicalHash(height)
		if !ok {
			// 如果该高度的规范链映射不存在，跳过
			continue
		}

		// 删除规范链映射
		canonicalKey := append(storage.CANONICAL, common.EncodeUint64ToBytes(height)...)
		batchWriter.DeleteKey(canonicalKey)

		// 如果 keepBlocks 为 false，删除区块数据
		if !p.keepBlocks {
			if err := p.deleteBlockData(storageInstance, batchWriter, canonicalHash, logger); err != nil {
				logger.Warn("Failed to delete block data",
					"height", height,
					"hash", canonicalHash.String(),
					"error", err)
				// 继续删除其他区块，不中断流程
			}
		}
	}

	// 4. 清理状态快照（在删除区块数据之后）
	if !p.keepBlocks {
		if err := p.cleanupStateSnapshots(storageInstance, batchWriter, logger); err != nil {
			logger.Warn("Failed to cleanup state snapshots", "error", err)
			// 不中断流程，继续执行
		}
	}

	// 5. 提交所有更改
	if err := batchWriter.WriteBatch(); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}

	// 6. 清理DPoS共识状态（在提交区块链数据之后）
	if err := p.cleanupDPoSConsensusState(logger); err != nil {
		logger.Warn("Failed to cleanup DPoS consensus state", "error", err)
		// 不中断流程，记录警告
	}

	return nil
}

func (p *rollbackParams) deleteBlockData(
	storageInstance storage.Storage,
	batchWriter *storage.BatchWriter,
	blockHash types.Hash,
	logger hclog.Logger,
) error {
	// 读取区块体以获取交易列表
	body, err := storageInstance.ReadBody(blockHash)
	if err != nil {
		// 如果区块体不存在，跳过
		return nil
	}

	// 删除区块头
	headerKey := append(storage.HEADER, blockHash.Bytes()...)
	batchWriter.DeleteKey(headerKey)

	// 删除区块体
	bodyKey := append(storage.BODY, blockHash.Bytes()...)
	batchWriter.DeleteKey(bodyKey)

	// 删除收据
	receiptsKey := append(storage.RECEIPTS, blockHash.Bytes()...)
	batchWriter.DeleteKey(receiptsKey)

	// 删除总难度
	difficultyKey := append(storage.DIFFICULTY, blockHash.Bytes()...)
	batchWriter.DeleteKey(difficultyKey)

	// 删除交易查找索引
	for _, tx := range body.Transactions {
		txLookupKey := append(storage.TX_LOOKUP_PREFIX, tx.Hash.Bytes()...)
		batchWriter.DeleteKey(txLookupKey)
	}

	return nil
}

// loadDataDirFromConfig 从配置文件加载 data-dir
func (p *rollbackParams) loadDataDirFromConfig() error {
	cfg, err := config.ReadConfigFile(p.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if cfg.DataDir == "" {
		return errors.New("data_dir not found in config file")
	}

	p.dataDir = cfg.DataDir
	return nil
}

// loadEpochConfig 从配置文件加载epoch相关参数
func (p *rollbackParams) loadEpochConfig(dataDir string, logger hclog.Logger) error {
	// 方案1: 尝试从yaml配置文件读取
	configPath := filepath.Join(dataDir, "node-config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// 尝试其他可能的配置文件名称
		configPath = filepath.Join(filepath.Dir(dataDir), "node-config.yaml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = filepath.Join(dataDir, "config.yaml")
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				// 如果配置文件不存在，使用默认值
				logger.Warn("Config file not found, using default epoch values")
				p.consensusSwitchHeight = 0
				p.epochSize = 16 // 默认：48秒 / 3秒 = 16个区块
				p.blockTime = 3 * time.Second
				return nil
			}
		}
	}

	// 读取yaml配置
	configData, err := os.ReadFile(configPath)
	if err != nil {
		logger.Warn("Failed to read config file, using default values", "error", err)
		p.consensusSwitchHeight = 0
		p.epochSize = 16
		p.blockTime = 3 * time.Second
		return nil
	}

	// 简单解析yaml（查找关键字段）
	configStr := string(configData)

	// 解析 dpos_epoch_duration
	epochDurationStr := "48s" // 默认值
	if idx := strings.Index(configStr, "dpos_epoch_duration:"); idx != -1 {
		line := configStr[idx:]
		if endIdx := strings.Index(line, "\n"); endIdx != -1 {
			line = line[:endIdx]
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			epochDurationStr = strings.Trim(parts[1], `"`)
		}
	}

	// 解析 block_time_s
	blockTimeSeconds := uint64(3) // 默认值
	if idx := strings.Index(configStr, "block_time_s:"); idx != -1 {
		line := configStr[idx:]
		if endIdx := strings.Index(line, "\n"); endIdx != -1 {
			line = line[:endIdx]
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			if val, err := common.ParseUint64orHex(&parts[1]); err == nil {
				blockTimeSeconds = val
			}
		}
	}

	// 解析共识切换高度（如果存在）
	p.consensusSwitchHeight = 0
	if idx := strings.Index(configStr, "consensus_switch_height:"); idx != -1 {
		line := configStr[idx:]
		if endIdx := strings.Index(line, "\n"); endIdx != -1 {
			line = line[:endIdx]
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			if val, err := common.ParseUint64orHex(&parts[1]); err == nil {
				p.consensusSwitchHeight = val
			}
		}
	}

	// 解析epoch duration
	epochDuration, err := time.ParseDuration(epochDurationStr)
	if err != nil {
		logger.Warn("Failed to parse epoch duration, using default", "value", epochDurationStr, "error", err)
		epochDuration = 48 * time.Second
	}

	// 计算epoch大小
	blockTime := time.Duration(blockTimeSeconds) * time.Second
	if blockTime == 0 {
		blockTime = 3 * time.Second
	}
	p.epochSize = uint64(epochDuration / blockTime)
	if p.epochSize == 0 {
		p.epochSize = 1
	}
	p.blockTime = blockTime

	logger.Info("Loaded epoch config",
		"consensusSwitchHeight", p.consensusSwitchHeight,
		"epochSize", p.epochSize,
		"epochDuration", epochDuration.String(),
		"blockTime", blockTime.String())

	return nil
}

// cleanupStateSnapshots 清理目标高度之后的状态快照
func (p *rollbackParams) cleanupStateSnapshots(
	storageInstance storage.Storage,
	batchWriter *storage.BatchWriter,
	logger hclog.Logger,
) error {
	logger.Info("Cleaning up state snapshots...")

	// 获取目标区块的状态根（保留）
	targetHeader, err := storageInstance.ReadHeader(p.targetHash)
	if err != nil {
		return fmt.Errorf("failed to read target header: %w", err)
	}
	targetStateRoot := targetHeader.StateRoot

	// 遍历目标高度之后的区块，删除其状态快照
	deletedCount := 0
	for height := p.targetHeight + 1; height <= p.currentHeight; height++ {
		canonicalHash, ok := storageInstance.ReadCanonicalHash(height)
		if !ok {
			continue
		}

		header, err := storageInstance.ReadHeader(canonicalHash)
		if err != nil {
			continue
		}

		// 跳过目标区块的状态根（保留）
		if header.StateRoot == targetStateRoot {
			continue
		}

		// 删除该区块的状态快照
		snapshotKey := append(storage.SNAPSHOTS, header.StateRoot.Bytes()...)
		batchWriter.DeleteKey(snapshotKey)
		deletedCount++
	}

	logger.Info("State snapshots cleanup completed",
		"deletedCount", deletedCount)

	return nil
}

// cleanupDPoSConsensusState 清理DPoS共识状态
func (p *rollbackParams) cleanupDPoSConsensusState(logger hclog.Logger) error {
	// 如果epoch参数未加载，跳过清理
	if p.epochSize == 0 {
		logger.Warn("Epoch config not loaded, skipping DPoS consensus state cleanup")
		return nil
	}

	// 计算目标epoch
	targetEpoch := p.calculateTargetEpoch(p.targetHeight)

	logger.Info("Cleaning up DPoS consensus state...",
		"targetEpoch", targetEpoch,
		"targetHeight", p.targetHeight)

	// 打开DPoS数据库
	dposDBPath := filepath.Join(p.dataDir, "dpos.db")
	if _, err := os.Stat(dposDBPath); os.IsNotExist(err) {
		logger.Debug("DPoS database not found, skipping cleanup", "path", dposDBPath)
		return nil
	}

	db, err := bolt.Open(dposDBPath, 0666, nil)
	if err != nil {
		return fmt.Errorf("failed to open DPoS database: %w", err)
	}
	defer db.Close()

	// 清理各个bucket的数据
	err = db.Update(func(tx *bolt.Tx) error {
		// 清理 epochs bucket
		if err := p.cleanupEpochsBucket(tx, targetEpoch, logger); err != nil {
			return err
		}

		// 清理 validatorSnapshots bucket
		if err := p.cleanupValidatorSnapshotsBucket(tx, targetEpoch, logger); err != nil {
			return err
		}

		// 清理 EpochBlocks bucket
		if err := p.cleanupEpochBlocksBucket(tx, targetEpoch, logger); err != nil {
			return err
		}

		// 清理 VotingPowerAtBlock bucket
		if err := p.cleanupVotingPowerBucket(tx, p.targetHeight, logger); err != nil {
			return err
		}

		// 回滚参数值（必须在清理提案之前，需要用提案的OldValue恢复）
		if err := p.rollbackParameterValues(tx, targetEpoch, logger); err != nil {
			return err
		}

		// 清理提案数据
		if err := p.cleanupProposalsBucket(tx, p.targetHeight, logger); err != nil {
			return err
		}

		// 清理验证者故障状态（根据 epoch 清理）
		if err := p.cleanupValidatorFaultStatusBucket(tx, targetEpoch, logger); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to cleanup DPoS state: %w", err)
	}

	// 清理奖励数据库（独立数据库）
	if err := p.cleanupEpochRewardsBucket(targetEpoch, logger); err != nil {
		return fmt.Errorf("failed to cleanup epoch rewards: %w", err)
	}

	return nil
}

// calculateTargetEpoch 计算目标高度对应的epoch
func (p *rollbackParams) calculateTargetEpoch(targetHeight uint64) uint64 {
	if targetHeight < p.consensusSwitchHeight {
		return 0
	}

	dposBlockNumber := targetHeight - p.consensusSwitchHeight
	epochNumber := (dposBlockNumber / p.epochSize) + 1

	return epochNumber
}

// cleanupEpochsBucket 清理epochs bucket
func (p *rollbackParams) cleanupEpochsBucket(
	tx *bolt.Tx,
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("epochs"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	deletedCount := 0
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		epochNumber := binary.BigEndian.Uint64(k)
		if epochNumber > targetEpoch {
			if err := cursor.Delete(); err != nil {
				return fmt.Errorf("failed to delete epoch %d: %w", epochNumber, err)
			}
			deletedCount++
		}
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up epochs bucket", "deletedCount", deletedCount)
	}

	return nil
}

// cleanupValidatorSnapshotsBucket 清理验证者快照
func (p *rollbackParams) cleanupValidatorSnapshotsBucket(
	tx *bolt.Tx,
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("validatorSnapshots"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	deletedCount := 0
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		epochNumber := binary.BigEndian.Uint64(k)
		if epochNumber > targetEpoch {
			if err := cursor.Delete(); err != nil {
				return fmt.Errorf("failed to delete validator snapshot for epoch %d: %w", epochNumber, err)
			}
			deletedCount++
		}
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up validator snapshots bucket", "deletedCount", deletedCount)
	}

	return nil
}

// cleanupEpochBlocksBucket 清理出块统计
func (p *rollbackParams) cleanupEpochBlocksBucket(
	tx *bolt.Tx,
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("EpochBlocks"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	deletedCount := 0
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		epochNumber := binary.BigEndian.Uint64(k)
		if epochNumber > targetEpoch {
			if err := cursor.Delete(); err != nil {
				return fmt.Errorf("failed to delete epoch blocks for epoch %d: %w", epochNumber, err)
			}
			deletedCount++
		}
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up epoch blocks bucket", "deletedCount", deletedCount)
	}

	return nil
}

// cleanupEpochRewardsBucket 清理奖励记录
func (p *rollbackParams) cleanupEpochRewardsBucket(
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	// 奖励数据在独立的数据库中
	rewardDBPath := filepath.Join(p.dataDir, "dpos.db.rewards")
	if _, err := os.Stat(rewardDBPath); os.IsNotExist(err) {
		return nil
	}

	rewardDB, err := bolt.Open(rewardDBPath, 0666, nil)
	if err != nil {
		return fmt.Errorf("failed to open reward database: %w", err)
	}
	defer rewardDB.Close()

	return rewardDB.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("EpochRewards"))
		if bucket == nil {
			return nil
		}

		cursor := bucket.Cursor()
		deletedCount := 0
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			epochNumber := binary.BigEndian.Uint64(k)
			if epochNumber > targetEpoch {
				if err := cursor.Delete(); err != nil {
					return fmt.Errorf("failed to delete epoch reward for epoch %d: %w", epochNumber, err)
				}
				deletedCount++
			}
		}

		if deletedCount > 0 {
			logger.Info("Cleaned up epoch rewards bucket", "deletedCount", deletedCount)
		}

		return nil
	})
}

// cleanupVotingPowerBucket 清理投票权重
func (p *rollbackParams) cleanupVotingPowerBucket(
	tx *bolt.Tx,
	targetHeight uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("VotingPowerAtBlock"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	deletedCount := 0
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		if len(k) >= 8 {
			blockNumber := binary.BigEndian.Uint64(k[:8])
			if blockNumber > targetHeight {
				if err := cursor.Delete(); err != nil {
					return fmt.Errorf("failed to delete voting power for block %d: %w", blockNumber, err)
				}
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up voting power bucket", "deletedCount", deletedCount)
	}

	return nil
}

// cleanupProposalsBucket 清理提案数据
func (p *rollbackParams) cleanupProposalsBucket(
	tx *bolt.Tx,
	targetHeight uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("proposals"))
	if bucket == nil {
		return nil
	}

	// 提案数据结构需要根据实际情况调整
	// 这里假设提案包含区块高度信息
	cursor := bucket.Cursor()
	keysToDelete := [][]byte{}

	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		// 尝试解析提案数据
		var proposalData map[string]interface{}
		if err := json.Unmarshal(v, &proposalData); err != nil {
			// 如果解析失败，跳过
			continue
		}

		// 检查提案的区块高度字段
		// 根据实际提案结构调整字段名
		if endBlock, ok := proposalData["endBlock"].(float64); ok {
			if uint64(endBlock) > targetHeight {
				keysToDelete = append(keysToDelete, k)
			}
		} else if startBlock, ok := proposalData["startBlock"].(float64); ok {
			// 如果只有startBlock，检查是否在目标高度之后
			if uint64(startBlock) > targetHeight {
				keysToDelete = append(keysToDelete, k)
			}
		}
	}

	// 删除标记的提案
	deletedCount := 0
	for _, key := range keysToDelete {
		if err := bucket.Delete(key); err != nil {
			return fmt.Errorf("failed to delete proposal: %w", err)
		}
		deletedCount++
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up proposals bucket", "deletedCount", deletedCount)
	}

	return nil
}

// cleanupValidatorFaultStatusBucket 清理验证者故障状态
// 删除 lastFaultyEpoch > targetEpoch 的故障记录
func (p *rollbackParams) cleanupValidatorFaultStatusBucket(
	tx *bolt.Tx,
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	bucket := tx.Bucket([]byte("validatorFaultStatus"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	deletedCount := 0
	var keysToDelete [][]byte

	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		// 解析故障状态信息
		var faultInfo map[string]interface{}
		if err := json.Unmarshal(v, &faultInfo); err != nil {
			logger.Warn("Failed to parse fault status, skipping",
				"key", fmt.Sprintf("%x", k),
				"error", err)
			continue
		}

		// 获取 lastFaultyEpoch
		lastFaultyEpoch := uint64(0)
		if lfe, ok := faultInfo["lastFaultyEpoch"].(float64); ok {
			lastFaultyEpoch = uint64(lfe)
		}

		// 如果故障记录的 epoch 大于目标 epoch，删除该记录
		if lastFaultyEpoch > targetEpoch {
			keysToDelete = append(keysToDelete, append([]byte{}, k...))
			deletedCount++
		}
	}

	// 删除标记的记录
	for _, key := range keysToDelete {
		if err := bucket.Delete(key); err != nil {
			return fmt.Errorf("failed to delete validator fault status: %w", err)
		}
	}

	if deletedCount > 0 {
		logger.Info("Cleaned up validator fault status bucket",
			"deletedCount", deletedCount,
			"targetEpoch", targetEpoch)
	}

	return nil
}

// rollbackParameterValues 回滚参数值
// 找到 effectiveEpoch > targetEpoch 且已执行的参数提案，用 OldValue 恢复参数值
func (p *rollbackParams) rollbackParameterValues(
	tx *bolt.Tx,
	targetEpoch uint64,
	logger hclog.Logger,
) error {
	proposalsBucket := tx.Bucket([]byte("proposals"))
	if proposalsBucket == nil {
		return nil
	}

	parametersBucket := tx.Bucket([]byte("parameters"))
	if parametersBucket == nil {
		// 参数bucket不存在，无需回滚
		return nil
	}

	cursor := proposalsBucket.Cursor()
	restoredCount := 0

	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		var proposalData map[string]interface{}
		if err := json.Unmarshal(v, &proposalData); err != nil {
			continue
		}

		// 只处理参数类型的提案
		proposalType, _ := proposalData["proposalType"].(string)
		if proposalType != "parameter" {
			continue
		}

		// 检查是否已执行
		schedule, hasSchedule := proposalData["schedule"].(map[string]interface{})
		if !hasSchedule {
			continue
		}

		applied, _ := schedule["applied"].(bool)
		if !applied {
			// 未执行的提案不需要回滚
			continue
		}

		// 获取 effectiveEpoch
		effectiveEpoch := uint64(0)
		if eff, ok := schedule["effectiveEpoch"].(float64); ok {
			effectiveEpoch = uint64(eff)
		}

		// 如果提案在目标epoch之后生效，需要回滚
		if effectiveEpoch > targetEpoch {
			paramName, _ := proposalData["parameter"].(string)
			oldValue := proposalData["oldValue"]

			if paramName == "" || oldValue == nil {
				logger.Warn("Invalid proposal data for rollback",
					"proposalID", string(k),
					"parameter", paramName)
				continue
			}

			// 构造参数值结构
			paramValue := map[string]interface{}{
				"current_value": oldValue,
				"updated_at":    time.Now().Format(time.RFC3339),
				"source":        fmt.Sprintf("rollback_to_epoch_%d", targetEpoch),
			}

			paramData, err := json.Marshal(paramValue)
			if err != nil {
				logger.Warn("Failed to marshal parameter value",
					"parameter", paramName,
					"error", err)
				continue
			}

			// 恢复参数值
			if err := parametersBucket.Put([]byte(paramName), paramData); err != nil {
				logger.Warn("Failed to restore parameter value",
					"parameter", paramName,
					"error", err)
				continue
			}

			// 重置提案的执行状态
			schedule["applied"] = false
			schedule["appliedAtBlock"] = nil
			proposalData["schedule"] = schedule
			proposalData["status"] = "scheduled" // 重置为已调度状态

			updatedProposal, err := json.Marshal(proposalData)
			if err != nil {
				logger.Warn("Failed to marshal updated proposal",
					"proposalID", string(k),
					"error", err)
				continue
			}

			if err := proposalsBucket.Put(k, updatedProposal); err != nil {
				logger.Warn("Failed to update proposal status",
					"proposalID", string(k),
					"error", err)
				continue
			}

			logger.Info("Restored parameter value from proposal",
				"parameter", paramName,
				"oldValue", oldValue,
				"effectiveEpoch", effectiveEpoch,
				"proposalID", string(k))

			restoredCount++
		}
	}

	if restoredCount > 0 {
		logger.Info("Rolled back parameter values",
			"restoredCount", restoredCount,
			"targetEpoch", targetEpoch)
	}

	return nil
}

func (p *rollbackParams) getResult() command.CommandResult {
	return &RollbackResult{
		CurrentHeight: p.currentHeight,
		TargetHeight:  p.targetHeight,
		TargetHash:    p.targetHash.String(),
		BlocksDeleted: p.blocksDeleted,
		KeepBlocks:    p.keepBlocks,
	}
}
