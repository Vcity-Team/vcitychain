// 从 reorg 分叉块中取出已签名交易，经 eth_sendRawTransaction 重新广播到链上。
// 不需要用户私钥：交易在首次发送时已签名，分叉块体里保存的就是完整 signed RLP。
//
// 用法（在仓库根目录）:
//
//	# 预览：指定 explorer 上带 reorg 标记的分叉块 hash
//	go run scripts/rebroadcast_fork_txs.go \
//	  -data-dir /path/to/node \
//	  -block-hash 0x... \
//	  -rpc https://rpc.vcity.app \
//	  -chain-id 8899 \
//	  -dry-run
//
//	# 只重发一笔（按 tx hash，从本地 TX_LOOKUP 定位分叉块）
//	go run scripts/rebroadcast_fork_txs.go \
//	  -data-dir /path/to/node \
//	  -tx-hash 0x17fbc348e6430efef23129fbf37aacb365b5df1bd8621b8f0c2d35452a256db0 \
//	  -rpc https://rpc.vcity.app \
//	  -chain-id 8899
//
//	# 实际广播（去掉 -dry-run）
//	go run scripts/rebroadcast_fork_txs.go -data-dir ... -block-hash ... -rpc ... -chain-id ...
//
// 注意:
//   - 读取区块需能访问节点 blockchain DB（节点停或运行均可，只读打开；若 LOCK 冲突请先停节点）
//   - 若发送者链上 nonce 已大于该笔 nonce（1.5 个月内又发过交易），无法重播，只能人工处理
//   - 广播前请确认分叉块 hash 来自 explorer 的 reorg 标记块，不是 canonical 块
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain/storage/leveldb"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type txJob struct {
	Tx     *types.Transaction
	Header *types.Header
}

func main() {
	dataDir := flag.String("data-dir", "", "节点 data-dir（读取 blockchain leveldb）")
	blockHashStr := flag.String("block-hash", "", "reorg 分叉块 hash（explorer 上带 reorg 标记的块）")
	txHashStr := flag.String("tx-hash", "", "只重发指定交易 hash（可选，与 block-hash 二选一）")
	rpcURL := flag.String("rpc", "http://127.0.0.1:8545", "用于校验 nonce / 广播交易的 JSON-RPC 地址")
	chainID := flag.Uint64("chain-id", 0, "链 ID（EIP-155 签名恢复 from 用，必填）")
	dryRun := flag.Bool("dry-run", false, "只打印将要重发的交易，不广播")
	force := flag.Bool("force", false, "跳过交互确认")
	flag.Parse()

	if *dataDir == "" {
		log.Fatal("请指定 -data-dir")
	}
	if *chainID == 0 {
		log.Fatal("请指定 -chain-id")
	}
	if *blockHashStr == "" && *txHashStr == "" {
		log.Fatal("请指定 -block-hash 或 -tx-hash")
	}
	if *blockHashStr != "" && *txHashStr != "" {
		log.Fatal("-block-hash 与 -tx-hash 只能指定其一")
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "rebroadcast-fork-txs", Level: hclog.LevelFromString("ERROR")})
	st, err := leveldb.NewLevelDBStorage(filepath.Join(*dataDir, "blockchain"), logger)
	if err != nil {
		log.Fatalf("打开 blockchain db 失败: %v", err)
	}
	defer st.Close()

	signer := crypto.NewLondonSigner(
		*chainID,
		true,
		crypto.NewEIP155Signer(*chainID, true),
	)

	var jobs []txJob

	switch {
	case *txHashStr != "":
		txHash := types.StringToHash(*txHashStr)
		blockHash, ok := st.ReadTxLookup(txHash)
		if !ok {
			log.Fatalf("TX_LOOKUP 未找到交易 %s（本节点 DB 可能没有索引过该分叉块）", txHash)
		}
		hdr, err := st.ReadHeader(blockHash)
		if err != nil {
			log.Fatalf("读取区块头失败: %v", err)
		}
		body, err := st.ReadBody(blockHash)
		if err != nil {
			log.Fatalf("读取区块体失败: %v", err)
		}
		tx, idx := types.FindTxByHash(body.Transactions, txHash)
		if tx == nil {
			log.Fatalf("区块 %s 中未找到交易 %s", blockHash, txHash)
		}
		jobs = append(jobs, txJob{Tx: tx, Header: hdr})
		log.Printf("通过 TX_LOOKUP 定位: tx=%s block=%s height=%d", txHash, blockHash, hdr.Number)
		_ = idx

	case *blockHashStr != "":
		blockHash := types.StringToHash(*blockHashStr)
		hdr, err := st.ReadHeader(blockHash)
		if err != nil {
			log.Fatalf("读取区块头失败: %v", err)
		}
		body, err := st.ReadBody(blockHash)
		if err != nil {
			log.Fatalf("读取区块体失败: %v", err)
		}
		if len(body.Transactions) == 0 {
			log.Fatalf("区块 %s 高度 %d 没有交易", blockHash, hdr.Number)
		}
		for _, tx := range body.Transactions {
			jobs = append(jobs, txJob{Tx: tx, Header: hdr})
		}
	}

	canonicalHash, ok := st.ReadCanonicalHash(jobs[0].Header.Number)
	if !ok {
		log.Printf("警告: 无法读取高度 %d 的 canonical hash", jobs[0].Header.Number)
	} else if canonicalHash == jobs[0].Header.Hash {
		log.Printf("警告: 指定区块 hash 与高度 %d 的 canonical 块相同，可能不是 reorg 分叉块", jobs[0].Header.Number)
	} else {
		log.Printf("高度 %d: canonical=%s  分叉=%s", jobs[0].Header.Number, canonicalHash, jobs[0].Header.Hash)
	}

	fmt.Println()
	fmt.Println("=== 待重播交易 ===")
	var toSend []txJob
	for i, job := range jobs {
		tx := job.Tx
		if tx.From == (types.Address{}) {
			from, err := signer.Sender(tx)
			if err != nil {
				log.Printf("[%d] %s  恢复 from 失败: %v — 跳过", i+1, tx.Hash, err)
				continue
			}
			tx.From = from
		}
		tx.ComputeHash(job.Header.Number)

		valueVCITY := weiToVCITY(tx.Value)
		fmt.Printf("[%d] hash=%s\n", i+1, tx.Hash)
		fmt.Printf("    from=%s  to=%v  value=%s VCITY (%s wei)\n", tx.From, tx.To, valueVCITY, tx.Value.String())
		fmt.Printf("    nonce=%d  gas=%d  type=%d\n", tx.Nonce, tx.Gas, tx.Type)

		chainNonce, err := rpcGetTransactionCount(*rpcURL, tx.From)
		if err != nil {
			log.Printf("    查询链上 nonce 失败: %v — 跳过", err)
			continue
		}
		fmt.Printf("    链上 nonce(latest)=%d\n", chainNonce)

		if chainNonce > tx.Nonce {
			fmt.Printf("    ✗ 无法重播: 链上 nonce 已前进（该 nonce 已被其他交易占用或已执行）\n\n")
			continue
		}
		if chainNonce < tx.Nonce {
			fmt.Printf("    ✗ 无法重播: 链上 nonce 小于交易 nonce（状态异常）\n\n")
			continue
		}

		mined, err := rpcTxMinedOnCanonical(*rpcURL, tx.Hash, job.Header.Number)
		if err != nil {
			log.Printf("    检查 canonical 执行状态失败: %v", err)
		} else if mined {
			fmt.Printf("    ✓ 已在 canonical 链上确认，无需重播\n\n")
			continue
		}

		fmt.Printf("    → 可以重播\n\n")
		toSend = append(toSend, job)
	}

	if len(toSend) == 0 {
		log.Println("没有可重播的交易，退出")
		return
	}

	if *dryRun {
		log.Printf("dry-run 模式，共 %d 笔可重播，未广播", len(toSend))
		return
	}

	if !*force {
		fmt.Printf("将向 %s 广播 %d 笔 signed raw tx，继续? (yes/no): ", *rpcURL, len(toSend))
		var resp string
		if _, err := fmt.Scanln(&resp); err != nil {
			log.Fatal("读取确认失败")
		}
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "yes" && resp != "y" {
			log.Println("已取消")
			return
		}
	}

	for i, job := range toSend {
		tx := job.Tx
		raw := tx.MarshalRLP()
		hash, err := rpcSendRawTransaction(*rpcURL, raw)
		if err != nil {
			log.Printf("[%d] %s 广播失败: %v", i+1, tx.Hash, err)
			continue
		}
		log.Printf("[%d] %s 已提交 mempool, rpc 返回 hash=%s rawLen=%d", i+1, tx.Hash, hash, len(raw))
	}
}

func weiToVCITY(v *big.Int) string {
	if v == nil {
		return "0"
	}
	// 1 VCITY = 1e18 wei
	f := new(big.Float).SetInt(v)
	f.Quo(f, big.NewFloat(1e18))
	return f.Text('f', 4)
}

func rpcCall(url, method string, params []interface{}, result interface{}) error {
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	})
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var envelope rpcResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode rpc response: %w (body=%s)", err, truncate(string(respBody), 200))
	}
	if envelope.Error != nil {
		return fmt.Errorf("rpc error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if result == nil {
		return nil
	}
	if bytes.Equal(envelope.Result, []byte("null")) {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func rpcGetTransactionCount(url string, from types.Address) (uint64, error) {
	var hexNonce string
	if err := rpcCall(url, "eth_getTransactionCount", []interface{}{from.String(), "latest"}, &hexNonce); err != nil {
		return 0, err
	}
	return parseHexUint64(hexNonce)
}

func rpcSendRawTransaction(url string, raw []byte) (string, error) {
	hexRaw := "0x" + hex.EncodeToString(raw)
	var hash string
	if err := rpcCall(url, "eth_sendRawTransaction", []interface{}{hexRaw}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// rpcTxMinedOnCanonical 检查交易是否在 canonical 链上有 receipt（blockNumber 对应 canonical hash）。
func rpcTxMinedOnCanonical(url string, txHash types.Hash, forkHeight uint64) (bool, error) {
	var receipt struct {
		BlockHash   string `json:"blockHash"`
		BlockNumber string `json:"blockNumber"`
		Status      string `json:"status"`
	}
	if err := rpcCall(url, "eth_getTransactionReceipt", []interface{}{txHash.String()}, &receipt); err != nil {
		return false, err
	}
	if receipt.BlockHash == "" {
		return false, nil
	}

	var canonicalBlock struct {
		Hash string `json:"hash"`
	}
	heightHex := fmt.Sprintf("0x%x", forkHeight)
	if err := rpcCall(url, "eth_getBlockByNumber", []interface{}{heightHex, false}, &canonicalBlock); err != nil {
		return false, err
	}
	if canonicalBlock.Hash == "" {
		return false, nil
	}

	// receipt 指向 canonical 块 → 已执行
	if strings.EqualFold(receipt.BlockHash, canonicalBlock.Hash) {
		return true, nil
	}
	// receipt 只指向分叉块 → 未在 canonical 执行
	return false, nil
}

func parseHexUint64(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0x" {
		return 0, nil
	}
	s = strings.TrimPrefix(s, "0x")
	v, err := hex.DecodeString(s)
	if err != nil {
		return 0, err
	}
	var out uint64
	for _, b := range v {
		out = out<<8 + uint64(b)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
