import { ethers } from 'ethers';

/**
 * 测试 2：单账户 nonce 链（0 → 1 → 2）
 *
 * 用法：node cmd/test2_nonce_chain.js
 */
const config = {
    rpcUrl: 'http://127.0.0.1:8545',
    chainId: 20230826,
    gasPrice: '1000000000',
    gasLimit: 80000,
    privateKey: '9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756',
    fromAddress: '0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe',
    amount: ethers.utils.parseEther('0.001'),
    recipients: [
        '0x1111111111111111111111111111111111111111',
        '0x2222222222222222222222222222222222222222',
        '0x3333333333333333333333333333333333333333',
    ],
};

const provider = new ethers.providers.JsonRpcProvider(config.rpcUrl);
const wallet = new ethers.Wallet(config.privateKey, provider);

async function sendTransfer(toAddress, amount, nonce) {
    console.log(`\n📤 nonce=${nonce} → ${toAddress} (${ethers.utils.formatEther(amount)} ETH)`);
    const tx = await wallet.sendTransaction({
        to: toAddress,
        value: amount,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce,
        chainId: config.chainId,
    });
    console.log(`   已广播: ${tx.hash}`);
    return tx;
}

async function main() {
    console.log('🚀 测试2：单账户 nonce 链 (0/1/2)');
    console.log('==================================================');

    const startNonce = await provider.getTransactionCount(config.fromAddress, 'pending');
    console.log(`发送方: ${config.fromAddress}`);
    console.log(`起始 nonce: ${startNonce}`);
    console.log(
        `起始余额: ${ethers.utils.formatEther(await provider.getBalance(config.fromAddress))} ETH`
    );

    const pending = [];
    for (let i = 0; i < config.recipients.length; i++) {
        pending.push(sendTransfer(config.recipients[i], config.amount, startNonce + i));
    }
    const txs = await Promise.all(pending);

    console.log('\n⏳ 等待 3 笔交易全部确认...');
    const receipts = await Promise.all(txs.map((tx) => tx.wait()));

    console.log('\n==================================================');
    console.log('📊 结果汇总');
    console.log('==================================================');

    let successCount = 0;
    receipts.forEach((receipt, i) => {
        const ok = receipt.status === 1;
        if (ok) successCount++;
        console.log(
            `${i + 1}. nonce=${startNonce + i}: ${ok ? '✅ success' : '❌ failed'}`
        );
        console.log(`   hash: ${receipt.hash}, block: ${receipt.blockNumber}`);
    });

    const finalNonce = await provider.getTransactionCount(config.fromAddress, 'latest');
    const expectedNonce = startNonce + config.recipients.length;

    console.log(`\n成功: ${successCount}/${receipts.length}`);
    console.log(`最终 nonce: ${finalNonce} (期望 ${expectedNonce})`);

    if (successCount === receipts.length && finalNonce === expectedNonce) {
        console.log('✅ 单账户 nonce 链测试通过');
    } else {
        console.log('❌ 单账户 nonce 链测试未通过，请检查 receipt / nonce');
        process.exit(1);
    }
}

main().catch((err) => {
    console.error('❌ 测试失败:', err.message);
    process.exit(1);
});
