import { ethers } from 'ethers';

/**
 * 测试 4：A→B→C 尽量同块（DAG 转账链依赖）
 *
 * 前提：A、B 都有足够余额，两笔 tx 几乎同时进 txpool，赶上同一次 Fill。
 *
 * 用法：
 *   node cmd/test4_dag_same_block_abc.js
 *   node cmd/test4_dag_same_block_abc.js 5        # 连续尝试 5 轮，提高同块概率
 *
 * 说明：
 * - B 必须有钱，B→C 才能和 A→B 同时入池（B=0 时只能分块，见 test3）。
 * - 同块仍取决于出块节奏与网络延迟，脚本可多次重试。
 * - 节点 debug 日志可搜：fillWithDAG、DAG构建成功、maxLevel
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
    amountAB: ethers.utils.parseEther('0.001'),
    amountBC: ethers.utils.parseEther('0.0005'),
};

const provider = new ethers.providers.JsonRpcProvider(config.rpcUrl);
const walletA = new ethers.Wallet(config.accountA.privateKey, provider);
const walletB = new ethers.Wallet(config.accountB.privateKey, provider);

const gasCost = ethers.BigNumber.from(config.gasPrice).mul(config.gasLimit);
const minBalanceB = config.amountBC.add(gasCost);

function txHash(receiptOrTx) {
    return receiptOrTx.transactionHash || receiptOrTx.hash;
}

async function getBalance(label, address) {
    const balance = await provider.getBalance(address);
    console.log(`   ${label} ${address}: ${ethers.utils.formatEther(balance)} ETH`);
    return balance;
}

async function broadcastPair() {
    const [nonceA, nonceB] = await Promise.all([
        provider.getTransactionCount(config.accountA.address, 'pending'),
        provider.getTransactionCount(config.accountB.address, 'pending'),
    ]);

    console.log(`\n📡 并发广播 (A nonce=${nonceA}, B nonce=${nonceB})`);

    const sendAB = walletA.sendTransaction({
        to: config.accountB.address,
        value: config.amountAB,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce: nonceA,
        chainId: config.chainId,
    });

    const sendBC = walletB.sendTransaction({
        to: config.accountC.address,
        value: config.amountBC,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce: nonceB,
        chainId: config.chainId,
    });

    const [txAB, txBC] = await Promise.all([sendAB, sendBC]);
    console.log(`   A→B: ${txHash(txAB)}`);
    console.log(`   B→C: ${txHash(txBC)}`);

    const [receiptAB, receiptBC] = await Promise.all([txAB.wait(), txBC.wait()]);
    return { txAB, txBC, receiptAB, receiptBC };
}

async function inspectBlock(blockNumber, hashAB, hashBC) {
    const block = await provider.getBlockWithTransactions(blockNumber);
    if (!block) {
        return;
    }
    const hashes = block.transactions.map((t) => t.hash);
    const hasAB = hashes.includes(hashAB);
    const hasBC = hashes.includes(hashBC);
    console.log(`\n🔎 块 ${blockNumber} 共 ${block.transactions.length} 笔 tx`);
    console.log(`   含 A→B: ${hasAB ? '是' : '否'}, 含 B→C: ${hasBC ? '是' : '否'}`);
    if (hasAB && hasBC) {
        const idxAB = hashes.indexOf(hashAB);
        const idxBC = hashes.indexOf(hashBC);
        console.log(`   块内顺序: A→B 索引=${idxAB}, B→C 索引=${idxBC}`);
        console.log(
            '   ℹ️  块内 tx 数组顺序 ≠ 执行顺序（DAG 按 level 执行，写入区块顺序可能不同）'
        );
        console.log('   两笔都 success 即说明状态执行正确；执行顺序看节点 Info 日志');
    }
}

async function runOnce(round, total) {
    console.log(`\n${'='.repeat(50)}`);
    console.log(`🔄 第 ${round}/${total} 轮`);
    console.log('='.repeat(50));

    const beforeBlock = await provider.getBlockNumber();
    const { receiptAB, receiptBC } = await broadcastPair();

    const okAB = receiptAB.status === 1;
    const okBC = receiptBC.status === 1;
    const sameBlock = receiptAB.blockNumber === receiptBC.blockNumber;

    console.log('\n📊 本轮结果');
    console.log(`   A→B: ${okAB ? '✅' : '❌'} block=${receiptAB.blockNumber} hash=${txHash(receiptAB)}`);
    console.log(`   B→C: ${okBC ? '✅' : '❌'} block=${receiptBC.blockNumber} hash=${txHash(receiptBC)}`);
    console.log(`   出块: ${beforeBlock} → ${await provider.getBlockNumber()}`);

    if (sameBlock && okAB && okBC) {
        console.log(`\n🎉 同块成功! block=${receiptAB.blockNumber}`);
        await inspectBlock(receiptAB.blockNumber, txHash(receiptAB), txHash(receiptBC));
        return true;
    }

    console.log('\nℹ️  未同块（可能 Fill 时池里只有一笔，或第二笔晚于封块）');
    if (okAB) {
        await inspectBlock(receiptAB.blockNumber, txHash(receiptAB), txHash(receiptBC));
    }
    if (okBC && receiptBC.blockNumber !== receiptAB.blockNumber) {
        await inspectBlock(receiptBC.blockNumber, txHash(receiptAB), txHash(receiptBC));
    }
    return false;
}

async function main() {
    const rounds = Math.max(1, parseInt(process.argv[2] || '1', 10));

    console.log('🚀 测试4：A→B→C 尽量同块（DAG 转账链）');
    console.log('==================================================');
    console.log(`RPC: ${config.rpcUrl}`);
    console.log(`尝试轮数: ${rounds}`);

    console.log('\n🔍 初始余额:');
    await getBalance('A', config.accountA.address);
    const balanceB = await getBalance('B', config.accountB.address);
    await getBalance('C', config.accountC.address);

    if (balanceB.lt(minBalanceB)) {
        console.error(
            `\n❌ B 余额不足，需至少 ${ethers.utils.formatEther(minBalanceB)} ETH 才能发 B→C`
        );
        process.exit(1);
    }

    console.log('\n💡 出块 validator 日志（默认 INFO 即可）搜:');
    console.log('   fillWithDAG 开始 / 收集候选 / DAG构建成功 / DAG执行完成');
    console.log('   或 账户分组并行执行完成（无依赖时）');
    console.log('   或 DAGExecutor 开始执行 / 执行层级 / DAG 执行完成');

    let successRounds = 0;
    for (let i = 1; i <= rounds; i++) {
        if (await runOnce(i, rounds)) {
            successRounds++;
        }
        if (i < rounds) {
            await new Promise((r) => setTimeout(r, 500));
        }
    }

    console.log('\n==================================================');
    console.log(`📈 同块成功 ${successRounds}/${rounds} 轮`);
    if (successRounds === 0) {
        console.log('建议: node cmd/test4_dag_same_block_abc.js 10  多试几轮');
        console.log('或检查出块间隔是否过短、tx 传播是否过慢');
        process.exit(1);
    }
    console.log('✅ 至少一轮同块 A→B→C，可结合节点 DAG 日志确认 Fill 路径');
}

main().catch((err) => {
    console.error('❌ 测试失败:', err.message);
    process.exit(1);
});
