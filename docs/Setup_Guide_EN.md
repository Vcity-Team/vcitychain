# DPoS Testnet Validator Node Setup Guide

## Recommended Hardware

## Setup Steps

### 1. Node Data Directory and Configuration

Download the executable **vcitychain** (choose the build for your OS), **config**, and **data** from [https://github.com/Vcity-Team/vcitychain/releases/tag/v2.0.0](https://github.com/Vcity-Team/vcitychain/releases/tag/v2.0.0). (Data files may be provided separately—see the release notes.)

1. Create a node run directory, copy the downloaded executable into it, then run:

```bash
./vcitychain polybft-secrets init --data-dir ./node1 --insecure
```

This creates a data directory for the node’s blockchain data.

2. Place the **genesis file** and **shared config** in a directory at the same level as `node1`. Put the **node-specific config** inside `node1`. If you run only one node on the machine, the node config can usually be left as-is; otherwise adjust ports as needed.

3. Place the **blockchain data** under the `node1` directory.

### 2. Start the Node

```bash
./vcitychain server --config ./node1/node-config-validator.yaml --consensus-switch-height=3763800
```

The node will sync blocks until it reaches the testnet height. If you have already downloaded the chain data, sync time is much shorter; remaining time depends on current mainnet height.

### 3. Voting (Become a Validator Candidate)

#### 3.1 Get test tokens (VCITY)

Get test tokens from the official testnet faucet (e.g. 10 VCITY).

#### 3.2 Register as a candidate

You need at least **10 VCITY** to register. Two options:

1. **Official DPoS console**: Connect with MetaMask, go to the candidate page, fill in **name**, **website**, and **description** (optional but recommended for visibility), then click “Register candidate”.

2. **CLI** (private key must **not** include the `0x` prefix):

```bash
./vcitychain dpos delegate register --jsonrpc https://testnet-rpc.vcity.app --address xxx --name xxx --website xxx --description xxx --private-key xxx --chain-id 20230826
```

**Note:** If you register as a candidate but do **not** run a physical node, you can still receive votes. However, when your rank enters the top 21, your validator set update takes effect in the **next epoch** (1 hour). For that entire epoch you will have no blocks produced, so at epoch end you will be marked as faulty and removed. To produce blocks again you will need to pass a recovery proposal.

#### 3.3 Campaign for votes

On the official DPoS page you can see your current vote rank among candidates. Campaign in the community so more users vote for you. When you enter the top 21, your weight will be updated in the next epoch and you will start producing blocks.

### 4. Producing blocks

Once in the active set, the node will produce blocks normally and earn rewards.

**Disclaimer:** Test tokens used on the testnet do not represent any mainnet rights and will not be converted to mainnet tokens.
