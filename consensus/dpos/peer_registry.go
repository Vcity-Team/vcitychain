package dpos

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Vcity-Team/vcitychain/network"
	networkCommon "github.com/Vcity-Team/vcitychain/network/common"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerRegistry 管理验证者地址与peer ID的映射
type PeerRegistry struct {
	validatorPeerMap map[types.Address]peer.ID
	peerValidatorMap map[peer.ID]types.Address
	mutex            sync.RWMutex
}

// NewPeerRegistry 创建新的peer注册表
func NewPeerRegistry() *PeerRegistry {
	return &PeerRegistry{
		validatorPeerMap: make(map[types.Address]peer.ID),
		peerValidatorMap: make(map[peer.ID]types.Address),
	}
}

// RegisterValidatorPeer 注册（或更新）验证者与peer的映射
func (pr *PeerRegistry) RegisterValidatorPeer(address types.Address, peerID peer.ID) {
	if address == (types.Address{}) || peerID == "" {
		return
	}

	pr.mutex.Lock()
	defer pr.mutex.Unlock()

	prevPeerID, exists := pr.validatorPeerMap[address]
	if exists && prevPeerID == peerID {
		return
	}

	if pr.validatorPeerMap == nil {
		pr.validatorPeerMap = make(map[types.Address]peer.ID)
	}
	if pr.peerValidatorMap == nil {
		pr.peerValidatorMap = make(map[peer.ID]types.Address)
	}

	pr.validatorPeerMap[address] = peerID
	pr.peerValidatorMap[peerID] = address
}

// RegisterValidatorPeerFromMultiAddr 使用MultiAddr注册验证者与peer的映射
func (pr *PeerRegistry) RegisterValidatorPeerFromMultiAddr(address types.Address, multiAddr string) error {
	multiAddr = strings.TrimSpace(multiAddr)
	if address == (types.Address{}) || multiAddr == "" {
		return fmt.Errorf("invalid validator address or multiAddr")
	}

	addrInfo, err := networkCommon.StringToAddrInfo(multiAddr)
	if err != nil {
		return fmt.Errorf("failed to parse multiAddr: %w", err)
	}

	pr.RegisterValidatorPeer(address, addrInfo.ID)
	return nil
}

// GetValidatorConnectivity 返回验证者的peer连接状态
func (pr *PeerRegistry) GetValidatorConnectivity(address types.Address, networkServer *network.Server) (peer.ID, bool, bool) {
	pr.mutex.RLock()
	peerID, exists := pr.validatorPeerMap[address]
	pr.mutex.RUnlock()

	if !exists || peerID == "" || networkServer == nil {
		return "", exists, false
	}

	// 检查peer是否已连接
	isConnected := networkServer.IsConnected(peerID)
	return peerID, true, isConnected
}

// GetPeerID 获取验证者的peer ID
func (pr *PeerRegistry) GetPeerID(address types.Address) (peer.ID, bool) {
	pr.mutex.RLock()
	defer pr.mutex.RUnlock()
	peerID, exists := pr.validatorPeerMap[address]
	return peerID, exists
}

// GetValidatorAddress 根据peer ID获取验证者地址
func (pr *PeerRegistry) GetValidatorAddress(peerID peer.ID) (types.Address, bool) {
	pr.mutex.RLock()
	defer pr.mutex.RUnlock()
	address, exists := pr.peerValidatorMap[peerID]
	return address, exists
}

// Clear 清空所有映射
func (pr *PeerRegistry) Clear() {
	pr.mutex.Lock()
	defer pr.mutex.Unlock()
	pr.validatorPeerMap = make(map[types.Address]peer.ID)
	pr.peerValidatorMap = make(map[peer.ID]types.Address)
}

