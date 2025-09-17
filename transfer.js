const { ethers } = require('ethers');

// 配置信息
const config = {
    // RPC 端点
    rpcUrl: 'http://localhost:9545',
    
    // 源账户私钥
    privateKey: '9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756',
    
    // 源账户地址
    fromAddress: '0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe',
    
    // 验证者地址列表
    validators: [
        '0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C',
        '0x7744E828e4Bd34aAfBB409C3b574B3647198EE65',
        '0xa5Ce949C933e06E8395194AE147283e091EcF3Cb',
        '0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9'
    ],
    
    // 每个验证者转账金额 (1000 ETH in wei)
    amountPerValidator: ethers.parseEther('1000'),
    
    // 链ID (根据项目配置，通常是 888)
    chainId: 888,
    
    // Gas 价格 (1 Gwei = 1000000000 wei)
    gasPrice: '1000000000',
    
    // Gas 限制
    gasLimit: 21000
};

// 创建 provider 和 wallet
const provider = new ethers.JsonRpcProvider(config.rpcUrl);
const wallet = new ethers.Wallet(config.privateKey, provider);

// 发送单个转账交易
async function sendTransfer(toAddress, amount, nonce) {
    try {
        console.log(`\n正在向 ${toAddress} 转账 ${ethers.formatEther(amount)} ETH...`);
        
    // 创建交易对象 (使用 Legacy 交易类型)
    const transaction = {
        to: toAddress,
        value: amount,
        gasLimit: config.gasLimit,
        gasPrice: config.gasPrice,
        nonce: nonce,
        chainId: config.chainId,
        type: 0  // 明确指定为 Legacy 交易类型
    };
        
        // 签名交易
        const signedTx = await wallet.signTransaction(transaction);
        console.log(`交易已签名: ${signedTx}`);
        
        // 发送原始交易
        const txResponse = await provider.broadcastTransaction(signedTx);
        console.log(`交易已发送，哈希: ${txResponse.hash}`);
        
        // 等待交易确认
        console.log('等待交易确认...');
        const receipt = await txResponse.wait();
        
        if (receipt.status === 1) {
            console.log(`✅ 转账成功! 交易哈希: ${receipt.hash}`);
            console.log(`   区块号: ${receipt.blockNumber}`);
            console.log(`   Gas 使用: ${receipt.gasUsed}`);
            return { success: true, hash: receipt.hash, blockNumber: receipt.blockNumber };
        } else {
            console.log(`❌ 转账失败! 交易哈希: ${receipt.hash}`);
            return { success: false, hash: receipt.hash, error: 'Transaction failed' };
        }
        
    } catch (error) {
        console.error(`❌ 转账到 ${toAddress} 时出错:`, error.message);
        return { success: false, error: error.message };
    }
}

// 获取账户余额
async function getBalance(address) {
    try {
        const balance = await provider.getBalance(address);
        return ethers.formatEther(balance);
    } catch (error) {
        console.error(`获取地址 ${address} 余额时出错:`, error.message);
        return '0';
    }
}

// 获取账户 nonce
async function getNonce(address) {
    try {
        return await provider.getTransactionCount(address, 'pending');
    } catch (error) {
        console.error(`获取地址 ${address} nonce 时出错:`, error.message);
        return 0;
    }
}

// 主函数
async function main() {
    console.log('🚀 开始批量转账到验证者...');
    console.log('=' .repeat(50));
    
    // 检查源账户余额
    const sourceBalance = await getBalance(config.fromAddress);
    console.log(`源账户余额: ${sourceBalance} ETH`);
    
    const totalAmount = config.amountPerValidator * BigInt(config.validators.length);
    console.log(`总转账金额: ${ethers.formatEther(totalAmount)} ETH`);
    
    if (parseFloat(sourceBalance) < parseFloat(ethers.formatEther(totalAmount))) {
        console.error('❌ 源账户余额不足!');
        return;
    }
    
    // 获取初始 nonce
    let currentNonce = await getNonce(config.fromAddress);
    console.log(`初始 nonce: ${currentNonce}`);
    
    const results = [];
    
    // 逐个发送转账交易
    for (let i = 0; i < config.validators.length; i++) {
        const validatorAddress = config.validators[i];
        console.log(`\n📤 处理验证者 ${i + 1}/${config.validators.length}: ${validatorAddress}`);
        
        const result = await sendTransfer(validatorAddress, config.amountPerValidator, currentNonce);
        results.push({
            validator: validatorAddress,
            ...result
        });
        
        // 增加 nonce 用于下一个交易
        currentNonce++;
        
        // 等待一段时间再发送下一个交易，避免 nonce 冲突
        if (i < config.validators.length - 1) {
            console.log('等待 2 秒后发送下一个交易...');
            await new Promise(resolve => setTimeout(resolve, 2000));
        }
    }
    
    // 显示结果汇总
    console.log('\n' + '=' .repeat(50));
    console.log('📊 转账结果汇总:');
    console.log('=' .repeat(50));
    
    let successCount = 0;
    let failCount = 0;
    
    results.forEach((result, index) => {
        const status = result.success ? '✅ 成功' : '❌ 失败';
        console.log(`${index + 1}. ${result.validator}: ${status}`);
        if (result.success) {
            console.log(`   交易哈希: ${result.hash}`);
            console.log(`   区块号: ${result.blockNumber}`);
        } else {
            console.log(`   错误: ${result.error}`);
        }
        console.log('');
        
        if (result.success) successCount++;
        else failCount++;
    });
    
    console.log(`总计: ${successCount} 成功, ${failCount} 失败`);
    
    // 检查验证者余额
    console.log('\n🔍 验证者当前余额:');
    console.log('-' .repeat(30));
    for (const validator of config.validators) {
        const balance = await getBalance(validator);
        console.log(`${validator}: ${balance} ETH`);
    }
}

// 错误处理
process.on('unhandledRejection', (error) => {
    console.error('未处理的错误:', error);
    process.exit(1);
});

// 运行主函数
if (require.main === module) {
    main().catch(console.error);
}

module.exports = {
    sendTransfer,
    getBalance,
    getNonce,
    config
};
