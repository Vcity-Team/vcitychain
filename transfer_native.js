const crypto = require('crypto');
const https = require('https');
const http = require('http');

// 配置信息
const config = {
    // RPC 端点
    rpcUrl: 'http://localhost:9545',
    
    // 源账户私钥 (hex string)
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
    amountPerValidator: '1000000000000000000000', // 1000 ETH in wei
    
    // 链ID
    chainId: 888,
    
    // Gas 价格 (1 Gwei = 1000000000 wei)
    gasPrice: '1000000000',
    
    // Gas 限制
    gasLimit: 21000
};

// 工具函数
function toHex(value, length = 0) {
    const hex = value.toString(16);
    return '0x' + (length > 0 ? hex.padStart(length * 2, '0') : hex);
}

function toBigInt(value) {
    if (typeof value === 'string') {
        return value.startsWith('0x') ? BigInt(value) : BigInt('0x' + value);
    }
    return BigInt(value);
}

function keccak256(data) {
    const hash = crypto.createHash('sha3-256');
    hash.update(data);
    return hash.digest();
}

// RLP 编码函数
function rlpEncode(input) {
    if (typeof input === 'string') {
        input = Buffer.from(input.slice(2), 'hex');
    }
    
    if (Buffer.isBuffer(input)) {
        if (input.length === 1 && input[0] < 0x80) {
            return input;
        } else if (input.length < 56) {
            return Buffer.concat([Buffer.from([0x80 + input.length]), input]);
        } else {
            const lengthBytes = Buffer.alloc(8);
            lengthBytes.writeUIntBE(input.length, 0, 8);
            const lengthHex = lengthBytes.toString('hex').replace(/^0+/, '');
            const lengthPrefix = 0x80 + 0x37 + lengthHex.length / 2;
            return Buffer.concat([Buffer.from([lengthPrefix]), Buffer.from(lengthHex, 'hex'), input]);
        }
    }
    
    if (Array.isArray(input)) {
        const encodedItems = input.map(item => rlpEncode(item));
        const totalLength = encodedItems.reduce((sum, item) => sum + item.length, 0);
        
        if (totalLength < 56) {
            return Buffer.concat([Buffer.from([0xc0 + totalLength]), ...encodedItems]);
        } else {
            const lengthBytes = Buffer.alloc(8);
            lengthBytes.writeUIntBE(totalLength, 0, 8);
            const lengthHex = lengthBytes.toString('hex').replace(/^0+/, '');
            const lengthPrefix = 0xc0 + 0x37 + lengthHex.length / 2;
            return Buffer.concat([Buffer.from([lengthPrefix]), Buffer.from(lengthHex, 'hex'), ...encodedItems]);
        }
    }
    
    throw new Error('Invalid input type for RLP encoding');
}

// 从私钥获取公钥
function getPublicKey(privateKey) {
    const key = crypto.createECDH('secp256k1');
    key.setPrivateKey(Buffer.from(privateKey, 'hex'));
    return key.getPublicKey();
}

// 从公钥获取地址
function getAddress(publicKey) {
    const hash = keccak256(publicKey.slice(1));
    return '0x' + hash.slice(12).toString('hex');
}

// ECDSA 签名
function sign(hash, privateKey) {
    const key = crypto.createECDH('secp256k1');
    key.setPrivateKey(Buffer.from(privateKey, 'hex'));
    
    const signature = crypto.createSign('sha256');
    signature.update(hash);
    const sig = signature.sign(key.getPrivateKey());
    
    const r = sig.slice(0, 32);
    const s = sig.slice(32, 64);
    const v = sig[64];
    
    return { r, s, v };
}

// 计算 EIP-155 V 值
function calculateV(v, chainId) {
    return v + chainId * 2 + 35;
}

// 创建交易哈希
function createTransactionHash(tx, chainId) {
    const items = [
        toBigInt(tx.nonce).toString(16),
        toBigInt(tx.gasPrice).toString(16),
        toBigInt(tx.gasLimit).toString(16),
        tx.to.slice(2),
        toBigInt(tx.value).toString(16),
        tx.data || '0x',
        chainId.toString(16),
        '0',
        '0'
    ];
    
    const rlp = rlpEncode(items.map(item => Buffer.from(item, 'hex')));
    return keccak256(rlp);
}

// 创建签名交易
function createSignedTransaction(tx, privateKey, chainId) {
    const txHash = createTransactionHash(tx, chainId);
    const { r, s, v } = sign(txHash, privateKey);
    const vEIP155 = calculateV(v, chainId);
    
    const signedTx = [
        toBigInt(tx.nonce).toString(16),
        toBigInt(tx.gasPrice).toString(16),
        toBigInt(tx.gasLimit).toString(16),
        tx.to.slice(2),
        toBigInt(tx.value).toString(16),
        tx.data || '0x',
        vEIP155.toString(16),
        r.toString('hex'),
        s.toString('hex')
    ];
    
    const rlp = rlpEncode(signedTx.map(item => Buffer.from(item, 'hex')));
    return '0x' + rlp.toString('hex');
}

// 发送 JSON-RPC 请求
function sendRpcRequest(method, params) {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify({
            jsonrpc: '2.0',
            method: method,
            params: params,
            id: Math.floor(Math.random() * 1000)
        });
        
        const options = {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(data)
            }
        };
        
        const req = http.request(config.rpcUrl.replace('http://', ''), options, (res) => {
            let responseData = '';
            
            res.on('data', (chunk) => {
                responseData += chunk;
            });
            
            res.on('end', () => {
                try {
                    const result = JSON.parse(responseData);
                    if (result.error) {
                        reject(new Error(result.error.message));
                    } else {
                        resolve(result.result);
                    }
                } catch (error) {
                    reject(error);
                }
            });
        });
        
        req.on('error', (error) => {
            reject(error);
        });
        
        req.write(data);
        req.end();
    });
}

// 获取账户余额
async function getBalance(address) {
    try {
        const balance = await sendRpcRequest('eth_getBalance', [address, 'latest']);
        return (parseInt(balance, 16) / Math.pow(10, 18)).toFixed(6);
    } catch (error) {
        console.error(`获取地址 ${address} 余额时出错:`, error.message);
        return '0';
    }
}

// 获取账户 nonce
async function getNonce(address) {
    try {
        const nonce = await sendRpcRequest('eth_getTransactionCount', [address, 'pending']);
        return parseInt(nonce, 16);
    } catch (error) {
        console.error(`获取地址 ${address} nonce 时出错:`, error.message);
        return 0;
    }
}

// 发送原始交易
async function sendRawTransaction(signedTx) {
    try {
        const txHash = await sendRpcRequest('eth_sendRawTransaction', [signedTx]);
        return txHash;
    } catch (error) {
        throw new Error(`发送交易失败: ${error.message}`);
    }
}

// 获取交易收据
async function getTransactionReceipt(txHash) {
    try {
        const receipt = await sendRpcRequest('eth_getTransactionReceipt', [txHash]);
        return receipt;
    } catch (error) {
        console.error(`获取交易收据失败:`, error.message);
        return null;
    }
}

// 发送单个转账交易
async function sendTransfer(toAddress, amount, nonce) {
    try {
        console.log(`\n正在向 ${toAddress} 转账 ${(parseInt(amount) / Math.pow(10, 18)).toFixed(6)} ETH...`);
        
        // 创建交易对象
        const transaction = {
            nonce: toHex(nonce),
            gasPrice: config.gasPrice,
            gasLimit: config.gasLimit,
            to: toAddress,
            value: amount,
            data: '0x'
        };
        
        console.log('交易参数:', JSON.stringify(transaction, null, 2));
        
        // 创建签名交易
        const signedTx = createSignedTransaction(transaction, config.privateKey, config.chainId);
        console.log(`签名交易: ${signedTx}`);
        
        // 发送交易
        const txHash = await sendRawTransaction(signedTx);
        console.log(`交易已发送，哈希: ${txHash}`);
        
        // 等待交易确认
        console.log('等待交易确认...');
        let receipt = null;
        let attempts = 0;
        const maxAttempts = 30; // 最多等待 30 秒
        
        while (!receipt && attempts < maxAttempts) {
            await new Promise(resolve => setTimeout(resolve, 1000));
            receipt = await getTransactionReceipt(txHash);
            attempts++;
        }
        
        if (receipt) {
            if (receipt.status === '0x1') {
                console.log(`✅ 转账成功! 交易哈希: ${receipt.transactionHash}`);
                console.log(`   区块号: ${parseInt(receipt.blockNumber, 16)}`);
                console.log(`   Gas 使用: ${parseInt(receipt.gasUsed, 16)}`);
                return { success: true, hash: receipt.transactionHash, blockNumber: parseInt(receipt.blockNumber, 16) };
            } else {
                console.log(`❌ 转账失败! 交易哈希: ${receipt.transactionHash}`);
                return { success: false, hash: receipt.transactionHash, error: 'Transaction failed' };
            }
        } else {
            console.log(`⏳ 交易超时，但已发送: ${txHash}`);
            return { success: true, hash: txHash, blockNumber: null };
        }
        
    } catch (error) {
        console.error(`❌ 转账到 ${toAddress} 时出错:`, error.message);
        return { success: false, error: error.message };
    }
}

// 主函数
async function main() {
    console.log('🚀 开始批量转账到验证者...');
    console.log('=' .repeat(50));
    
    // 验证私钥和地址
    const publicKey = getPublicKey(config.privateKey);
    const derivedAddress = getAddress(publicKey);
    console.log(`私钥对应的地址: ${derivedAddress}`);
    console.log(`配置的源地址: ${config.fromAddress}`);
    
    if (derivedAddress.toLowerCase() !== config.fromAddress.toLowerCase()) {
        console.error('❌ 私钥与源地址不匹配!');
        return;
    }
    
    // 检查源账户余额
    const sourceBalance = await getBalance(config.fromAddress);
    console.log(`源账户余额: ${sourceBalance} ETH`);
    
    const totalAmount = BigInt(config.amountPerValidator) * BigInt(config.validators.length);
    const totalAmountEth = (Number(totalAmount) / Math.pow(10, 18)).toFixed(6);
    console.log(`总转账金额: ${totalAmountEth} ETH`);
    
    if (parseFloat(sourceBalance) < parseFloat(totalAmountEth)) {
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
            if (result.blockNumber) {
                console.log(`   区块号: ${result.blockNumber}`);
            }
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
