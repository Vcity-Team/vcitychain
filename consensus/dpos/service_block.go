package dpos

import (
	"fmt"
	"sync"

	"github.com/Vcity-Team/vcitychain/types"
)

// blockService 区块服务实现
type blockService struct {
	dpos  *DPoS
	mutex sync.RWMutex
}

// NewBlockService 创建新的区块服务
// 注意：runtime类型需要从dpos包导出或使用接口
func NewBlockService(dpos *DPoS) BlockService {
	return &blockService{
		dpos: dpos,
		// runtime通过dpos.runtime访问
	}
}

// BuildBlock 构建新区块
func (bs *blockService) BuildBlock(parent *types.Header, coinbase types.Address) (*types.FullBlock, error) {
	bs.mutex.Lock()
	defer bs.mutex.Unlock()
	
	// 使用现有的buildBlock逻辑
	// 这里需要调用block_builder.go中的buildBlock方法
	// 暂时返回nil，后续迁移
	return nil, fmt.Errorf("not implemented")
}

// ValidateBlock 验证区块
func (bs *blockService) ValidateBlock(block *types.Block) error {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()
	
	// 使用现有的VerifyHeader方法
	return bs.dpos.VerifyHeader(block.Header)
}

// ShouldProduceBlock 判断是否应该出块
func (bs *blockService) ShouldProduceBlock() bool {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()
	
	// 使用runtime的shouldProduceBlockNow方法
	// 需要通过dpos访问runtime
	if bs.dpos != nil && bs.dpos.runtime != nil {
		// 需要导出shouldProduceBlockNow方法或通过接口访问
		return false // 暂时返回false，后续迁移
	}
	
	return false
}

// GetBlockCreator 获取区块创建者
func (bs *blockService) GetBlockCreator(blockNumber uint64) (types.Address, error) {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()
	
	// 使用现有的GetBlockCreator方法
	// 这里需要调用utils_block_creator.go中的GetBlockCreator
	return types.ZeroAddress, fmt.Errorf("not implemented")
}


