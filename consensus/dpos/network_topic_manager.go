package dpos

import (
	"fmt"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/golang/protobuf/proto"
)

// getSignatureRequestTopic 获取签名请求主题
func (r *dposRuntime) getSignatureRequestTopic() (*network.Topic, error) {
	// 首先检查网络服务是否可用
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	r.topicMutex.RLock()
	if r.signatureRequestTopic != nil {
		defer r.topicMutex.RUnlock()
		return r.signatureRequestTopic, nil
	}
	r.topicMutex.RUnlock()

	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()

	// 双重检查
	if r.signatureRequestTopic != nil {
		return r.signatureRequestTopic, nil
	}

	// 再次检查网络服务（双重检查）
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	// 使用有效的默认值创建主题，避免网络层发送零值消息
	// 创建一个模板消息作为主题初始化，这样网络层就不会发送无效的默认消息
	defaultRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,
		BlockHash:      []byte("TEMPLATE_MSG"),
		CheckpointHash: []byte("TEMPLATE_MSG"),
		Round:          0,
		Proposer:       types.Address(r.config.Key.Address()).Bytes(),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认请求
	defaultRequestData, err := proto.Marshal(defaultRequest)
	if err != nil {
		r.logger.Warn("failed to marshal default request template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultRequestData = []byte("TEMPLATE_MSG")
	}

	// 创建 DPOSMessage 作为模板
	defaultDPOSMessage := &dposProto.TransportMessage{
		Data: defaultRequestData,
	}

	// 首先检查网络集成层是否已经有现有主题
	if r.networkIntegration != nil {
		if existingTopic := r.networkIntegration.GetSignatureRequestTopic(); existingTopic != nil {
			r.signatureRequestTopic = existingTopic
			return existingTopic, nil
		}
	}

	// 如果网络集成层没有，尝试创建新主题
	r.logger.Info("🔍 DPoS尝试创建签名请求主题", "topic", "dpos-signature-request", "网络集成层状态", r.networkIntegration != nil)
	topic, err := r.network.NewTopic("dpos-signature-request", defaultDPOSMessage)
	if err != nil {
		r.logger.Error("🔍 DPoS创建主题失败", "topic", "dpos-signature-request", "错误类型", fmt.Sprintf("%T", err), "错误信息", err.Error())

		// 如果主题已存在，再次检查网络集成层
		if strings.Contains(err.Error(), "topic already exists") {
			r.logger.Info("🔍 主题已存在，再次检查网络集成层", "topic", "dpos-signature-request")

			// 再次检查网络集成层是否有现有主题
			if r.networkIntegration != nil {
				if existingTopic := r.networkIntegration.GetSignatureRequestTopic(); existingTopic != nil {
					r.logger.Info("🔗 从网络集成层获取到现有主题", "topic", "dpos-signature-request")
					r.signatureRequestTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果网络集成层也没有，等待一下再重试
			r.logger.Info("等待网络层同步后重试", "topic", "dpos-signature-request")
			time.Sleep(200 * time.Millisecond)

			// 最后一次检查网络集成层
			if r.networkIntegration != nil {
				if existingTopic := r.networkIntegration.GetSignatureRequestTopic(); existingTopic != nil {
					r.logger.Info("🔗 延迟获取到网络集成层主题", "topic", "dpos-signature-request")
					r.signatureRequestTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果仍然没有，返回错误而不是创建新主题
			r.logger.Error("无法获取签名请求主题，网络集成层也未提供", "topic", "dpos-signature-request")
			return nil, fmt.Errorf("signature request topic already exists but not available from network integration")
		} else {
			r.logger.Warn("创建签名请求主题失败", "error", err)
			return nil, fmt.Errorf("failed to create signature request topic: %w", err)
		}
	}

	r.signatureRequestTopic = topic

	// 存储实际使用的protoID
	actualProtoID := topic.GetActualProtoID()
	r.logger.Debug("🔍 签名请求Topic名称对比", "原始名称", "dpos-signature-request", "实际名称", actualProtoID)

	r.logger.Debug("成功创建签名请求主题")
	return topic, nil
}

// getSignatureResponseTopic 获取签名响应主题
func (r *dposRuntime) getSignatureResponseTopic() (*network.Topic, error) {
	// 首先检查网络服务是否可用
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	r.topicMutex.RLock()
	if r.signatureResponseTopic != nil {
		defer r.topicMutex.RUnlock()
		return r.signatureResponseTopic, nil
	}
	r.topicMutex.RUnlock()

	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()

	// 双重检查
	if r.signatureResponseTopic != nil {
		return r.signatureResponseTopic, nil
	}

	// 再次检查网络服务（双重检查）
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	// 使用有效的默认值创建主题，避免网络层发送零值消息
	// 创建一个有效的签名响应作为模板
	defaultResponse := &dposProto.SignatureResponse{
		ValidatorAddr:  types.Address(r.config.Key.Address()).Bytes(),
		Signature:      []byte("DEFAULT_SIGNATURE"),
		CheckpointHash: types.Hash{}.Bytes(), // 使用零哈希，但这是合法的
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认响应
	defaultResponseData, err := proto.Marshal(defaultResponse)
	if err != nil {
		r.logger.Warn("failed to marshal default response template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultResponseData = []byte("DEFAULT_RESPONSE")
	}

	// 创建 DPOSMessage 作为模板
	defaultDPOSMessage := &dposProto.TransportMessage{
		Data: defaultResponseData,
	}

	// 首先检查网络集成层是否已经有现有主题
	if r.networkIntegration != nil {
		if existingTopic := r.networkIntegration.GetSignatureResponseTopic(); existingTopic != nil {
			r.logger.Info("🔗 复用网络集成层的签名响应主题", "topic", "dpos-signature-response")
			r.signatureResponseTopic = existingTopic
			return existingTopic, nil
		}
	}

	// 如果网络集成层没有，尝试创建新主题
	topic, err := r.network.NewTopic("dpos-signature-response", defaultDPOSMessage)
	if err != nil {
		// 如果主题已存在，再次检查网络集成层
		if strings.Contains(err.Error(), "topic already exists") {
			r.logger.Debug("主题已存在，再次检查网络集成层", "topic", "dpos-signature-response")

			// 再次检查网络集成层是否有现有主题
			if r.networkIntegration != nil {
				if existingTopic := r.networkIntegration.GetSignatureResponseTopic(); existingTopic != nil {
					r.logger.Info("🔗 从网络集成层获取到现有主题", "topic", "dpos-signature-response")
					r.signatureResponseTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果网络集成层也没有，等待一下再重试
			r.logger.Info("等待网络层同步后重试", "topic", "dpos-signature-response")
			time.Sleep(200 * time.Millisecond)

			// 最后一次检查网络集成层
			if r.networkIntegration != nil {
				if existingTopic := r.networkIntegration.GetSignatureResponseTopic(); existingTopic != nil {
					r.logger.Info("🔗 延迟获取到网络集成层主题", "topic", "dpos-signature-response")
					r.signatureResponseTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果仍然没有，返回错误而不是创建新主题
			r.logger.Error("无法获取签名响应主题，网络集成层也未提供", "topic", "dpos-signature-response")
			return nil, fmt.Errorf("signature response topic already exists but not available from network integration")
		} else {
			r.logger.Warn("创建签名响应主题失败", "error", err)
			return nil, fmt.Errorf("failed to create signature response topic: %w", err)
		}
	}

	r.signatureResponseTopic = topic

	// 存储实际使用的protoID
	actualProtoID := topic.GetActualProtoID()
	r.logger.Debug("🔍 签名响应Topic名称对比", "原始名称", "dpos-signature-response", "实际名称", actualProtoID)

	r.logger.Info("成功创建签名响应主题")
	return topic, nil
}

// getSignatureQueryTopic 获取签名查询主题
func (r *dposRuntime) getSignatureQueryTopic() (*network.Topic, error) {
	// 简化的实现，返回nil表示暂时不实现
	return nil, fmt.Errorf("signature query topic not implemented")
}

