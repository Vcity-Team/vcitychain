package dpos

import (
	"github.com/Vcity-Team/vcitychain/types"
)

// BLSPublicKeyRequest BLS公钥请求消息
type BLSPublicKeyRequest struct {
	RequesterAddress types.Address `json:"requester_address"`
	TargetAddress    types.Address `json:"target_address"`
	Timestamp        uint64        `json:"timestamp"`
}

// BLSPublicKeyResponse BLS公钥响应消息
type BLSPublicKeyResponse struct {
	RequestID        string        `json:"request_id"`
	TargetAddress    types.Address `json:"target_address"`
	RequesterAddress types.Address `json:"requester_address"`
	BlsPublicKey     []byte        `json:"bls_public_key"`
	Timestamp        uint64        `json:"timestamp"`
}




