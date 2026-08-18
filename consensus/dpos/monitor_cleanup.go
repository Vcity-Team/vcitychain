package dpos

import (
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// cleanupExpiredCaches 清理过期的运行时缓存
func (r *dposRuntime) cleanupExpiredCaches() {
	now := time.Now()

	// 清理过期的签名生成记录（超过10分钟）
	r.signatureGenerationDedupMutex.Lock()
	for key, timestamp := range r.processedSignatureGenerations {
		if now.Sub(timestamp) > 10*time.Minute {
			delete(r.processedSignatureGenerations, key)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()

	// 清理过期的日志时间记录（超过1小时）
	r.logMutex.Lock()
	for key, timestamp := range r.lastLogTime {
		if now.Sub(timestamp) > time.Hour {
			delete(r.lastLogTime, key)
		}
	}
	r.logMutex.Unlock()

	// 清理过期的BLS私钥缓存
	r.blsPrivateKeyCacheMutex.Lock()
	if !r.blsPrivateKeyCacheTime.IsZero() && now.Sub(r.blsPrivateKeyCacheTime) > blsKeyCacheTTL {
		r.blsPrivateKeyCache = nil
		r.blsPrivateKeyCacheTime = time.Time{}
	}
	r.blsPrivateKeyCacheMutex.Unlock()
}

// cleanupSignatureCollectionResources 清理签名收集相关的资源
func (r *dposRuntime) cleanupSignatureCollectionResources(checkpointHash types.Hash) {
	// 清理已处理的签名请求记录
	r.signatureRequestDedupMutex.Lock()
	for key := range r.processedSignatureRequests {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureRequests, key)
		}
	}
	r.signatureRequestDedupMutex.Unlock()

	// 清理已处理的签名响应记录
	r.signatureResponseDedupMutex.Lock()
	for key := range r.processedSignatureResponses {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureResponses, key)
		}
	}
	r.signatureResponseDedupMutex.Unlock()

	// 清理已处理的签名生成记录
	r.signatureGenerationDedupMutex.Lock()
	for key := range r.processedSignatureGenerations {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureGenerations, key)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()

	// 清理待处理的签名请求
	r.signatureRequestMutex.Lock()
	delete(r.pendingSignatureRequests, checkpointHash)
	r.signatureRequestMutex.Unlock()

	// 签名收集资源已清理
}
