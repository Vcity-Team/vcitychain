package dpos

import (
	"context"
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// initializeRuntime 初始化运行时状态
func (r *dposRuntime) initializeRuntime() error {
	// 检查配置是否可用
	if r.config == nil {
		return fmt.Errorf("runtime config is nil")
	}

	// 🆕 修复：根据当前区块号计算初始轮次
	r.currentRound = r.calculateInitialRound()

	// 初始化受托人集合
	if err := r.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	// 初始化投票者映射
	r.voters = make(map[types.Address]*VoterInfo)

	// 初始化签名请求存储
	r.pendingSignatureRequests = make(map[types.Hash]*SignatureRequest)

	// 初始化签名请求去重机制
	r.processedSignatureRequests = make(map[string]time.Time)

	// 初始化签名响应广播去重机制
	r.processedSignatureResponses = make(map[string]time.Time)

	// 初始化签名响应生成去重机制
	r.processedSignatureGenerations = make(map[string]time.Time)

	// 初始化并发控制
	r.maxConcurrentSignatures = 10 // 最多同时处理10个签名请求
	r.signatureRequestSemaphore = make(chan struct{}, r.maxConcurrentSignatures)

	// 🆕 初始化防重复日志机制
	r.lastLogTime = make(map[string]time.Time)

	// 🆕 初始化投票签名验证相关
	r.processedVotes = make(map[string]bool)

	// 检查网络服务状态
	if r.network == nil {
		r.logger.Warn("网络服务不可用，DPoS共识将无法进行网络通信")
	} else {
		r.setupNetworkEventListeners()

		// 暂时禁用网络集成管理器，使用原有的签名收集机制
	}

	// 初始化资源监控器
	r.resourceMonitor = NewResourceMonitor(r.logger, r)
	r.resourceMonitor.Start(context.Background())

	return nil
}

// setupNetworkIntegration 设置网络集成
func (r *dposRuntime) setupNetworkIntegration() error {
	if r.network == nil {
		return fmt.Errorf("网络服务不可用，无法设置网络集成")
	}

	// 创建网络集成管理器
	r.networkIntegration = NewNetworkIntegration(r.network, r.logger)

	// 设置DPoS运行时回调
	r.networkIntegration.SetDPoSRuntime(r)

	// 🆕 设置DPoS实例引用（如果backend是DPoS实例）
	if dpos, ok := r.backend.(*DPoS); ok {
		r.networkIntegration.SetDPoSInstance(dpos)
	}

	// 🆕 设置BLS公钥持久化回调函数
	r.networkIntegration.SetBLSKeyPersistCallback(func(address types.Address, blsKeyBytes []byte) error {
		// 通过backend获取DPoS实例
		if dpos, ok := r.backend.(*DPoS); ok {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				r.logger.Warn("BLS公钥持久化失败",
					"address", address.String(),
					"blsKeyLength", len(blsKeyBytes),
					"error", err)
				return err
			}
			r.logger.Debug("BLS公钥持久化成功",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}

		// 如果backend不是DPoS实例，尝试通过全局注册表查找
		if dpos, exists := GetDPoSInstance(address.String()); exists && dpos != nil {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				r.logger.Warn("通过全局注册表BLS公钥持久化失败",
					"address", address.String(),
					"blsKeyLength", len(blsKeyBytes),
					"error", err)
				return err
			}
			r.logger.Debug("通过全局注册表BLS公钥持久化成功",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}

		r.logger.Warn("无法找到DPoS实例进行BLS公钥持久化",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
		return fmt.Errorf("DPoS instance not available for BLS key persistence")
	})

	// 🆕 设置BLS公钥查找回调函数
	r.networkIntegration.SetBLSKeyLookupCallback(func(address types.Address) ([]byte, error) {
		// 通过全局注册表获取DPoS实例
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			return dposInstance.GetBLSKeyBytesFromGenesis(address)
		}
		return nil, fmt.Errorf("DPoS instance not available for BLS key lookup")
	})

	// 尝试获取现有主题，避免重复创建
	r.topicMutex.RLock()
	if r.signatureRequestTopic != nil {
		r.networkIntegration.SetExistingTopics(r.signatureRequestTopic, r.signatureResponseTopic)
		r.logger.Info("使用现有主题设置网络集成")
	}
	r.topicMutex.RUnlock()

	// 启动网络集成
	if err := r.networkIntegration.Start(); err != nil {
		return fmt.Errorf("网络集成启动失败: %w", err)
	}

	// 🆕 预注册创世验证者的Peer映射
	if r.config != nil && len(r.config.InitialDelegates) > 0 {
		for _, delegate := range r.config.InitialDelegates {
			if delegate == nil || delegate.MultiAddr == "" {
				continue
			}
			if err := r.networkIntegration.RegisterValidatorPeerFromMultiAddr(delegate.Address, delegate.MultiAddr); err != nil {
				r.logger.Warn("⚠️ 注册初始验证者Peer映射失败",
					"address", delegate.Address.String(),
					"multiAddr", delegate.MultiAddr,
					"error", err)
			} else {
				r.logger.Info("🛰️ 已加载初始验证者Peer映射",
					"address", delegate.Address.String(),
					"multiAddr", delegate.MultiAddr)
			}
		}
	}

	return nil
}
