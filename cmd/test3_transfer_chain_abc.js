import { ethers } from 'ethers';

/**
 * 测试 3：A → B → C 链式转账
 *
 * 用法：node cmd/test3_transfer_chain_abc.js
 *
 * 说明：
 * - B 初始余额应为 0（或不足以单独支付 B→C）。
 * - 真实网络上 txpool 入池时会校验当前链上余额，B 在 A→B 未上链前无法广播 B→C。
 * - 因此本脚本分两步：先 A→B 确认，再 B→C（验证业务依赖顺序）。
 * - 「同块 DAG 两层执行」由单元测试 TestBlockBuilder_FillWithDAG_transferChainABC 覆盖。
 */
const config = {
    rpcUrl: 'http://127.0.0.1:8545',
    chainId: 20230826,
    gasPrice: '1000000000',
    gasLimit: 80000,
    accountA: {
        privateKey: '9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756',
        address: '0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe',
    },
    accountB: {
        privateKey: 'b084fff3f6c8c41d300d293390cb6e9222c6a23103377bd4a8a69185c488d1af',
        address: '0x49ea069594dF2cF234E7Fc21786C2fE59F4A8FFD',
    },
    accountC: {
        address: '0x3333333333333333333333333333333333333333',
    },
    amountAB: ethers.utils.parseEther('0.01'),
    amountBC: ethers.utils.parseEther('0.005'),
};

const provider = new ethers.providers.JsonRpcProvider(config.rpcUrl);
const walletA = new ethers.Wallet(config.accountA.privateKey, provider);
const walletB = new ethers.Wallet(config.accountB.privateKey, provider);

function txHash(receiptOrTx) {
    return receiptOrTx.transactionHash || receiptOrTx.hash;
}

async function getBalance(label, address) {
    const balance = await provider.getBalance(address);
    const eth = ethers.utils.formatEther(balance);
    console.log(`   ${label} ${address}: ${eth} ETH`);
    return balance;
}

async function sendTransfer(wallet, fromLabel, toAddress, amount, nonce) {
    console.log(`\n📤 [${fromLabel}] nonce=${nonce} → ${toAddress}`);
    console.log(`   金额: ${ethers.utils.formatEther(amount)} ETH`);
    const tx = await wallet.sendTransaction({
        to: toAddress,
        value: amount,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce,
        chainId: config.chainId,
    });
    console.log(`   已广播: ${txHash(tx)}`);
    return tx;
}

async function main() {
    console.log('🚀 测试3：A → B → C 链式转账');
    console.log('==================================================');

    console.log('\n🔍 初始余额:');
    await getBalance('A', config.accountA.address);
    const balanceBBefore = await getBalance('B', config.accountB.address);
    const balanceCBefore = await getBalance('C', config.accountC.address);

    const gasCost = ethers.BigNumber.from(config.gasPrice).mul(config.gasLimit);
    const minRequired = config.amountBC.add(gasCost);
    if (balanceBBefore.gte(minRequired)) {
        console.log(
            '\n⚠️  警告: B 余额已足够单独发 B→C，建议换未预充值的 B 账户以严格验证链式依赖'
        );
    }

    const nonceA = await provider.getTransactionCount(config.accountA.address, 'pending');

    // 步骤 1：A → B（B 必须先收到钱）
    console.log('\n── 步骤 1/2：A → B，等待确认 ──');
    const txAB = await sendTransfer(walletA, 'A', config.accountB.address, config.amountAB, nonceA);
    const receiptAB = await txAB.wait();
    const okAB = receiptAB.status === 1;

    console.log(`\n   A→B: ${okAB ? '✅ success' : '❌ failed'}`);
    console.log(`   hash: ${txHash(receiptAB)}, block: ${receiptAB.blockNumber}`);

    if (!okAB) {
        console.log('\n❌ A→B 失败，无法继续 B→C');
        process.exit(1);
    }

    const balanceBAfterAB = await provider.getBalance(config.accountB.address);
    console.log(`\n   B 收到 A 转账后余额: ${ethers.utils.formatEther(balanceBAfterAB)} ETH`);
    if (balanceBAfterAB.lt(minRequired)) {
        console.log('\n❌ B 余额仍不足以支付 B→C + gas，请增大 amountAB');
        process.exit(1);
    }

    // 步骤 2：B → C（依赖 A→B 已完成）
    const nonceB = await provider.getTransactionCount(config.accountB.address, 'pending');
    console.log('\n── 步骤 2/2：B → C，等待确认 ──');
    const txBC = await sendTransfer(walletB, 'B', config.accountC.address, config.amountBC, nonceB);
    const receiptBC = await txBC.wait();
    const okBC = receiptBC.status === 1;

    console.log('\n==================================================');
    console.log('📊 结果汇总');
    console.log('==================================================');

    console.log(`A→B: ${okAB ? '✅ success' : '❌ failed'}`);
    console.log(`   hash: ${txHash(receiptAB)}, block: ${receiptAB.blockNumber}`);
    console.log(`B→C: ${okBC ? '✅ success' : '❌ failed'}`);
    console.log(`   hash: ${txHash(receiptBC)}, block: ${receiptBC.blockNumber}`);

    if (receiptAB.blockNumber === receiptBC.blockNumber) {
        console.log(`\n✅ 两笔在同一区块 ${receiptAB.blockNumber}`);
    } else {
        console.log(
            `\nℹ️  A→B 在块 ${receiptAB.blockNumber}，B→C 在块 ${receiptBC.blockNumber}`
        );
        console.log('   （真实网络 txpool 入池校验导致分块，属正常；同块 DAG 见单元测试）');
    }

    console.log('\n🔍 最终余额:');
    await getBalance('A', config.accountA.address);
    await getBalance('B', config.accountB.address);
    await getBalance('C', config.accountC.address);

    const balanceCAfter = await provider.getBalance(config.accountC.address);
    const cReceived = balanceCAfter.sub(balanceCBefore);
    if (okAB && okBC && cReceived.gte(config.amountBC)) {
        console.log('\n✅ A→B→C 链式转账测试通过');
    } else {
        console.log('\n❌ A→B→C 链式转账测试未通过');
        process.exit(1);
    }
}

main().catch((err) => {
    console.error('❌ 测试失败:', err.message);
    process.exit(1);
});
