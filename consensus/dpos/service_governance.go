package dpos

import (
	"sync"
)

// governanceService 治理服务实现
type governanceService struct {
	dpos  *DPoS
	mutex sync.RWMutex
}

// NewGovernanceService 创建新的治理服务
func NewGovernanceService(dpos *DPoS) GovernanceService {
	return &governanceService{
		dpos: dpos,
	}
}

// CreateProposal 创建提案
func (gs *governanceService) CreateProposal(proposal *ParameterProposal) error {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	
	// 使用现有的CreateParameterProposal方法
	// 这里需要适配参数
	return nil
}

// VoteOnProposal 对提案投票
func (gs *governanceService) VoteOnProposal(proposalID string, vote *ParameterVote) error {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	
	// 使用现有的VoteOnParameterProposal方法
	// 签名: VoteOnParameterProposal(voter types.Address, proposalID string, support bool, privateKeyHex string)
	err := gs.dpos.VoteOnParameterProposal(vote.Voter, proposalID, vote.Support, "")
	return err
}

// CheckProposalResult 检查提案结果
func (gs *governanceService) CheckProposalResult(proposalID string) (ProposalStatus, error) {
	gs.mutex.RLock()
	defer gs.mutex.RUnlock()
	
	// 使用现有的CheckProposalResult方法
	// 注意：CheckProposalResult只返回error，需要从提案中获取状态
	err := gs.dpos.CheckProposalResult(proposalID)
	if err != nil {
		return ProposalPending, err
	}
	
	// 获取提案以获取状态
	proposal, err := gs.dpos.GetParameterProposal(proposalID)
	if err != nil {
		return ProposalPending, err
	}
	
	// 根据提案状态返回
	if proposal.Status == ProposalPassed {
		return ProposalPassed, nil
	} else if proposal.Status == ProposalRejected {
		return ProposalRejected, nil
	} else if proposal.Status == ProposalExecuted {
		return ProposalExecuted, nil
	} else if proposal.Status == ProposalActive {
		return ProposalActive, nil
	}
	
	return ProposalPending, nil
}

// ExecuteProposal 执行提案
func (gs *governanceService) ExecuteProposal(proposalID string) error {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	
	// 使用现有的提案执行逻辑
	// 这里需要调用governance_execute.go中的executeParameterProposalInTx
	return nil
}

// GetProposal 获取提案
func (gs *governanceService) GetProposal(proposalID string) (*ParameterProposal, error) {
	gs.mutex.RLock()
	defer gs.mutex.RUnlock()
	
	// 使用现有的GetParameterProposal方法
	return gs.dpos.GetParameterProposal(proposalID)
}

// GetActiveProposals 获取活跃提案
func (gs *governanceService) GetActiveProposals() ([]*ParameterProposal, error) {
	gs.mutex.RLock()
	defer gs.mutex.RUnlock()
	
	// 使用现有的GetActiveProposals方法
	return gs.dpos.GetActiveProposals()
}

// GetCurrentParameterValues 获取当前参数值
func (gs *governanceService) GetCurrentParameterValues() map[string]interface{} {
	gs.mutex.RLock()
	defer gs.mutex.RUnlock()
	
	// 使用现有的GetCurrentParameterValues方法
	return gs.dpos.GetCurrentParameterValues()
}

