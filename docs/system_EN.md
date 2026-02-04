# VCityChain DPoS System Description

This document provides an overview of the DPoS (Delegated Proof of Stake) consensus module of VCity Chain, including voting, economic system, governance, fault handling, and other core mechanisms.

---

## **I. Overview**

### **1.1 DPoS Introduction**

- Basic concepts of the DPoS consensus mechanism
- Characteristics and advantages of DPoS in VCity Chain
- Module architecture overview

### **1.2 Core Concepts**

- Validator (Validator/Delegate)
- Voter
- Candidate (Candidate/SR)
- Stake
- Voting Power

### **1.3 Epoch Mechanism**

- **Epoch concept**:
  - Fixed time window (default 24 hours, configurable)
  - Each epoch contains a fixed number of blocks
  - **Epoch size calculation**: `epochSize = epochDuration / blockTime`
    - Example: epochDuration = 24h = 86400s, blockTime = 3s → epochSize = 28800 blocks
    - Code: `getEpochSize() = uint64(EpochDuration / BlockTime)`
- Epoch is the basic time unit for reward distribution, fault detection, and state updates
- **Epoch calculation**:
  - Epoch number starts from the consensus switch height (`ConsensusSwitchHeight`)
  - Formula: `currentEpoch = ((blockNumber - consensusSwitchHeight) / epochSize) + 1`
  - The first DPoS block is Epoch 1
- **Epoch configuration**:
  - `EpochDuration`: code `d.config.EpochDuration`; YAML `epochDuration` or `dpos_epoch_duration` (string, e.g. "24h"); default 24h
  - `BlockTime`: code `d.config.BlockTime.Duration`; YAML `blockTime` or `block_time_s` (string e.g. "2s" or uint64 seconds); default 3s
- **Importance of epoch boundaries**: at each epoch boundary the system runs reward calculation/distribution, validator fault detection, validator set updates (voting weight changes), and execution of passed governance proposal schedules, ensuring atomic and consistent state updates.

---

## **II. Block Production and Fork Handling**

### **2.1 Slot Mechanism Overview**

- **Slot**: a fixed time window; each validator produces blocks in its own slot. Computed from absolute time, not block number.
- Slot-based design ensures precise and predictable block times.
- **Slot vs block**: each slot corresponds to one block opportunity; normally one block per slot; if a validator fails, the slot is skipped; if block production exceeds the slot, it is cut off and the turn moves to the next validator.

### **2.2 Slot Calculation**

- **Formula**: `currentSlot = (currentTime - genesisTime) / blockWindow`
  - Example: genesis 2024-01-01 00:00:00, now 2024-01-01 00:00:12, blockWindow 3s → currentSlot = 12/3 = 4
- **Genesis time**: DPoS genesis time = timestamp of the block before the consensus switch block (e.g. if switch height is 7370, use block 7369’s timestamp).
- **BlockWindow**: from `d.config.BlockTime.Duration`, default 3s; validators must produce within this window.

### **2.3 Block Producer Selection**

- **Formula**: `expectedValidatorIndex = currentSlot % validatorCount`
  - Example: slot 100, 21 validators → index 16 (0-based) should produce.
- **Validator list order**: by voting power (desc), then by address (asc) for ties; ensures all nodes agree on the producer.
- **Time window check**: slotStart = genesisTime + (currentSlot × blockWindow), slotEnd = slotStart + blockWindow; if now &lt; slotStart wait; if now in [slotStart, slotEnd] produce; if now &gt; slotEnd (with 500ms tolerance) skip.
- **Flow**: compute currentSlot → index = currentSlot % validatorCount → get validator address → check if local node is producer → check time window → build block if both pass.

### **2.4 Fork Prevention**

- **Before producing**: (1) Ensure chain head is still the expected parent; if not, discard the built block. (2) Track lastProducedSlot to avoid producing twice in the same slot.
- **Reorg**: On new block, compare total difficulty (TD). DPoS sets `header.Difficulty = 1` for all blocks; TD = sum of difficulties ≈ block count. If incoming chain TD &gt; current TD, run `handleReorg`: find common ancestor, revert old chain, apply new chain, update canonical hash, trigger state rollback/replay. Lower-TD chain is stored as fork.

### **2.5 Block Production Example**

- Genesis 2024-01-01 00:00:00, blockWindow 3s, 21 validators [A..U]. At T = 00:00:10: timeSinceGenesis = 12s, currentSlot = 4, expectedIndex = 4 → validator E; slot [00:00:12, 00:00:15]; E can produce. If E fails in that window, the slot is lost; next slot (e.g. 6) goes to G; E is recorded as missed blocks and at epoch end may be removed from the producer set if over the threshold.

### **2.6 Fork Resolution Example**

- Chain A at block 100 (TD 100). Validators X and Y both produce at slot 101 → chains B and C both have TD 101. Nodes pick the first received as canonical (e.g. B). If a node was on C, it reorgs: common ancestor 100, revert 101-Y, apply 101-X, update canonical, sync state.

---

## **III. Voting Mechanism**

### **3.1 Staking Thresholds and Parameters**

- **SR candidate deposit** (`SRThreshold`): YAML `dpos_SR_threshold` (string, wei). Default 0 (code default 100 VCITY). Required to become a candidate and receive votes.
- **Min voting power** (`MinVotingPower`): YAML `dpos_delegate_threshold`. Default 1e21 (1000 VCITY). Used as governance voting threshold (`governance_min_voting_threshold`); VCity design: only accounts with at least this stake can vote on governance proposals. In practice often set equal to `dpos_SR_threshold`.
- **Min vote amount** (`MinVoteAmount`): code constant 1e18 (1 VCITY); minimum per vote.

### **3.2 Becoming a Candidate**

- Must meet `dpos_SR_threshold`. Flow: stake VCITY to threshold → system checks → register as candidate and accept votes.

### **3.3 Voting for Validators**

- Flow: user stakes VCITY → call vote API (candidate address + amount) → system checks balance and remaining votable amount → update candidate voting power in memory → persist vote (`persistVoteToDatabase`). Weight = vote amount; votes are locked for VoteLockTime. State is persisted so new nodes can recover it from chain sync.

### **3.4 Block Producer Set and Replacement**

- **Block producer set**: top N by voting power (e.g. 21), limited by `maxValidatorSetSize`.
- **Update design**: votes update weight in memory immediately, but producer set updates only at epoch boundary (`pendingValidatorUpdate`). At epoch end, sort by weight (then address), take top N; replaced validators leave the set. BlockScheduler assigns fixed time slots; replacement takes effect at the next epoch.

### **3.5 Voting Examples**

- Example 1: A has 5000 VCITY, B (candidate) has 0; A votes 3000 to B → B’s weight 3000; if that is enough for top 21 (per config), B joins the producer set.
- Example 2: You have 10,000 VCITY and can split votes (e.g. 4000 + 3000 + 3000); system accumulates with totalVotedAmount. Once total ≥ 10,000, further votes are rejected with “insufficient remaining votable amount…”; you must add balance or revoke votes to free capacity.
- **Note**: Anyone can delegate; you can vote for multiple candidates (A, B, C…); total vote amount cannot exceed your balance.

---

## **IV. Economic System**

### **4.1 Reward Parameters**

- **RewardAmount**: YAML `dpos_reward_amount` (string, wei). Default 1e21 (1000 VCITY). Governable.
- **BlockTime**: YAML `blockTime` / `block_time_s`. Default 2s. Used for expected block count.
- **EpochDuration**: YAML `epochDuration` / `dpos_epoch_duration`. Default 24h. Used for expected blocks per epoch.

### **4.2 Reward Calculation**

1. Expected blocks per epoch = epochDuration / blockTime (e.g. 86400/3 = 28800).
2. Reward per block = RewardAmount / expectedBlocks (e.g. 1000/5 = 200 VCITY per block).
3. Validator reward = rewardPerBlock × actualBlocksProduced (e.g. 200 × 8 = 1600 VCITY).
4. Voter reward: each voter gets a share of the validator’s reward by vote proportion. Example: producer A gets 1600; 10% of A’s votes are from voter B; if commission is 30%, A keeps 1600×30%, B gets 1600×10%×(1−30%).

### **4.3 Reward Distribution**

- **When**: at each epoch end block.
- **BlockProductionTracker**: records per-validator block production per epoch; used for reward calculation and fault detection; persisted.
- **Flow**: at epoch end call `distributeEpochRewards`, get actual blocks from tracker, compute validator and voter rewards, write `RewardDistributionInfo` to block ExtraData; all nodes read it and execute transfers; rewards are paid from `RewardAccount`.

---

## **V. Governance**

### **5.1 Overview**

- Proposal types: Parameter Proposal, Validator Recovery Proposal.

### **5.2 Proposal Creation**

- Only validators can create proposals.
- **Governable parameters** (examples): `dpos_reward_amount` (range 1e18–1e24 wei, default 1000 VCITY), `dpos_delegate_threshold` (range 1e18–1e23 wei, default 1000 VCITY). All have range checks.
- **Parameter proposal**: validate param is votable → get OldValue → validate new value → sign proposal → set blocks: StartBlock = currentBlock+1, EndBlock = currentBlock+getVotePeriod(), ValidEndBlock = currentBlock+getValidPeriod(). getVotePeriod() = ProposalVotePeriod/BlockTime (YAML `dpos_proposal_vote_period`, e.g. "24h", default 28800 blocks). getValidPeriod() = ProposalValidPeriod/BlockTime (YAML `dpos_proposal_valid_period`, e.g. "7d", default 201600 blocks). Save to ProposalStore, state Pending.
- **Recovery proposal**: only for validators with isFaulty=true; includes RecoveryReason. Execution is scheduled to take effect at the next epoch boundary.

### **5.3 Voting**

- Only validators can vote; within [StartBlock, EndBlock]; one vote per address. Weight: one validator one vote (not by amount). Pass condition: support rate ≥ `governance_voting_threshold`.

### **5.4 Viewing Proposals**

- RPC: list proposals, proposal detail (status, votes, execution). States: Pending → Active → Passed/Rejected → Executed.

### **5.5 Execution**

- After a proposal passes, anyone can execute it within the valid period (EndBlock to ValidEndBlock). ValidEndBlock is set at creation (currentBlock + getValidPeriod()), not from EndBlock.
- **Parameter proposal**: execute → update parameter immediately. **Recovery proposal**: execute → schedule (Scheduled=true, EffectiveEpoch=next epoch, Applied=false); applied at that epoch boundary.
- Execution flow: verify state=Passed and block ≤ ValidEndBlock → update parameter or register recovery schedule → set state Executed, record ExecutedAt/ExecutedBy → persist.

### **5.6 Example: Change Reward Amount**

- Vote period 24h, valid period 7d, blockTime 3s → getVotePeriod()=28800, getValidPeriod()=201600. Create at block 1000: dpos_reward_amount 1000→1500 VCITY. StartBlock=1001, EndBlock=44200, ValidEndBlock=202600. During 1001–44200, B and C vote support (8000 VCITY total); support 100% → Passed. At block 50000 (&lt;202600) someone executes → reward amount becomes 1500 VCITY, state Executed.

---

## **VI. Fault Detection and Punishment**

### **6.1 Fault Detection**

- **When**: at each epoch end block (for the epoch that just ended).
- Use BlockProductionTracker: actual blocks per validator vs expected (epochSize = epochDuration/blockTime). Missed blocks = expected − actual; missed rate = missed/expected. If missed rate ≥ `dpos_missed_blocks_percentage`, set IsFaulty=true. Threshold in YAML `dpos_missed_blocks_percentage`; balance false positives vs missed faults.

### **6.2 Punishment**

- **Minor offense** (e.g. node down, missed blocks): config `dpos_minor_offense_slash_rate`.
- **Severe offense** (double-sign: same validator signs two different block hashes at same height): config `dpos_severe_offense_slash_rate`.
- On trigger: remove validator from producer set immediately; mark IsFaulty=true; slash all stakes voting for that validator by configured rate (to burn address); next candidates by weight replace; state persisted.

### **6.3 Fault Handling**

- At epoch end: `detectValidatorFaults` → FaultFlagInfo (NodeAddress, IsFaulty, MissedBlocks, ActualBlocks, EpochNumber, LastFaultyEpoch, Reason) → write to ExtraData (extra.FaultFlags). All nodes sync fault state from ExtraData; persist via StakeStore and in-memory faultyValidators. On blocks with fault flags, call `updateBlockProducersFromFaultFlags`; remove faulty from producers (`updateMemoryFaultStatus`), update set, persist (`updateValidatorFaultStatus`).

### **6.4 Replacement**

- After removal, next candidate by weight replaces; new set effective from next epoch start.

---

## **VII. Fault Recovery**

### **7.1 Recovery**

- Node must be faulty and have been fixed; recovery is via governance (recovery proposal), not automatic.

### **7.2 Creating a Recovery Proposal**

- Only validators can create. Node must be faulty; create proposal type validator_recovery with RecoveryReason; sign and submit. Old value: fault state (isFaulty=true, missedBlocks, lastFaultyEpoch); new value: recovered (isFaulty=false, missedBlocks=0).

### **7.3 Lifecycle**

- Voting: same rules as parameter proposals; only validators vote. After pass, anyone can execute within valid period. Execution does not apply immediately; it is scheduled to take effect at the next epoch boundary.

### **7.4 Example**

- Epoch 5 end: A marked faulty and removed. During Epoch 6 A fixes the node. At block 300 B creates recovery proposal; blocks 301–400 voting; pass. Block 401 execute → scheduled for Epoch 7. Epoch 7 start: A’s fault cleared, A back in producer set.

---

## **VIII. Transaction Types and Gas Flow**

The code supports both:

### **8.1 EIP-1559 (DynamicFeeTx)**

- Fields: maxFeePerGas (GasFeeCap), maxPriorityFeePerGas (GasTipCap). Effective Tip = min(GasTipCap, GasFeeCap - BaseFee). Base Fee and Effective Tip (gasUsed × respective rate) both go to Coinbase (validator). So currently all gas goes to the producer.

### **8.2 Legacy Tx**

- Effective Tip = GasPrice - BaseFee (BaseFee is deducted).

Config: `base_fee_config: "1000000000:2:8"` → BaseFee = 1 Gwei, BaseFeeEM = 2, BaseFeeChangeDenom = 8. In tests, burn can be a separate address from the reward distribution address for easier gas tracking.

![](static/Om99b6dXjoCrFLxiHFTcHNOsnUd.png)

---

## **IX. Rollback**

Rollback is a last resort and has large impact; use with care.

### **9.1 Steps**

1. Stop the process (e.g. `kill -9 pid`).
2. Remove lock: `rm node*/blockchain/LOCK`.
3. Run rollback (e.g. to height 100000):  
   `./vcitychain rollback --config ./node1/node-config-validator.yaml --target-height 100000 --force`

Output example: Rollback completed; Current Height | 100500; Target Height | 100000; Target Hash | 0x...; Blocks Deleted | 500; Keep Blocks | false. The `--force` flag skips interactive confirmation.

### **9.2 Rollback Logic and Scope**

- Check node is stopped → open chain DB (LevelDB) → validate target height → load epoch config → rollback chain (update head, remove canonical mapping, delete block data, tx index, state snapshots) → rollback DPoS state (epochs, validatorSnapshots, EpochBlocks, VotingPowerAtBlock, parameters, proposals, validatorFaultStatus) → clear rewards DB (dpos.db.rewards) → print result.
- **Scope**: LevelDB blockchain/; BoltDB dpos.db; BoltDB dpos.db.rewards.

---

## **X. Configuration**

### **10.1 Testnet (example)**

- dpos_validators_count: 21  
- dpos_missed_blocks_percentage: 1000 (10% in basis points)  
- dpos_minor_offense_slash_rate: 50 (0.5%)  
- dpos_severe_offense_slash_rate: 1000 (10%)  
- base_fee_config: "1000000000:2:8"  
- burn_contract: "0:0x0000...0000" (empty; base fee to producer)  
- dpos_proposal_vote_period: "1d", dpos_proposal_valid_period: "5d"  
- dpos_delegate_threshold: "1000000000000000000000" (1000 VCITY)  
- dpos_epoch_duration: "1h"  
- dpos_reward_distribution: "0x4BCB..." (reward account, e.g. genesis root)  
- dpos_reward_amount: "120000000000000000000" (120 VCITY per epoch)  
- dpos_min_freeze_period: 300 (5 min), dpos_unfreeze_lock_period: 600 (10 min)  
- dpos_commission_radio: 1000 (10%), dpos_commission_effective: 30m  

**Notes**: dpos_missed_blocks_percentage = fault threshold; dpos_minor_offense_slash_rate / dpos_severe_offense_slash_rate = slash rates. dpos_validators_count sets committee size (e.g. 21) and affects BFT tolerance ⌊(n−1)/3⌋. base_fee_config = baseFee:baseFeeEM:baseFeeChangeDenom; burn_contract = blockNumber:address for where to send base fee/slash (0 = burn). dpos_delegate_threshold = delegate registration deposit (wei) and often governance voting threshold. dpos_validator_reward_ratio / dpos_voter_reward_ratio split epoch reward (e.g. 70% / 30%); must sum to 100.

### **10.2 Production (example)**

Same as [DPoS Economics Design](https://lcnxt0aw7kvz.feishu.cn/wiki/VQo2wYWPZi3crEkWw8GcwzVSnDg). Example differences: dpos_min_freeze_period: 86400 (1 day), dpos_unfreeze_lock_period: 432000 (5 days), dpos_commission_effective: 604800 (7 days).

### **10.3 Yield (APY)**

- APY depends on total staked (higher stake → lower APY). With 21 validators, 120 VCITY/epoch, 1h epoch: 8760 epochs/year, 1,051,200 VCITY/year; 10% commission.  
- Voter APY: voter annual reward = 1,051,200 × 90% = 945,600 VCITY; APY = (945,600 / totalStaked) × 100%.  
- Validator APY: validator commission = 1,051,200 × 10% = 105,120 VCITY; APY depends on validator stake and block share.  
- Formulas and example tables: see original doc (static images and code). Summary: voter APY inversely proportional to total stake; validator APY depends on own stake, block share, and votes received; 10% commission ⇒ 90% to voters, 10% to validators.

---

## **XI. DPoS RPC API**

See [VCityChain DPoS API](https://lcnxt0aw7kvz.feishu.cn/wiki/EWJBwpYb4i87EnkFykNcBhPEnSf?fromScene=spaceOverview).

---

## **XII. Fault and Proposal Lifecycle**

### **12.1 Fault**

- **Occurrence**: During Epoch N validator misses blocks; rate above threshold; still in current set but marked faulty.
- **Detection**: At Epoch N end block: count blocks per validator, compute missed rate, run detectValidatorFaults(), save to DB, update memory, write pendingFaultFlags to ExtraData.
- **Validator set**: Same block: apply any recovery proposals first → run fault detection → calculateNextEpochValidators() (exclude faulty) → write new set to ExtraData.
- **Effect**: From Epoch N+1 start, faulty node is excluded from producers.

### **12.2 Proposal**

- **Execution**: When execute tx is mined: executeRecoveryProposalInTx(); effectiveEpoch = currentEpoch (if not at epoch end block) or currentEpoch+1 (if at epoch end); mark Scheduled=true, ProposalExecuted; save, wait for boundary.
- **Application**: At effectiveEpoch end block (before computing validator set in buildBlock): collect recovery proposals with EffectiveEpoch == currentEpoch and not applied; clear fault flags (DB + memory), reload validator set, set Applied=true. Fault detection and set calculation run after applying recovery.
- **Recovery effect**: From Epoch N+1 start, recovered node is in the new set and produces again.

### **12.3 Examples**

- **Example 1 (fault)**: Epoch 10 blocks 7370–7409; A misses in Epoch 10. At block 7409: detect fault, save, compute Epoch 11 set without A, write ExtraData. From block 7410 (Epoch 11), A is excluded.
- **Example 2 (recovery)**: A faulty in Epoch 10, excluded in Epoch 11. In Epoch 11: create and pass recovery, execute at block 7440 (not epoch end) → effectiveEpoch=11. At block 7449 (Epoch 11 end): apply recovery (clear A’s fault), then fault detection, then Epoch 12 set includes A. From block 7450 (Epoch 12), A produces again.
- **Example 3 (execute at epoch end)**: Recovery executed at block 7449 (Epoch 11 end) → effectiveEpoch=12. At Epoch 12 end block 7489: apply recovery, then compute Epoch 13 set with A. From block 7490 (Epoch 13), A produces.

**Summary tables** (abbreviated):

| Phase        | When           | Action                    | Effect                |
|-------------|----------------|---------------------------|------------------------|
| Fault occur | During Epoch N | Node misses blocks        | Still producing       |
| Fault detect| Epoch N end    | Detect, save              | DB updated            |
| Set calc    | Epoch N end    | Exclude faulty            | New set in ExtraData  |
| Fault effect| Epoch N+1 start| Read ExtraData            | Faulty excluded       |

| Phase        | When                 | Action              | Effect                |
|-------------|----------------------|--------------------|------------------------|
| Execute     | Epoch N (non-end)    | effectiveEpoch=N   | Scheduled             |
| Apply       | Epoch N end          | Clear fault        | Flag cleared          |
| Set calc    | Epoch N end          | Include recovered  | New set in ExtraData  |
| Recovery    | Epoch N+1 start      | Read ExtraData     | Node produces again   |

(If executed at epoch end block, effectiveEpoch=N+1; apply at N+1 end; recovery at N+2 start.)

---

## **XII (alt). System Features and Design**

### **12.1 VCity Chain Features**

- **BLS aggregate signatures**: Multiple validator signatures aggregated into one (e.g. 64 bytes); saves bandwidth, faster verification, scale-invariant signature size; implemented in consensus/dpos (Aggregate, Verify, BlsKey).
- **Governance voting threshold**: Min stake required to vote on proposals; improves quality and reduces spam/malicious voting.
- **Fixed epoch duration**: Configurable (e.g. 24h); reward and state updates on fixed windows; consistent boundaries.
- **Fault detection and recovery**: BlockProductionTracker; auto removal and slash; recovery only via passed governance proposal.

### **12.2 Design and Optimization Ideas**

- Validator count: consider min/max to avoid frequent changes. Block finality: consider a fixed “stable block” confirmation count and API. Round vs Epoch: clarify or unify. Fork handling: optional “stable block” concept and API. Validator registration: clarify approval flow and authority.

### **12.3 Highlights**

- BLS aggregation; delayed state updates at epoch boundary; fixed-window rewards; fault detection and governance recovery; flexible governance; governance voting threshold.

---

## **XIII. Summary and Best Practices**

### **13.1 Highlights**

- BLS aggregation; epoch-boundary state updates; fixed-window reward calculation; fault detection and recovery; governance with proposal vote and execution; governance voting threshold.

### **13.2 Recommendations**

- Voters: diversify votes to reduce risk. Validators: keep nodes stable. Proposers: justify necessity and impact.

### **13.3 Notes**

- Votes are locked for a period. Executed proposals take effect at epoch boundary, not immediately. Recovery requires a passed governance proposal; no automatic recovery.

---

## **Appendix**

### **A. Configuration Parameters Overview**

- All config parameters, defaults, and value ranges (see Sections X and the YAML examples above).
