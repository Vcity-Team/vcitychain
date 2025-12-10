import ethers from 'ethers';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { dirname } from 'path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// 配置参数
const ADDRESSES = [
    "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe",
    '0x373680F58bA9A10E48199f6255eDD89757304128',
    '0x9f024fa32D63547a8810314E114D4FEAD1e60854',
    '0x3980E889849eb47f51Da1a90527726e25Ed85266',
    '0x1B5583D6c8a8C970a2b91f2B71D5c27BCA954122',
    '0x607C48332bdb69AdE0833678214A867D7a7392Ef',
    '0x6fF9038d8BcA8a3680B1FF81e4d6c0d76e975311',
    '0x32cb7D2e9931C692BB8399cfA692150F4dD7d3CE',
    '0xBCc9A6a75891eFAd1cc4e6498E4059D68eF85098'
];
const RPC_URL = process.env.RPC_URL || "http://localhost:8545";
const INTERVAL = parseInt(process.env.INTERVAL) || 2; // 秒
const CLEAR_SCREEN = process.env.CLEAR_SCREEN === 'true';
const CHART_WIDTH = parseInt(process.env.CHART_WIDTH) || 40;
const MONITOR_TXPOOL = process.env.MONITOR_TXPOOL !== 'false'; // 默认启用

// 全局状态
const accounts = new Map();
const startTime = Date.now();
let iteration = 0;
let totalTransactions = 0;
let totalTpsHistory = [];
let consecutiveFailures = 0;
let totalFailures = 0;
let lastSuccessTime = Date.now();
let lastNonces = new Map();
let lastTxPoolAnalysis = null;
const maxTpsHistory = 100;

// 初始化账户跟踪
ADDRESSES.forEach(addr => {
    accounts.set(addr, {
        address: addr,
        previousNonce: null,
        previousTime: null,
        initialNonce: null,
        totalTransactions: 0,
        tpsHistory: [],
        currentTps: 0.0
    });
});

const provider = new ethers.providers.JsonRpcProvider(RPC_URL);

// 工具函数
function formatNumber(num) {
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

function formatNumberBigInt(bi) {
    return bi.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

function clearScreen() {
    if (CLEAR_SCREEN) {
        process.stdout.write('\x1B[2J\x1B[0f');
    }
}

// 绘制TPS图表
function drawTpsChart(tpsValues, width = 40, title = "TPS Chart", returnText = false) {
    if (tpsValues.length === 0) {
        return "";
    }

    const maxTps = Math.max(...tpsValues);
    const minTps = Math.min(...tpsValues);
    const adjustedMax = maxTps === minTps ? minTps + 1 : maxTps;
    const height = 10;
    const chartLines = [];

    chartLines.push("");
    chartLines.push(`${title} (Last ${tpsValues.length} intervals):`);
    chartLines.push(`Max: ${maxTps.toFixed(2)} TPS | Min: ${minTps.toFixed(2)} TPS`);
    chartLines.push("-".repeat(width + 10));

    // 从顶部到底部绘制
    for (let i = height; i >= 0; i--) {
        const threshold = minTps + (adjustedMax - minTps) * (i / height);
        let line = threshold.toFixed(2).padStart(6) + " |";

        tpsValues.forEach(tps => {
            line += tps >= threshold ? "#" : " ";
        });

        chartLines.push(line);
    }

    // 绘制底部轴
    chartLines.push("-".repeat(width + 10));
    let axisLine = "       ";
    for (let i = 0; i < Math.min(tpsValues.length, width); i++) {
        axisLine += (i % 5 === 0) ? "|" : " ";
    }
    chartLines.push(axisLine);

    // 显示最新的TPS值
    const latestValues = tpsValues.slice(-10);
    if (latestValues.length > 0) {
        let valuesLine = "Latest: ";
        latestValues.forEach(v => {
            valuesLine += v.toFixed(2) + " ";
        });
        chartLines.push(valuesLine);
    }

    if (returnText) {
        return chartLines.join("\n");
    }

    // 显示到控制台
    console.log("");
    console.log(`\x1b[36m${title} (Last ${tpsValues.length} intervals):\x1b[0m`);
    console.log(`\x1b[90mMax: ${maxTps.toFixed(2)} TPS | Min: ${minTps.toFixed(2)} TPS\x1b[0m`);
    console.log(`\x1b[90m${"-".repeat(width + 10)}\x1b[0m`);

    for (let i = 3; i < chartLines.length - 1; i++) {
        console.log(`\x1b[90m${chartLines[i]}\x1b[0m`);
    }

    if (chartLines.length > 3) {
        console.log(`\x1b[33m${chartLines[chartLines.length - 1]}\x1b[0m`);
    }

    return "";
}

// 保存图表到文件
function saveTpsChart(tpsValues, width = 40, title = "TPS Chart") {
    if (tpsValues.length === 0) {
        return;
    }

    const chartText = drawTpsChart(tpsValues, width, title, true);

    if (chartText) {
        const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, -5);
        const fileName = `tps_chart_${timestamp}.txt`;
        const filePath = path.join(process.cwd(), fileName);

        try {
            fs.writeFileSync(filePath, chartText, 'utf8');
            console.log(`\x1b[32mChart saved to: ${filePath}\x1b[0m`);
        } catch (error) {
            console.error(`\x1b[31mFailed to save chart: ${error.message}\x1b[0m`);
        }
    }
}

// 获取交易池状态
async function getTxPoolStatus() {
    try {
        const status = await provider.send('txpool_status', []);
        const inspect = await provider.send('txpool_inspect', []);
        const content = await provider.send('txpool_content', []);

        return { status, inspect, content };
    } catch (error) {
        throw new Error(`查询交易池失败: ${error.message}`);
    }
}

// 分析交易池数据
function analyzeTxPool(data) {
    if (!data) {
        return null;
    }

    const { inspect, content } = data;

    const toBigIntSafe = (v) => {
        if (typeof v === 'bigint') return v;
        if (typeof v === 'number') return BigInt(v);
        if (typeof v === 'string') {
            const s = v.trim();
            if (s.startsWith('0x') || s.startsWith('0X')) return BigInt(s);
            return BigInt(s);
        }
        return 0n;
    };

    const currentCapacityBI = toBigIntSafe(inspect.currentCapacity ?? inspect.current_capacity ?? 0);
    const maxCapacityBI = toBigIntSafe(inspect.maxCapacity ?? inspect.max_capacity ?? 0);

    const usedBI = currentCapacityBI < 0n ? 0n : currentCapacityBI;
    const cappedMaxBI = maxCapacityBI < 0n ? 0n : maxCapacityBI;
    const effectiveUsedBI = cappedMaxBI > 0n ? (usedBI > cappedMaxBI ? cappedMaxBI : usedBI) : usedBI;

    const remainingBI = cappedMaxBI > effectiveUsedBI ? (cappedMaxBI - effectiveUsedBI) : 0n;
    const highPressureThresholdBI = cappedMaxBI * 80n / 100n;
    const isHighPressure = effectiveUsedBI >= highPressureThresholdBI && cappedMaxBI > 0n;

    const slotUsageRate = cappedMaxBI > 0n
        ? Number((effectiveUsedBI * 10000n) / cappedMaxBI) / 100
        : 0;

    // 统计交易
    const accountDetails = new Map();
    let totalPendingTxs = 0;
    let totalQueuedTxs = 0;

    if (content.pending) {
        Object.keys(content.pending).forEach(addr => {
            const txCount = Object.keys(content.pending[addr]).length;
            totalPendingTxs += txCount;
            accountDetails.set(addr, {
                address: addr,
                pending: txCount,
                queued: 0,
                total: txCount
            });
        });
    }

    if (content.queued) {
        Object.keys(content.queued).forEach(addr => {
            const txCount = Object.keys(content.queued[addr]).length;
            totalQueuedTxs += txCount;
            const existing = accountDetails.get(addr);
            if (existing) {
                existing.queued = txCount;
                existing.total += txCount;
            } else {
                accountDetails.set(addr, {
                    address: addr,
                    pending: 0,
                    queued: txCount,
                    total: txCount
                });
            }
        });
    }

    const totalTxs = totalPendingTxs + totalQueuedTxs;
    const avgSlotsPerTx = totalTxs > 0 ? Number(effectiveUsedBI) / totalTxs : 0;

    return {
        currentCapacity: effectiveUsedBI,
        maxCapacity: cappedMaxBI,
        remaining: remainingBI,
        highPressureThreshold: highPressureThresholdBI,
        isHighPressure,
        slotUsageRate,
        totalPendingTxs,
        totalQueuedTxs,
        totalTxs,
        avgSlotsPerTx,
        accountDetails: Array.from(accountDetails.values()).sort((a, b) => b.total - a.total)
    };
}

// 显示交易池状态
function showTxPoolStatus(analysis) {
    if (!analysis) {
        return;
    }

    console.log("");
    const boxTop = "┌─ 交易池状态 " + "─".repeat(60) + "┐";
    console.log(`\x1b[36m${boxTop}\x1b[0m`);
    const pipeChar = "│";

    console.log(`${pipeChar} 当前使用: ${formatNumberBigInt(analysis.currentCapacity)} slots`);
    console.log(`${pipeChar} 最大容量: ${formatNumberBigInt(analysis.maxCapacity)} slots`);

    const usageRateText = `${analysis.slotUsageRate.toFixed(2)}%`;
    const pressureText = analysis.isHighPressure ? '⚠️  HIGH PRESSURE' : '';
    const color = analysis.isHighPressure ? '\x1b[31m' : '\x1b[37m';
    console.log(`${color}${pipeChar} 使用率: ${usageRateText} ${pressureText}\x1b[0m`);

    console.log(`${pipeChar} HighPressure 阈值: ${formatNumberBigInt(analysis.highPressureThreshold)} slots (80%)`);
    console.log(`${pipeChar} 剩余容量: ${formatNumberBigInt(analysis.remaining)} slots`);

    // 诊断警告
    if (analysis.slotUsageRate > 50 && analysis.totalTxs === 0) {
        console.log(`\x1b[31m${pipeChar} [WARNING] Slot usage rate: ${analysis.slotUsageRate.toFixed(2)}% but transaction count is 0, slots leak bug confirmed!\x1b[0m`);
        console.log(`\x1b[33m${pipeChar}    Issue: Transactions removed but slot counter not decreased, slots not released during account cleanup\x1b[0m`);
        console.log(`\x1b[33m${pipeChar}    Impact: New transactions will be rejected (highPressure state), node restart required to fix\x1b[0m`);
    } else if (analysis.slotUsageRate > 50 && analysis.totalTxs > 0) {
        const expectedSlots = analysis.totalTxs * analysis.avgSlotsPerTx;
        const actualSlots = Number(analysis.currentCapacity);
        if (actualSlots > expectedSlots * 1.5) {
            console.log(`\x1b[33m${pipeChar} ⚠️  警告: 实际slots(${actualSlots})远大于预期(${expectedSlots.toFixed(0)})，可能存在slots计算问题\x1b[0m`);
        }
    }

    console.log(`${pipeChar} Pending 交易: ${analysis.totalPendingTxs} 笔`);
    console.log(`${pipeChar} Queued 交易:  ${analysis.totalQueuedTxs} 笔`);
    console.log(`${pipeChar} 总交易数:     ${analysis.totalTxs} 笔`);
    console.log(`${pipeChar} 平均 slots/交易: ${analysis.avgSlotsPerTx.toFixed(2)} slots`);

    if (analysis.avgSlotsPerTx > 1.5) {
        console.log(`\x1b[33m${pipeChar} ⚠️  警告: 平均 slots/交易 > 1.5，可能存在大交易或计算问题\x1b[0m`);
    }

    if (analysis.accountDetails.length > 0) {
        console.log(`${pipeChar} 总账户数: ${analysis.accountDetails.length}`);
        const topAccounts = analysis.accountDetails.slice(0, 5);
        topAccounts.forEach(acc => {
            const addrShort = acc.address.substring(0, 16) + "...";
            const addrPadded = addrShort.padEnd(20);
            const pendingStr = acc.pending.toString().padStart(6);
            const queuedStr = acc.queued.toString().padStart(6);
            const totalStr = acc.total.toString().padStart(6);
            console.log(`\x1b[90m${pipeChar}   ${addrPadded} Pending: ${pendingStr} Queued: ${queuedStr} Total: ${totalStr}\x1b[0m`);
        });
        if (analysis.accountDetails.length > 5) {
            console.log(`\x1b[90m${pipeChar}   ... 还有 ${analysis.accountDetails.length - 5} 个账户\x1b[0m`);
        }
    }

    const boxBottom = "└" + "─".repeat(60) + "┘";
    console.log(`\x1b[36m${boxBottom}\x1b[0m`);
}

// 显示统计信息
function showStatistics() {
    const endTime = Date.now();
    const duration = endTime - startTime;
    const totalSeconds = duration / 1000;

    console.log("");
    console.log("\x1b[32m===============================================\x1b[0m");
    console.log("\x1b[32m           MONITORING STATISTICS\x1b[0m");
    console.log("\x1b[32m===============================================\x1b[0m");

    const dateFormat = "yyyy-MM-dd HH:mm:ss";
    const startTimeStr = new Date(startTime).toLocaleString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false
    });
    const endTimeStr = new Date(endTime).toLocaleString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false
    });

    console.log(`Start Time: ${startTimeStr}`);
    console.log(`End Time:   ${endTimeStr}`);

    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = Math.floor(totalSeconds % 60);
    const durationStr = `${hours.toString().padStart(2, '0')}:${minutes.toString().padStart(2, '0')}:${seconds.toString().padStart(2, '0')}`;
    const durationText = `${durationStr} (${totalSeconds.toFixed(2)} seconds)`;
    console.log(`\x1b[36mDuration:   ${durationText}\x1b[0m`);
    console.log("");

    console.log("\x1b[36m--- Per Account Statistics ---\x1b[0m");

    let totalTxs = 0;
    accounts.forEach((acc, addr) => {
        if (acc.initialNonce !== null && acc.previousNonce !== null) {
            const accTxs = acc.previousNonce - acc.initialNonce;
            totalTxs += accTxs;
            console.log(`Account: ${addr.substring(0, 16)}...`);
            console.log(`  Initial: ${acc.initialNonce} | Final: ${acc.previousNonce} | Txs: ${accTxs}`);
            if (acc.tpsHistory.length > 0) {
                const avgTps = acc.tpsHistory.reduce((a, b) => a + b, 0) / acc.tpsHistory.length;
                console.log(`\x1b[33m  Avg TPS: ${avgTps.toFixed(4)}\x1b[0m`);
            }
        }
    });

    console.log("");
    console.log("\x1b[36m--- Combined Statistics ---\x1b[0m");
    console.log(`\x1b[32mTotal Transactions: ${totalTxs}\x1b[0m`);

    if (totalSeconds > 0 && totalTxs > 0) {
        const avgTps = totalTxs / totalSeconds;
        console.log(`\x1b[33mAverage TPS: ${avgTps.toFixed(4)}\x1b[0m`);
    }

    if (totalTpsHistory.length > 0) {
        const maxTps = Math.max(...totalTpsHistory);
        const minTps = Math.min(...totalTpsHistory);
        const avgRecentTps = totalTpsHistory.reduce((a, b) => a + b, 0) / totalTpsHistory.length;
        console.log(`\x1b[31mPeak TPS:    ${maxTps.toFixed(4)}\x1b[0m`);
        console.log(`\x1b[90mMin TPS:     ${minTps.toFixed(4)}\x1b[0m`);
        console.log(`\x1b[36mRecent Avg:  ${avgRecentTps.toFixed(4)}\x1b[0m`);
    }

    console.log(`Total Queries: ${iteration}`);
    const failureColor = totalFailures > 0 ? '\x1b[33m' : '\x1b[32m';
    console.log(`${failureColor}Total Failures: ${totalFailures}\x1b[0m`);
    console.log("\x1b[32m===============================================\x1b[0m");

    // 保存图表
    if (totalTpsHistory.length > 0) {
        console.log("");
        console.log("\x1b[36mSaving TPS chart to current directory...\x1b[0m");
        saveTpsChart(totalTpsHistory, CHART_WIDTH, "Combined TPS Chart");
    }
}

// 注册退出处理
process.on('SIGINT', () => {
    showStatistics();
    process.exit(0);
});

// 主循环
async function main() {
    console.log("\x1b[32mStarting multi-account nonce monitor with TPS calculation...\x1b[0m");
    if (MONITOR_TXPOOL) {
        console.log("\x1b[36mAlso monitoring transaction pool status\x1b[0m");
    }
    console.log(`\x1b[36mMonitoring ${ADDRESSES.length} accounts:\x1b[0m`);
    ADDRESSES.forEach(addr => {
        console.log(`\x1b[90m  - ${addr}\x1b[0m`);
    });
    console.log(`\x1b[36mRPC: ${RPC_URL}\x1b[0m`);
    console.log(`\x1b[36mInterval: ${INTERVAL} seconds\x1b[0m`);
    console.log("\x1b[33mPress Ctrl+C to stop\x1b[0m");
    console.log("");

    while (true) {
        try {
            iteration++;
            const currentTime = Date.now();
            const timestamp = new Date(currentTime).toLocaleString('zh-CN', {
                year: 'numeric',
                month: '2-digit',
                day: '2-digit',
                hour: '2-digit',
                minute: '2-digit',
                second: '2-digit',
                hour12: false
            });
            let totalCurrentTps = 0.0;
            const allNonces = [];

            // 查询所有账户
            let successCount = 0;
            let failureCount = 0;

            for (const addr of ADDRESSES) {
                try {
                    const nonce = await provider.getTransactionCount(addr, 'latest');
                    const acc = accounts.get(addr);

                    // 设置初始nonce
                    if (acc.initialNonce === null) {
                        acc.initialNonce = nonce;
                    }

                    let diff = null;
                    let currentTps = 0.0;

                    if (acc.previousNonce !== null) {
                        diff = nonce - acc.previousNonce;
                        if (diff > 0) {
                            acc.totalTransactions += diff;
                            totalTransactions += diff;

                            // 计算TPS
                            if (acc.previousTime !== null) {
                                const timeDiff = (currentTime - acc.previousTime) / 1000;
                                if (timeDiff > 0) {
                                    currentTps = diff / timeDiff;
                                    acc.currentTps = currentTps;
                                    totalCurrentTps += currentTps;

                                    // 添加到账户历史
                                    acc.tpsHistory.push(currentTps);
                                    if (acc.tpsHistory.length > maxTpsHistory) {
                                        acc.tpsHistory = acc.tpsHistory.slice(-maxTpsHistory);
                                    }
                                }
                            }
                        }
                    }

                    acc.previousNonce = nonce;
                    acc.previousTime = currentTime;

                    allNonces.push({
                        address: addr,
                        nonce: nonce,
                        diff: diff,
                        tps: currentTps
                    });

                    successCount++;
                } catch (error) {
                    failureCount++;
                    totalFailures++;
                    console.error(`\x1b[31m[${timestamp}] Query failed for ${addr.substring(0, 16)}...: ${error.message}\x1b[0m`);
                }
            }

            // 更新连续失败计数
            if (failureCount > 0) {
                consecutiveFailures++;
            } else {
                consecutiveFailures = 0;
                lastSuccessTime = currentTime;
            }

            // 显示失败统计（如果有失败）
            if (consecutiveFailures > 0) {
                const timeSinceLastSuccess = (currentTime - lastSuccessTime) / 1000;
                const timeSinceLastSuccessRounded = timeSinceLastSuccess.toFixed(1);
                const successRatio = `${successCount}/${ADDRESSES.length}`;
                const failureMsg = `[${timestamp}] ⚠️ 连续失败 ${consecutiveFailures} 次 (距上次成功: ${timeSinceLastSuccessRounded}秒) | 本次成功: ${successRatio} | 总失败: ${totalFailures}`;
                console.log(`\x1b[33m${failureMsg}\x1b[0m`);
            }

            // 添加到总TPS历史
            if (totalCurrentTps > 0) {
                totalTpsHistory.push(totalCurrentTps);
            }

            // 检测数据变化
            let hasChanges = false;
            const changedAccounts = [];

            allNonces.forEach(nonceInfo => {
                const addr = nonceInfo.address;
                const lastNonce = lastNonces.get(addr) || null;

                // 如果有变化（nonce增加或首次记录）
                if (lastNonce === null || nonceInfo.nonce !== lastNonce) {
                    hasChanges = true;
                    changedAccounts.push(nonceInfo);
                    lastNonces.set(addr, nonceInfo.nonce);
                }
            });

            // 只在有变化或首次运行时显示
            if (hasChanges || iteration === 1) {
                if (CLEAR_SCREEN && iteration > 1) {
                    clearScreen();
                    console.log("\x1b[32mStarting multi-account nonce monitor with TPS calculation...\x1b[0m");
                    console.log(`\x1b[36mMonitoring ${ADDRESSES.length} accounts\x1b[0m`);
                    console.log(`\x1b[36mRPC: ${RPC_URL} | Interval: ${INTERVAL} seconds\x1b[0m`);
                    console.log("\x1b[33mPress Ctrl+C to stop\x1b[0m");
                    console.log("");
                }

                if (!CLEAR_SCREEN && iteration > 1) {
                    console.log("");
                    console.log("\x1b[90m" + "=".repeat(50) + "\x1b[0m");
                }

                console.log(`Time: ${timestamp} | Iteration: ${iteration}`);

                // 只显示有变化的账户
                if (changedAccounts.length > 0) {
                    changedAccounts.forEach(nonceInfo => {
                        const addrShort = nonceInfo.address.substring(0, 16) + "...";

                        process.stdout.write(`${addrShort} : Nonce=${nonceInfo.nonce} `);

                        if (nonceInfo.diff !== null) {
                            if (nonceInfo.diff > 0) {
                                process.stdout.write(`\x1b[32mChange=+${nonceInfo.diff} \x1b[0m`);
                            } else if (nonceInfo.diff < 0) {
                                process.stdout.write(`\x1b[31mChange=${nonceInfo.diff} \x1b[0m`);
                            } else {
                                process.stdout.write(`\x1b[90mChange=0 \x1b[0m`);
                            }
                        } else {
                            process.stdout.write(`\x1b[36mChange=First \x1b[0m`);
                        }

                        if (nonceInfo.tps > 0) {
                            console.log(`\x1b[35mTPS=${nonceInfo.tps.toFixed(2)}\x1b[0m`);
                        } else {
                            console.log("");
                        }
                    });

                    console.log("\x1b[90m" + "-".repeat(50) + "\x1b[0m");

                    // 显示合并统计
                    if (totalCurrentTps > 0) {
                        console.log(`\x1b[35mCombined Current TPS: ${totalCurrentTps.toFixed(4)}\x1b[0m`);
                    }

                    const elapsed = (currentTime - startTime) / 1000;
                    if (elapsed > 0 && totalTransactions > 0) {
                        const avgTps = totalTransactions / elapsed;
                        console.log(`\x1b[36mCombined Avg TPS: ${avgTps.toFixed(4)}\x1b[0m`);
                    }

                    console.log(`\x1b[33mTotal Transactions: ${totalTransactions}\x1b[0m`);
                }
            }

            // 监控交易池（如果启用，只在变化时显示）
            if (MONITOR_TXPOOL) {
                try {
                    const txPoolData = await getTxPoolStatus();
                    const txPoolAnalysis = analyzeTxPool(txPoolData);

                    if (txPoolAnalysis) {
                        // 检测交易池变化
                        let txPoolChanged = false;
                        if (lastTxPoolAnalysis === null) {
                            txPoolChanged = true;
                        } else {
                            // 检查关键指标是否变化
                            if (txPoolAnalysis.totalTxs !== lastTxPoolAnalysis.totalTxs ||
                                txPoolAnalysis.currentCapacity !== lastTxPoolAnalysis.currentCapacity ||
                                txPoolAnalysis.slotUsageRate !== lastTxPoolAnalysis.slotUsageRate) {
                                txPoolChanged = true;
                            }
                        }

                        if (txPoolChanged || iteration === 1) {
                            showTxPoolStatus(txPoolAnalysis);
                            lastTxPoolAnalysis = txPoolAnalysis;
                        }
                    }
                } catch (error) {
                    const errorTime = new Date().toLocaleString('zh-CN', {
                        year: 'numeric',
                        month: '2-digit',
                        day: '2-digit',
                        hour: '2-digit',
                        minute: '2-digit',
                        second: '2-digit',
                        hour12: false
                    });
                    console.error(`\x1b[33m[${errorTime}] TxPool query failed: ${error.message}\x1b[0m`);
                }
            }

            await new Promise(resolve => setTimeout(resolve, INTERVAL * 1000));
        } catch (error) {
            const errorTime = new Date().toLocaleString('zh-CN', {
                year: 'numeric',
                month: '2-digit',
                day: '2-digit',
                hour: '2-digit',
                minute: '2-digit',
                second: '2-digit',
                hour12: false
            });
            console.error(`\x1b[31m[${errorTime}] Error: ${error.message}\x1b[0m`);
            await new Promise(resolve => setTimeout(resolve, INTERVAL * 1000));
        }
    }
}

// 运行
main().catch(error => {
    console.error(`\x1b[31m❌ 程序错误: ${error.message}\x1b[0m`);
    if (error.stack) {
        console.error(error.stack);
    }
    process.exit(1);
});

