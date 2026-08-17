# Transactions Block List with Safe Multisig Admin

This guide covers enabling the on-chain transactions block list (`0x0300…0002`) with a **Safe multisig** as the sole Admin, plus day-2 operations (blacklist / unban).

## Contract

| Item | Value |
|------|--------|
| Address | `0x0300000000000000000000000000000000000002` |
| Runtime | Built-in address list (not Solidity) |
| Roles | `0` = none, `1` = **Enabled** (blacklisted), `2` = **Admin** |

ABI (4byte selectors):

```text
setAdmin(address)
setEnabled(address)   // blacklist
setNone(address)      // unban
readAddressList(address) returns (uint256)
```

Semantics for the transactions block list:

- **EnabledRole** → account cannot send txs; transfers/calls **to** that address are also rejected
- **AdminRole** (Safe) is **not** blocked
- `SystemCaller` and calls **to** the block-list contract itself are exempt (Admin can always `setNone`)

## Bootstrap (new network)

1. Deploy a Safe (official Safe Singleton + Proxy) on the chain; owners = signers, threshold = m-of-n.
2. Generate genesis with Safe as the only admin:

```bash
vcitychain genesis \
  --transactions-block-list-admin 0x<SafeAddress> \
  # ... other genesis flags
```

Or set in `genesis.json`:

```json
"params": {
  "transactionsBlockList": {
    "adminAddresses": ["0x<SafeAddress>"],
    "enabledAddresses": []
  },
  "forks": {
    "transactionsBlockList": { "block": 0 }
  }
}
```

3. Start nodes. Verify Admin role:

```bash
# readAddressList(Safe) → 2
cast call 0x0300000000000000000000000000000000000002 \
  "readAddressList(address)(uint256)" 0x<SafeAddress> \
  --rpc-url http://127.0.0.1:8545
```

## Hardfork (live network)

All validators must upgrade with the **same** genesis params. Prefer activating at a **future** height `H` (H > current tip).

1. Deploy Safe on the live chain; record its address.
2. Edit every node’s `genesis.json`:

```json
"params": {
  "transactionsBlockList": {
    "adminAddresses": ["0x<SafeAddress>"],
    "enabledAddresses": []
  },
  "forks": {
    "transactionsBlockList": { "block": H }
  }
}
```

3. Rolling-restart / upgrade binaries that include From+To filtering and fork injection.
4. At block `H` (or the first block after upgrade if `H` was already passed), nodes write Admin roles into state and start enforcing the list.
5. Confirm `readAddressList(Safe) == 2` after activation.

:::warning

- Set **both** `params.transactionsBlockList` and `forks.transactionsBlockList.block = H` with **H > 0**.
- Do **not** use `block: 0` on an already-running chain: that path is for new genesis only.
- When `H > 0`, the node **does not** rewrite genesis allocs (avoids `genesis file does not match current genesis`). Roles are injected at height `H`, with a deterministic catch-up on the first block `>= H` if the exact fork block was missed.
- Hardfork injection also sets a 1-wei balance on `0x0300…0002` (same as genesis allocs) so EIP-161 empty-account cleanup cannot wipe role storage.
- If you add the params object **without** the fork key, enforcement starts immediately on upgrade and genesis allocs are applied (unsafe on live chains).

:::

## Safe operations

Use Safe Transaction Builder / Safe SDK. Target = `0x0300000000000000000000000000000000000002`.

| Action | Calldata |
|--------|----------|
| Blacklist | `setEnabled(victim)` |
| Unban | `setNone(victim)` |
| Transfer Admin | `setAdmin(newSafe)` then have new Safe manage the list (old Admin cannot remove itself) |
| Query | `readAddressList(addr)` → `0` / `1` / `2` |

### ethers.js sketch

```javascript
const BLOCK_LIST = "0x0300000000000000000000000000000000000002";
const abi = [
  "function setEnabled(address)",
  "function setNone(address)",
  "function setAdmin(address)",
  "function readAddressList(address) view returns (uint256)",
];

// Populate Safe tx: to=BLOCK_LIST, data=contract.interface.encodeFunctionData("setEnabled", [victim])
// Collect m-of-n signatures, then execTransaction on the Safe.
```

### Expected failures

- Blacklisted `from` or `to` → txpool / `eth_sendRawTransaction` returns `account is blacklisted`
- Execution layer also rejects with the same error if a tx somehow reaches block building

## Emergency notes

- Keep Safe threshold and key custody under a separate ops policy (hardware wallets / MPC as Safe owners).
- Prefer a dedicated “emergency” Safe with a lower threshold only if governance accepts that risk model.
- Do **not** call `setEnabled(Safe)` on the Admin itself — that removes Admin privileges.

## Related

- ACL overview: [allowlist-general.md](./allowlist-general.md)
- Design: [allowlist.md](../../../design/runtime/allowlist.md)
