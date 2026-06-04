// address-tx-scan 通过 JSON-RPC 逐块拉取完整交易（eth_getBlockByNumber(..., true)），
// 筛出 from 或 to 等于给定地址的顶层交易，用于与浏览器索引对照、自查是否「漏交易」。
//
// 限制：仅看区块里打包的顶层交易的 from/to；不含 internal call、不含仅由合约事件
// 产生的「代币转账」等，那些需另用 eth_getLogs 或 trace API。
//
//	go run ./cmd/address-tx-scan -rpc https://mainnet-rpc.vcity.app \
//	  -addr 0x561B66c57E305f68A660D8e327401B396a988Ff8 -from 0x84FA30 -to latest -o hits.txt
//
// 按块扫描；stderr 每 2000 块打印一行 progress（与 erc721-token-logs 分片粒度一致）。
// 本工具不使用 os.Exit：一律从 main 正常返回（退出码恒为 0），错误见 stderr。
// Windows 下带 -o 时默认结束前等待按键；自动化设 ADDRESS_TX_SCAN_NOWAIT=1 或加 -wait。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// chunkBlockSpan 为 stderr 进度打印的块跨度（仍逐块 eth_getBlockByNumber，与 erc721-token-logs 一致）。
const chunkBlockSpan = 2000

type rpcBody struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type blockTx struct {
	Hash  string  `json:"hash"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Value string  `json:"value"`
}

type blockEnvelope struct {
	Number       string          `json:"number"`
	Transactions json.RawMessage `json:"transactions"`
}

func rpcPost(rpcURL string, method string, params []interface{}) ([]byte, error) {
	body, err := json.Marshal(rpcBody{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	var rr rpcResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, err
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}
	return raw, nil
}

func ethBlockNumber(rpcURL string) (uint64, error) {
	raw, err := rpcPost(rpcURL, "eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, err
	}
	var wrap struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return 0, err
	}
	return parseHexUint64(wrap.Result)
}

func parseHexUint64(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(s)), "0x")
	if s == "" {
		return 0, errors.New("empty hex")
	}
	return strconv.ParseUint(s, 16, 64)
}

func resolveBlock(rpcURL, tag string) (uint64, error) {
	t := strings.ToLower(strings.TrimSpace(tag))
	switch t {
	case "earliest":
		return 0, nil
	case "latest":
		return ethBlockNumber(rpcURL)
	default:
		if strings.HasPrefix(t, "0x") {
			return parseHexUint64(t)
		}
		return strconv.ParseUint(strings.TrimSpace(tag), 10, 64)
	}
}

func blockHex(n uint64) string {
	return "0x" + strconv.FormatUint(n, 16)
}

func chunkTotal(from, to, span uint64) int {
	if to < from {
		return 0
	}
	width := to - from + 1
	return int((width + span - 1) / span)
}

func normAddr(a string) string {
	return strings.ToLower(strings.TrimSpace(a))
}

func addrMatch(want, from string, to *string) bool {
	w := normAddr(want)
	if normAddr(from) == w {
		return true
	}
	if to != nil && normAddr(*to) == w {
		return true
	}
	return false
}

func getBlockWithTxs(rpcURL string, num uint64) (*blockEnvelope, []blockTx, error) {
	raw, err := rpcPost(rpcURL, "eth_getBlockByNumber", []interface{}{blockHex(num), true})
	if err != nil {
		return nil, nil, err
	}
	var wrap struct {
		Result *blockEnvelope `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, nil, err
	}
	if wrap.Result == nil {
		return nil, nil, fmt.Errorf("block %s not returned (null)", blockHex(num))
	}
	var txs []blockTx
	if len(wrap.Result.Transactions) > 0 && string(wrap.Result.Transactions) != "null" {
		if err := json.Unmarshal(wrap.Result.Transactions, &txs); err != nil {
			return nil, nil, fmt.Errorf("decode txs: %w", err)
		}
	}
	return wrap.Result, txs, nil
}

func pauseIfRequested(wait bool) {
	if !wait {
		return
	}
	pauseForUser()
}

func main() {
	run()
}

func run() {
	rpc := flag.String("rpc", "https://mainnet-rpc.vcity.app", "JSON-RPC HTTP endpoint")
	addr := flag.String("addr", "", "address to match on from or to (required)")
	fromB := flag.String("from", "", "start block: hex 0x.. or earliest")
	toB := flag.String("to", "latest", "end block: hex 0x.. or latest")
	outPath := flag.String("o", "", "write matching lines to this file (UTF-8); empty = stdout only")
	wait := flag.Bool("wait", false, "wait for Enter before exit; on Windows, -o implies wait unless ADDRESS_TX_SCAN_NOWAIT=1")
	flag.Parse()

	outTrim := strings.TrimSpace(*outPath)
	waitEffective := *wait
	if runtime.GOOS == "windows" && outTrim != "" && os.Getenv("ADDRESS_TX_SCAN_NOWAIT") == "" {
		waitEffective = true
	}
	defer pauseIfRequested(waitEffective)

	if strings.TrimSpace(*addr) == "" || strings.TrimSpace(*fromB) == "" {
		fmt.Fprintln(os.Stderr, "usage: -rpc URL -addr 0x... -from earliest|0x... [-to latest|0x...] [-o file]")
		flag.Usage()
		return
	}
	want := *addr

	fromNum, err := resolveBlock(*rpc, *fromB)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	toNum, err := resolveBlock(*rpc, *toB)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if fromNum > toNum {
		fmt.Fprintf(os.Stderr, "invalid range from > to (%d > %d)\n", fromNum, toNum)
		return
	}

	var out *os.File
	if outTrim != "" {
		out, err = os.Create(outTrim)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		defer func() { _ = out.Close() }()
	}

	w := io.Writer(os.Stdout)
	if out != nil {
		w = io.MultiWriter(os.Stdout, out)
	}

	hits := 0
	totalChunks := chunkTotal(fromNum, toNum, chunkBlockSpan)
	chunkIdx := 0
	for start := fromNum; start <= toNum; start += chunkBlockSpan {
		chunkIdx++
		end := start + chunkBlockSpan - 1
		if end > toNum {
			end = toNum
		}
		fmt.Fprintf(os.Stderr, "progress: blocks %s..%s (chunk %d/%d) hits=%d\n",
			blockHex(start), blockHex(end), chunkIdx, totalChunks, hits)

		for n := start; n <= end; n++ {
			_, txs, err := getBlockWithTxs(*rpc, n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "block %s: %v\n", blockHex(n), err)
				return
			}
			for _, tx := range txs {
				if !addrMatch(want, tx.From, tx.To) {
					continue
				}
				hits++
				toStr := ""
				if tx.To != nil {
					toStr = *tx.To
				}
				_, _ = fmt.Fprintf(w, "block=%s tx=%s from=%s to=%s value=%s\n",
					blockHex(n), tx.Hash, tx.From, toStr, tx.Value)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "done: blocks %s..%s hits=%d\n", blockHex(fromNum), blockHex(toNum), hits)
	if out != nil {
		fmt.Fprintf(os.Stderr, "wrote: %s\n", outTrim)
	}
}
