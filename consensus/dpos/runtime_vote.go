package dpos

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// startVoteCollection 启动投票收集
func (r *dposRuntime) startVoteCollection() error {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		return fmt.Errorf("key not available, cannot start vote collection")
	}

	voteTime := 5 * time.Second // 默认5秒
	if r.config.PolyBFTConfig != nil {
		voteTime = r.config.PolyBFTConfig.BlockTime.Duration * 4 // 投票时间设为区块时间的4倍
	}

	r.voteTimer = time.NewTicker(voteTime)
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("vote-collection", func() {
			defer func() {
				// 确保定时器被停止
				if r.voteTimer != nil {
					r.voteTimer.Stop()
					r.voteTimer = nil
				}
				r.logger.Debug("投票收集goroutine已退出")
			}()

			for {
				select {
				case <-r.voteTimer.C:
					if err := r.collectVotes(); err != nil {
						r.logger.Error("failed to collect votes", "error", err)
					}
				case <-r.closeCh:
					r.logger.Debug("投票收集goroutine收到关闭信号")
					return
				}
			}
		})
	}

	return nil
}

// collectVotes 收集投票
func (r *dposRuntime) collectVotes() error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 处理待处理的投票
	for _, vote := range r.pendingVotes {
		if err := r.processVote(vote); err != nil {
			r.logger.Error("failed to process vote", "error", err, "voter", vote.Voter)
		}
	}

	// 清空待处理投票
	r.pendingVotes = nil

	return nil
}

// processVote 处理投票
func (r *dposRuntime) processVote(vote *VoteMessage) error {
	// 验证投票签名
	if err := r.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("invalid vote signature: %w", err)
	}

	// 更新投票者信息
	voter, exists := r.voters[vote.Voter]
	if !exists {
		voter = &VoterInfo{
			Address:        vote.Voter,
			VotingPower:    big.NewInt(0),
			VotedDelegates: make([]types.Address, 0),
			LastVoteTime:   uint64(time.Now().Unix()),
			LockedUntil:    uint64(time.Now().Unix()) + 86400, // 锁定24小时
		}
		r.voters[vote.Voter] = voter
	}

	// 更新投票权重
	voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
	voter.LastVoteTime = uint64(time.Now().Unix())

	// 添加受托人到投票列表
	voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)

	return nil
}

// verifyVoteSignature 验证投票签名
func (r *dposRuntime) verifyVoteSignature(vote *VoteMessage) error {
	// 1. 检查签名字段
	if len(vote.Signature) == 0 {
		return fmt.Errorf("vote signature is empty")
	}

	// 2. 检查签名长度（ECDSA签名应该是65字节）
	if len(vote.Signature) != 65 {
		return fmt.Errorf("invalid signature length: expected 65 bytes, got %d", len(vote.Signature))
	}

	// 3. 构建待签名消息
	message := r.buildVoteMessage(vote)

	// 4. 计算消息哈希
	hash := crypto.Keccak256(message)

	// 5. 恢复公钥
	publicKey, err := crypto.RecoverPubkey(vote.Signature, hash)
	if err != nil {
		return fmt.Errorf("failed to recover public key: %w", err)
	}

	// 6. 验证公钥与投票者地址匹配
	if !verifyAddressMatchesPublicKey(vote.Voter, publicKey) {
		return fmt.Errorf("public key does not match voter address")
	}

	// 7. 验证签名
	if !verifyECDSASignature(publicKey, hash, vote.Signature) {
		return fmt.Errorf("signature verification failed")
	}

	// 8. 防重放验证
	if err := r.verifyVoteNonce(vote); err != nil {
		return fmt.Errorf("vote nonce verification failed: %w", err)
	}

	r.logger.Debug("投票签名验证成功",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"round", vote.Round)

	return nil
}

// buildVoteMessage 构建标准化的投票消息
func (r *dposRuntime) buildVoteMessage(vote *VoteMessage) []byte {
	// 使用标准化的消息格式，确保签名验证的一致性
	// 格式：voter:delegate:amount:round:timestamp
	message := fmt.Sprintf("%s:%s:%s:%d:%d",
		vote.Voter.String(),
		vote.Delegate.String(),
		vote.Amount.String(),
		vote.Round,
		vote.Timestamp)

	return []byte(message)
}

// verifyVoteNonce 验证投票nonce，防止重放攻击
func (r *dposRuntime) verifyVoteNonce(vote *VoteMessage) error {
	// 构建nonce key
	nonceKey := fmt.Sprintf("%s:%d:%d",
		vote.Voter.String(),
		vote.Round,
		vote.Timestamp)

	r.voteMutex.Lock()
	defer r.voteMutex.Unlock()

	// 检查是否已经处理过这个nonce
	if r.processedVotes[nonceKey] {
		return fmt.Errorf("vote nonce already processed: %s", nonceKey)
	}

	// 标记为已处理
	r.processedVotes[nonceKey] = true

	// 清理过期的nonce（超过1小时的）
	r.cleanupExpiredVotes()

	return nil
}

// cleanupExpiredVotes 清理过期的投票nonce
func (r *dposRuntime) cleanupExpiredVotes() {
	// 这里可以实现更复杂的清理逻辑
	// 目前使用简单的策略：当map大小超过1000时清理一半
	if len(r.processedVotes) > 1000 {
		// 简单清理：保留一半
		count := 0
		for key := range r.processedVotes {
			if count >= 500 {
				delete(r.processedVotes, key)
			}
			count++
		}
	}
}

// updateDelegateVotingPower 更新受托人投票权重
func (r *dposRuntime) updateDelegateVotingPower(delegate types.Address, amount *big.Int) {
	// 使用静态变量跟踪调用次数
	static := struct {
		count int
		mu    sync.Mutex
	}{}

	static.mu.Lock()
	static.count++
	currentCount := static.count
	static.mu.Unlock()

	fmt.Printf("🔍 updateDelegateVotingPower: 开始更新受托人投票权重 (第%d次调用)\n", currentCount)
	fmt.Printf("  - 调用时间: %s\n", time.Now().Format("15:04:05.000"))
	fmt.Printf("  - 受托人地址: %s\n", delegate.String())
	fmt.Printf("  - 新增投票权重: %s (0x%x)\n", amount.String(), amount.Bytes())

	// 🆕 详细打印内存中的 r.delegates 数组状态
	fmt.Printf("🔍 内存中 r.delegates 数组状态 (共%d个):\n", len(r.delegates))
	for i, d := range r.delegates {
		fmt.Printf("  - delegates[%d]: 地址=%s, VotingPower=%s (0x%x), IsActive=%v\n",
			i, d.Address.String(), d.VotingPower.String(), d.VotingPower.Bytes(), d.IsActive)
	}

	// 🆕 检查是否为创世验证者
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			if dposInstance.isGenesisValidator(delegate) {
				fmt.Printf("  - 🔒 创世验证者权重保持不变，跳过更新\n")
				fmt.Printf("  - 地址: %s\n", delegate.String())
				fmt.Printf("  - 权重: %s\n", amount.String())
				fmt.Printf("  - 说明: 创世验证者权重永远不变\n")
				return
			}
		}
	}

	// 🆕 查找目标受托人并更新
	found := false
	for i, d := range r.delegates {
		if d.Address == delegate {
			oldPower := new(big.Int).Set(d.VotingPower)
			d.VotingPower = new(big.Int).Add(d.VotingPower, amount)

			fmt.Printf("  - ✅ 找到目标受托人: %s (索引%d)\n", d.Address.String(), i)
			fmt.Printf("  - 旧投票权重: %s (0x%x)\n", oldPower.String(), oldPower.Bytes())
			fmt.Printf("  - 新投票权重: %s (0x%x)\n", d.VotingPower.String(), d.VotingPower.Bytes())
			fmt.Printf("  - 计算过程: %s + %s = %s\n", oldPower.String(), amount.String(), d.VotingPower.String())
			fmt.Printf("  - 更新后IsActive: %v\n", d.IsActive)
			found = true
			break
		}
	}

	if !found {
		fmt.Printf("  - ❌ 未找到目标受托人: %s\n", delegate.String())
		fmt.Printf("  - 当前内存中的delegates数组可能不完整或为空\n")
	}

	// 🆕 更新后再次打印内存状态
	fmt.Printf("🔍 更新后内存中 r.delegates 数组状态:\n")
	for i, d := range r.delegates {
		fmt.Printf("  - delegates[%d]: 地址=%s, VotingPower=%s (0x%x), IsActive=%v\n",
			i, d.Address.String(), d.VotingPower.String(), d.VotingPower.Bytes(), d.IsActive)
	}
}

