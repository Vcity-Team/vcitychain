import { ethers } from 'ethers';

/**
 * 测试 1：3+ 账户同时转账（并行 Fill / 多账户同块）
 *
 * 用法：node cmd/test1_parallel_multi_account.js
 *
 * 请配置 3 个 genesis 预充值账户的私钥与地址。
 */
const config = {
    rpcUrl: 'http://127.0.0.1:8545',
    chainId: 20230826,
    gasPrice: '1000000000',
    gasLimit: 80000,
    amount: ethers.utils.parseEther('0.001'),
    senders: [
        {
            privateKey: '9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756',
            address: '0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe',
        },
        {
            privateKey: 'cb9ba060bbc7fc5906b5b8251afadf6c74af1018d7872d90c9faa8630c2b3ffa',
            address: '0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C',
        },
        {
            privateKey: '929ad2305f07245fed3070fd79fc6343f9396c516c7c347482fca5bcc14db522',
            address: '0x7744E828e4Bd34aAfBB409C3b574B3647198EE65',
        },
    ],
    recipients: [
        '0x1111111111111111111111111111111111111111',
        '0x2222222222222222222222222222222222222222',
        '0x3333333333333333333333333333333333333333',
    ],
};

const provider = new ethers.providers.JsonRpcProvider(config.rpcUrl);

async function getBalance(address) {
    const balance = await provider.getBalance(address);
    return ethers.utils.formatEther(balance);
}

async function sendTransfer(wallet, toAddress, amount, nonce) {
    return wallet.sendTransaction({
        to: toAddress,
        value: amount,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce,
        chainId: config.chainId,
    });
}

async function main() {
    console.log('🚀 测试1：3 账户同时转账（并行打包）');
    console.log('==================================================');

    const beforeBlock = await provider.getBlockNumber();

    const tasks = config.senders.map(async (sender, i) => {
        const wallet = new ethers.Wallet(sender.privateKey, provider);
        const to = config.recipients[i];
        const nonce = await provider.getTransactionCount(sender.address, 'pending');

        console.log(`\n📤 [账户${i + 1}] ${sender.address}`);
        console.log(`   → ${to}, nonce=${nonce}, amount=${ethers.utils.formatEther(config.amount)} ETH`);

        const tx = await sendTransfer(wallet, to, config.amount, nonce);
        console.log(`   已广播: ${tx.hash}`);
        return tx.wait();
    });

    console.log('\n⏳ 并发广播 3 笔交易，等待确认...');
    const receipts = await Promise.all(tasks);

    const afterBlock = await provider.getBlockNumber();
    const blockNumbers = [...new Set(receipts.map((r) => r.blockNumber))];

    console.log('\n==================================================');
    console.log('📊 结果汇总');
    console.log('==================================================');

    let successCount = 0;
    receipts.forEach((receipt, i) => {
        const ok = receipt.status === 1;
        if (ok) successCount++;
        console.log(
            `${i + 1}. ${config.senders[i].address}: ${ok ? '✅ success' : '❌ failed'}`
        );
        console.log(`   hash: ${receipt.hash}`);
        console.log(`   block: ${receipt.blockNumber}, gasUsed: ${receipt.gasUsed.toString()}`);
    });

    console.log(`\n成功: ${successCount}/${receipts.length}`);
    console.log(`出块范围: ${beforeBlock} → ${afterBlock}`);
    console.log(`交易所在区块: ${blockNumbers.join(', ')}`);
    if (blockNumbers.length === 1) {
        console.log('✅ 3 笔交易在同一区块（并行 Fill 理想情况）');
    } else {
        console.log('ℹ️  交易分布在多个区块（仍可能正常，取决于出块速度与 txpool）');
    }

    console.log('\n🔍 发送方 nonce / 余额:');
    for (const sender of config.senders) {
        const nonce = await provider.getTransactionCount(sender.address, 'latest');
        const balance = await getBalance(sender.address);
        console.log(`   ${sender.address}: nonce=${nonce}, balance=${balance} ETH`);
    }
}

main().catch((err) => {
    console.error('❌ 测试失败:', err.message);
    process.exit(1);
});
