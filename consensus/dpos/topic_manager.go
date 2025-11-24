package dpos

import (
	"fmt"
	"strings"
	"sync"

	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/proto"
)

// TopicConfig 定义topic配置
type TopicConfig struct {
	Name         string        // topic名称
	MessageType  proto.Message // protobuf消息类型
	IsCritical   bool          // 是否为关键topic（创建失败会阻塞）
	TemplateData []byte        // 模板数据（用于初始化）
	LogName      string        // 日志中显示的名称
}

// Topic 类型别名，简化引用
type Topic = network.Topic

// TopicManager 管理所有DPoS topics
type TopicManager struct {
	network *network.Server
	logger  hclog.Logger
	topics  map[string]*Topic
	mutex   sync.RWMutex
}

// NewTopicManager 创建topic管理器
func NewTopicManager(network *network.Server, logger hclog.Logger) *TopicManager {
	return &TopicManager{
		network: network,
		logger:  logger,
		topics:  make(map[string]*Topic),
	}
}

// CreateTopic 创建单个topic（统一错误处理）
func (tm *TopicManager) CreateTopic(config TopicConfig) (*Topic, error) {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	// 检查是否已存在
	if existingTopic, exists := tm.topics[config.Name]; exists {
		tm.logger.Debug("Topic已存在于管理器", "name", config.Name)
		return existingTopic, nil
	}

	// 创建TransportMessage
	var transportMsg proto.Message
	if config.TemplateData != nil {
		transportMsg = &dposProto.TransportMessage{
			Data: config.TemplateData,
		}
	} else {
		transportMsg = &dposProto.TransportMessage{
			Data: nil,
		}
	}

	// 尝试创建topic
	topic, err := tm.network.NewTopic(config.Name, transportMsg)
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			tm.logger.Warn("主题已存在，跳过创建", "name", config.Name, "logName", config.LogName)
			// topic已存在但无法获取，返回nil但不报错
			return nil, nil
		}
		// 其他错误，如果是关键topic则返回错误
		if config.IsCritical {
			return nil, fmt.Errorf("failed to create critical topic %s (%s): %w", config.Name, config.LogName, err)
		}
		// 非关键topic，记录警告但继续
		tm.logger.Warn("创建非关键topic失败", "name", config.Name, "logName", config.LogName, "error", err)
		return nil, nil
	}

	// 成功创建，记录日志
	if config.IsCritical && topic != nil {
		actualProtoID := topic.GetActualProtoID()
		tm.logger.Debug("🔍 网络集成层Topic名称对比",
			"原始名称", config.Name,
			"实际名称", actualProtoID,
			"logName", config.LogName)
	}

	// 存储到map
	tm.topics[config.Name] = topic
	return topic, nil
}

// CreateAllTopics 批量创建所有topics
func (tm *TopicManager) CreateAllTopics() error {
	// 准备签名请求主题的模板数据
	defaultSignatureRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,
		BlockHash:      []byte("TEMPLATE_MSG"),
		CheckpointHash: []byte("TEMPLATE_MSG"),
		Round:          0,
		Proposer:       []byte("DEFAULT_PROPOSER"),
		Timestamp:      0, // 使用0而不是time.Now()，避免每次调用都变化
	}
	defaultRequestData, err := proto.Marshal(defaultSignatureRequest)
	if err != nil {
		tm.logger.Warn("failed to marshal default signature request template", "error", err)
		defaultRequestData = []byte("TEMPLATE_MSG")
	}

	// 准备签名响应主题的模板数据
	defaultSignatureResponse := &dposProto.SignatureResponse{
		ValidatorAddr:  []byte("DEFAULT_VALIDATOR"),
		Signature:      []byte("DEFAULT_SIGNATURE"),
		CheckpointHash: []byte("DEFAULT_CHECKPOINT"),
		Timestamp:      0,
	}
	defaultResponseData, err := proto.Marshal(defaultSignatureResponse)
	if err != nil {
		tm.logger.Warn("failed to marshal default signature response template", "error", err)
		defaultResponseData = []byte("DEFAULT_RESPONSE")
	}

	// 配置表：定义所有topics
	configs := []TopicConfig{
		{
			Name:         "dpos-signature-request",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   true,
			TemplateData: defaultRequestData,
			LogName:      "签名请求主题",
		},
		{
			Name:         "dpos-signature-response",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   true,
			TemplateData: defaultResponseData,
			LogName:      "签名响应主题",
		},
		{
			Name:         "dpos-vote",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "投票主题",
		},
		{
			Name:         "dpos-delegate",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "委托主题",
		},
		{
			Name:         "dpos-bls-key-broadcast",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "BLS公钥广播主题",
		},
		{
			Name:         "dpos-bls-key-ack",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "BLS公钥确认主题",
		},
		{
			Name:         "dpos-bls-key-request",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "BLS公钥请求主题",
		},
		{
			Name:         "dpos-bls-key-response",
			MessageType:  &dposProto.TransportMessage{},
			IsCritical:   false,
			TemplateData: nil,
			LogName:      "BLS公钥响应主题",
		},
	}

	// 统计关键topics创建情况
	criticalTopicsCreated := 0
	var criticalTopicErrors []error

	// 统一创建所有topics
	for _, config := range configs {
		topic, err := tm.CreateTopic(config)
		if err != nil {
			if config.IsCritical {
				criticalTopicErrors = append(criticalTopicErrors, err)
			}
			// 继续处理其他topics
			continue
		}

		if topic != nil && config.IsCritical {
			criticalTopicsCreated++
		}
	}

	// 检查是否至少有一个关键topic可用
	if criticalTopicsCreated == 0 {
		errorMsg := "无法创建任何关键主题"
		if len(criticalTopicErrors) > 0 {
			errorMsg += fmt.Sprintf(": %v", criticalTopicErrors)
		}
		tm.logger.Error(errorMsg)
		return fmt.Errorf("failed to create any critical topics")
	}

	return nil
}

// GetTopic 获取已创建的topic
func (tm *TopicManager) GetTopic(name string) *Topic {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()
	return tm.topics[name]
}

// CloseAll 关闭所有topics
func (tm *TopicManager) CloseAll() {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	for name, topic := range tm.topics {
		if topic != nil {
			topic.Close()
			tm.logger.Debug("已关闭topic", "name", name)
		}
	}

	// 清空map
	tm.topics = make(map[string]*network.Topic)
}

// GetAllTopics 获取所有topics的映射（用于调试）
func (tm *TopicManager) GetAllTopics() map[string]*Topic {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	// 返回副本
	result := make(map[string]*Topic)
	for k, v := range tm.topics {
		result[k] = v
	}
	return result
}
