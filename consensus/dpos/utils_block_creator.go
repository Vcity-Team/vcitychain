package dpos

import (
	"github.com/Vcity-Team/vcitychain/types"
)

// GetBlockCreator 获取区块创建者
func (d *DPoS) GetBlockCreator(header *types.Header) (types.Address, error) {
	return types.BytesToAddress(header.Miner), nil
}




