package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/common"
	ethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// HostToStateDBAdapter 将 runtime.Host 适配成 vm.StateDB
type HostToStateDBAdapter struct {
	host   runtime.Host
	config *chain.ForksInTime
}

// NewHostToStateDBAdapter 创建 StateDB 适配器
func NewHostToStateDBAdapter(host runtime.Host, config *chain.ForksInTime) vm.StateDB {
	return &HostToStateDBAdapter{
		host:   host,
		config: config,
	}
}

// CreateAccount 创建账户
func (h *HostToStateDBAdapter) CreateAccount(addr common.Address) {
	// vcitychain 的 Host 接口没有显式的 CreateAccount 方法
	// 账户会在首次访问时自动创建，这里可以空实现
}

// SubBalance 减少余额
func (h *HostToStateDBAdapter) SubBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	// 将 uint256.Int 转换为 big.Int
	var amountBig *big.Int = amount.ToBig()
	// 通过 Transfer 实现：从 addr 转到零地址
	_ = h.host.Transfer(vcAddr, types.ZeroAddress, amountBig)
}

// AddBalance 增加余额
func (h *HostToStateDBAdapter) AddBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	// 将 uint256.Int 转换为 big.Int
	var amountBig *big.Int = amount.ToBig()
	// 通过 Transfer 实现：从零地址转到 addr
	_ = h.host.Transfer(types.ZeroAddress, vcAddr, amountBig)
}

// GetBalance 获取余额
func (h *HostToStateDBAdapter) GetBalance(addr common.Address) *uint256.Int {
	vcAddr := CommonAddressToVc(addr)
	balanceBig := h.host.GetBalance(vcAddr)
	// 将 big.Int 转换为 uint256.Int
	balance := new(uint256.Int)
	balance.SetFromBig(balanceBig)
	return balance
}

// GetNonce 获取 nonce
func (h *HostToStateDBAdapter) GetNonce(addr common.Address) uint64 {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetNonce(vcAddr)
}

// SetNonce 设置 nonce
func (h *HostToStateDBAdapter) SetNonce(addr common.Address, nonce uint64) {
	// vcitychain 的 Host 接口没有 SetNonce 方法
	// 这个操作通常由状态管理器处理，这里可以空实现或记录日志
}

// GetCodeHash 获取代码哈希
func (h *HostToStateDBAdapter) GetCodeHash(addr common.Address) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	hash := h.host.GetCodeHash(vcAddr)
	return VcHashToCommon(hash)
}

// GetCode 获取代码
func (h *HostToStateDBAdapter) GetCode(addr common.Address) []byte {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetCode(vcAddr)
}

// SetCode 设置代码
func (h *HostToStateDBAdapter) SetCode(addr common.Address, code []byte) {
	// vcitychain 的 Host 接口没有 SetCode 方法
	// 这个操作通常由状态管理器处理
}

// GetCodeSize 获取代码大小
func (h *HostToStateDBAdapter) GetCodeSize(addr common.Address) int {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetCodeSize(vcAddr)
}

// GetState 获取存储状态
func (h *HostToStateDBAdapter) GetState(addr common.Address, key common.Hash) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	vcKey := CommonHashToVc(key)
	value := h.host.GetStorage(vcAddr, vcKey)
	return VcHashToCommon(value)
}

// SetState 设置存储状态
func (h *HostToStateDBAdapter) SetState(addr common.Address, key, value common.Hash) {
	vcAddr := CommonAddressToVc(addr)
	vcKey := CommonHashToVc(key)
	vcValue := CommonHashToVc(value)
	h.host.SetState(vcAddr, vcKey, vcValue)
}

// Suicide 销毁账户（已废弃，使用 SelfDestruct）
func (h *HostToStateDBAdapter) Suicide(addr common.Address) bool {
	h.SelfDestruct(addr)
	return true
}

// SelfDestruct 销毁账户
func (h *HostToStateDBAdapter) SelfDestruct(addr common.Address) {
	vcAddr := CommonAddressToVc(addr)
	// 使用 Selfdestruct，beneficiary 设为零地址
	h.host.Selfdestruct(vcAddr, types.ZeroAddress)
}

// Selfdestruct6780 销毁账户（EIP-6780）
func (h *HostToStateDBAdapter) Selfdestruct6780(addr common.Address) {
	// EIP-6780 的行为与 SelfDestruct 相同，但只在同一交易中有效
	h.SelfDestruct(addr)
}

// HasSuicided 检查账户是否已销毁（已废弃，使用 HasSelfDestructed）
func (h *HostToStateDBAdapter) HasSuicided(addr common.Address) bool {
	return h.HasSelfDestructed(addr)
}

// HasSelfDestructed 检查账户是否已销毁
func (h *HostToStateDBAdapter) HasSelfDestructed(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	return !h.host.AccountExists(vcAddr) && h.host.Empty(vcAddr)
}

// Exist 检查账户是否存在
func (h *HostToStateDBAdapter) Exist(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	return h.host.AccountExists(vcAddr)
}

// Empty 检查账户是否为空
func (h *HostToStateDBAdapter) Empty(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	return h.host.Empty(vcAddr)
}

// PrepareAccessList 准备访问列表（EIP-2930，已废弃，使用 Prepare）
func (h *HostToStateDBAdapter) PrepareAccessList(sender common.Address, dest *common.Address, precompiles []common.Address, list ethTypes.AccessList) {
	// vcitychain 可能不支持 AccessList，这里可以空实现
}

// Prepare 准备访问列表（新版本）
func (h *HostToStateDBAdapter) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses ethTypes.AccessList) {
	// vcitychain 可能不支持 AccessList，这里可以空实现
}

// AddressInAccessList 检查地址是否在访问列表中
func (h *HostToStateDBAdapter) AddressInAccessList(addr common.Address) bool {
	// vcitychain 可能不支持 AccessList，返回 false
	return false
}

// SlotInAccessList 检查存储槽是否在访问列表中
func (h *HostToStateDBAdapter) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	// vcitychain 可能不支持 AccessList，返回 false
	return false, false
}

// AddAddressToAccessList 添加地址到访问列表
func (h *HostToStateDBAdapter) AddAddressToAccessList(addr common.Address) {
	// vcitychain 可能不支持 AccessList，这里可以空实现
}

// AddSlotToAccessList 添加存储槽到访问列表
func (h *HostToStateDBAdapter) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	// vcitychain 可能不支持 AccessList，这里可以空实现
}

// GetTransientState 获取临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	// 对于大多数情况，与 GetState 相同
	return h.GetState(addr, key)
}

// SetTransientState 设置临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) SetTransientState(addr common.Address, key, value common.Hash) {
	// 对于大多数情况，与 SetState 相同
	h.SetState(addr, key, value)
}

// AddRefund 增加退款
func (h *HostToStateDBAdapter) AddRefund(gas uint64) {
	// vcitychain 的 Host 接口没有 AddRefund 方法
	// 退款通常由状态管理器处理
}

// SubRefund 减少退款
func (h *HostToStateDBAdapter) SubRefund(gas uint64) {
	// vcitychain 的 Host 接口没有 SubRefund 方法
}

// GetRefund 获取退款
func (h *HostToStateDBAdapter) GetRefund() uint64 {
	return h.host.GetRefund()
}

// GetCommittedState 获取已提交的状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	// 对于大多数情况，与 GetState 相同
	return h.GetState(addr, key)
}

// Snapshot 创建快照
func (h *HostToStateDBAdapter) Snapshot() int {
	// vcitychain 的 Host 接口没有 Snapshot 方法
	// 快照通常由状态管理器处理，这里返回 0
	return 0
}

// RevertToSnapshot 恢复到快照
func (h *HostToStateDBAdapter) RevertToSnapshot(id int) {
	// vcitychain 的 Host 接口没有 RevertToSnapshot 方法
	// 快照恢复通常由状态管理器处理
}

// AddLog 添加日志
func (h *HostToStateDBAdapter) AddLog(log *ethTypes.Log) {
	vcAddr := CommonAddressToVc(log.Address)
	topics := make([]types.Hash, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = CommonHashToVc(topic)
	}
	h.host.EmitLog(vcAddr, topics, log.Data)
}

// AddPreimage 添加预映像（用于某些 EIP）
func (h *HostToStateDBAdapter) AddPreimage(hash common.Hash, preimage []byte) {
	// vcitychain 可能不支持预映像，这里可以空实现
}

// ForEachStorage 遍历存储（用于某些操作）
func (h *HostToStateDBAdapter) ForEachStorage(addr common.Address, cb func(common.Hash, common.Hash) bool) error {
	// vcitychain 的 Host 接口没有遍历存储的方法
	// 这里可以返回 nil 或实现一个简单的遍历逻辑
	return nil
}

// Commit 提交状态更改
func (h *HostToStateDBAdapter) Commit(deleteEmptyObjects bool) (common.Hash, error) {
	// vcitychain 的 Host 接口没有 Commit 方法
	// 提交通常由状态管理器处理，这里返回零哈希
	return common.Hash{}, nil
}

// IntermediateRoot 计算中间根
func (h *HostToStateDBAdapter) IntermediateRoot(deleteEmptyObjects bool) common.Hash {
	// vcitychain 的 Host 接口没有 IntermediateRoot 方法
	// 这里返回零哈希
	return common.Hash{}
}

