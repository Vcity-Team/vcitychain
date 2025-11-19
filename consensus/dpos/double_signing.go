package dpos

import (
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

const (
	defaultDoubleSigningWindow           = 2   // 检查同一高度或相邻高度
	defaultDoubleSigningRecordRetention  = 512 // 只保留最近N个高度的记录
	defaultDoubleSigningRecordsPerSigner = 128 // 每个验证者最多保留的记录数
)

// BlockSignature 记录验证者的单个区块签名
type BlockSignature struct {
	ValidatorAddr types.Address
	BlockHeight   uint64
	BlockHash     types.Hash
	Timestamp     uint64
}

// DoubleSigningDetector 负责检测双签
type DoubleSigningDetector struct {
	signatures map[string][]*BlockSignature
	mutex      sync.Mutex
	logger     hclog.Logger

	heightWindow           uint64
	retentionWindow        uint64
	maxRecordsPerValidator int
}

// NewDoubleSigningDetector 创建检测器
func NewDoubleSigningDetector(logger hclog.Logger) *DoubleSigningDetector {
	return &DoubleSigningDetector{
		signatures:             make(map[string][]*BlockSignature),
		logger:                 logger,
		heightWindow:           defaultDoubleSigningWindow,
		retentionWindow:        defaultDoubleSigningRecordRetention,
		maxRecordsPerValidator: defaultDoubleSigningRecordsPerSigner,
	}
}

// DetectDoubleSigning 检测是否出现双签（检测同一高度或相邻高度）
func (d *DoubleSigningDetector) DetectDoubleSigning(validatorAddr types.Address, blockHeight uint64, blockHash types.Hash) (bool, *BlockSignature) {
	if validatorAddr == (types.Address{}) {
		return false, nil
	}

	signature := &BlockSignature{
		ValidatorAddr: validatorAddr,
		BlockHeight:   blockHeight,
		BlockHash:     blockHash,
		Timestamp:     uint64(time.Now().Unix()),
	}

	d.mutex.Lock()
	defer d.mutex.Unlock()

	addressKey := validatorAddr.String()
	existing := d.signatures[addressKey]

	for _, prev := range existing {
		if prev == nil {
			continue
		}

		if blockHeight == prev.BlockHeight {
			if prev.BlockHash != blockHash {
				d.logger.Warn("🚨 检测到双重签名",
					"validator", validatorAddr.String(),
					"height", blockHeight,
					"currentHash", blockHash.String(),
					"conflictHash", prev.BlockHash.String())

				existing = append(existing, signature)
				d.signatures[addressKey] = d.pruneRecords(existing, blockHeight)
				return true, prev
			}
			continue
		}

		heightDistance := distance(blockHeight, prev.BlockHeight)
		if heightDistance <= d.heightWindow &&
			prev.BlockHash != blockHash {
			d.logger.Debug("ℹ️ 忽略相邻高度的不同区块（非双签）",
				"validator", validatorAddr.String(),
				"currentHeight", blockHeight,
				"previousHeight", prev.BlockHeight,
				"currentHash", blockHash.String(),
				"previousHash", prev.BlockHash.String())
		}
	}

	existing = append(existing, signature)
	d.signatures[addressKey] = d.pruneRecords(existing, blockHeight)
	return false, nil
}

// ClearOldSignatures 手动清理过旧签名
func (d *DoubleSigningDetector) ClearOldSignatures(currentHeight uint64) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	for key, records := range d.signatures {
		d.signatures[key] = d.pruneRecords(records, currentHeight)
		if len(d.signatures[key]) == 0 {
			delete(d.signatures, key)
		}
	}
}

func (d *DoubleSigningDetector) pruneRecords(records []*BlockSignature, currentHeight uint64) []*BlockSignature {
	if len(records) == 0 {
		return records
	}

	filtered := records[:0]
	for _, record := range records {
		if record == nil {
			continue
		}

		if currentHeight >= record.BlockHeight && currentHeight-record.BlockHeight > d.retentionWindow {
			continue
		}
		filtered = append(filtered, record)
	}

	if len(filtered) > d.maxRecordsPerValidator {
		filtered = filtered[len(filtered)-d.maxRecordsPerValidator:]
	}

	return filtered
}

func distance(a, b uint64) uint64 {
	if a >= b {
		return a - b
	}
	return b - a
}
