package dpos

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// BLSRequestBuilder 构建BLS请求
type BLSRequestBuilder struct {
	dposInstance *DPoS
}

// NewBLSRequestBuilder 创建请求构建器
func NewBLSRequestBuilder(dposInstance *DPoS) *BLSRequestBuilder {
	return &BLSRequestBuilder{
		dposInstance: dposInstance,
	}
}

// BuildRequest 构建请求消息（RequestID 使用 UnixNano，避免同秒冲突）
func (brb *BLSRequestBuilder) BuildRequest(targetAddress types.Address) (*BLSPublicKeyRequest, string) {
	now := time.Now()
	requestID := fmt.Sprintf("bls_request_%s_%d", targetAddress.String(), now.UnixNano())
	request := &BLSPublicKeyRequest{
		RequestID:        requestID,
		RequesterAddress: types.Address(brb.dposInstance.key.Address()),
		TargetAddress:    targetAddress,
		Timestamp:        uint64(now.Unix()),
	}
	return request, requestID
}

// MarshalRequest 序列化请求
func (brb *BLSRequestBuilder) MarshalRequest(request *BLSPublicKeyRequest) ([]byte, error) {
	return json.Marshal(request)
}
