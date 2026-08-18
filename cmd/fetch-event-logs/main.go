// fetch-event-logs 按块范围分批调用 eth_getLogs，适合同步服务拉事件或排查慢节点超时。
//
// 与节点 json_rpc_block_range_limit 解耦：用 -batch 控制单次 RPC 块跨度；每批使用独立 HTTP 超时（-timeout）。
//
// 用法:
//
//	go run ./cmd/fetch-event-logs -rpc http://127.0.0.1:9546 \
//	  -from 0xf2f00e -to 0xf2f3fd \
//	  -contracts 0xContract1,0xContract2
//
// 默认 topic0 = Transfer(address,address,uint256)。自定义事件:
//
//	go run ./cmd/fetch-event-logs -rpc ... -from 100 -to 200 -topic0 0x...
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
	"strconv"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/command"
)

const topic0Transfer = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type filterParam struct {
	Address   interface{}   `json:"address,omitempty"` // string or []string
	FromBlock string        `json:"fromBlock"`
	ToBlock   string        `json:"toBlock"`
	Topics    []interface{} `json:"topics,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type logRow struct {
	Address          string   `json:"address"`
	TxHash           string   `json:"transactionHash"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	TransactionIndex string   `json:"transactionIndex"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	Removed          bool     `json:"removed"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(command.ExitCodeInternal)
	}
}

func run() error {
	rpcURL := flag.String("rpc", "", "JSON-RPC endpoint (required)")
	fromBlk := flag.String("from", "", "fromBlock: decimal, 0x hex, or earliest")
	toBlk := flag.String("to", "latest", "toBlock: decimal, 0x hex, latest, or earliest")
	contracts := flag.String("contracts", "", "comma-separated contract addresses (optional; empty = all contracts)")
	topic0 := flag.String("topic0", topic0Transfer, "topic0 event signature hash; empty to omit topics filter")
	batch := flag.Uint64("batch", 200, "blocks per eth_getLogs request (inclusive range)")
	timeout := flag.Duration("timeout", 120*time.Second, "HTTP timeout per batch request")
	sleep := flag.Duration("sleep", 10*time.Millisecond, "pause between successful batches")
	printJSON := flag.Bool("json", false, "print logs as JSON array on stdout")
	outPath := flag.String("o", "", "also write stdout payload to this file")
	flag.Parse()

	if strings.TrimSpace(*rpcURL) == "" {
		return errors.New("usage: -rpc is required")
	}
	if strings.TrimSpace(*fromBlk) == "" {
		return errors.New("usage: -from is required")
	}

	fromNum, err := resolveBlockRef(*rpcURL, *fromBlk, *timeout)
	if err != nil {
		return err
	}
	toNum, err := resolveBlockRef(*rpcURL, *toBlk, *timeout)
	if err != nil {
		return err
	}
	if fromNum > toNum {
		return fmt.Errorf("invalid range: fromBlock > toBlock (%d > %d)", fromNum, toNum)
	}

	addrs := parseAddresses(*contracts)
	topicFilter := buildTopicFilter(*topic0)

	totalChunks := chunkTotal(fromNum, toNum, *batch)
	var all []logRow

	chunkIdx := 0
	for start := fromNum; start <= toNum; start += *batch {
		chunkIdx++
		end := start + *batch - 1
		if end > toNum {
			end = toNum
		}
		fmt.Fprintf(os.Stderr, "progress: blocks %s..%s (chunk %d/%d)\n",
			blockNumHex(start), blockNumHex(end), chunkIdx, totalChunks)

		fp := filterParam{
			FromBlock: blockNumHex(start),
			ToBlock:   blockNumHex(end),
			Topics:    topicFilter,
		}
		if len(addrs) == 1 {
			fp.Address = addrs[0]
		} else if len(addrs) > 1 {
			fp.Address = addrs
		}

		rows, err := callGetLogs(*rpcURL, fp, *timeout)
		if err != nil {
			return fmt.Errorf("eth_getLogs %s..%s: %w", blockNumHex(start), blockNumHex(end), err)
		}
		fmt.Fprintf(os.Stderr, "  batch logs=%d\n", len(rows))
		all = append(all, rows...)

		if *sleep > 0 && end < toNum {
			time.Sleep(*sleep)
		}
	}

	fmt.Fprintf(os.Stderr, "done: total_logs=%d blocks=%d..%d\n", len(all), fromNum, toNum)

	var stdout io.Writer = os.Stdout
	if p := strings.TrimSpace(*outPath); p != "" {
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		defer func() {
			_ = f.Sync()
			_ = f.Close()
		}()
		stdout = io.MultiWriter(os.Stdout, f)
		fmt.Fprintf(os.Stderr, "wrote: %s\n", p)
	}

	if *printJSON {
		out, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(out))
		return err
	}

	for _, r := range all {
		from, to := "", ""
		if len(r.Topics) > 2 {
			from = topicToAddr(r.Topics[1])
			to = topicToAddr(r.Topics[2])
		}
		_, _ = fmt.Fprintf(stdout, "block=%s tx=%s logIndex=%s contract=%s from=%s to=%s\n",
			r.BlockNumber, r.TxHash, r.LogIndex, r.Address, from, to)
	}
	return nil
}

func parseAddresses(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func buildTopicFilter(topic0 string) []interface{} {
	t := strings.TrimSpace(topic0)
	if t == "" {
		return nil
	}
	return []interface{}{t}
}

func topicToAddr(topic string) string {
	if len(topic) < 2+24+40 {
		return ""
	}
	return "0x" + topic[len(topic)-40:]
}

func blockNumHex(n uint64) string {
	return "0x" + strconv.FormatUint(n, 16)
}

func parseHexUint64(s string) (uint64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return 0, errors.New("empty hex block number")
	}
	return strconv.ParseUint(s, 16, 64)
}

func rpcJSON(rpcURL, method string, params interface{}, timeout time.Duration) ([]byte, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var rr rpcResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, err
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}
	return body, nil
}

func ethBlockNumber(rpcURL string, timeout time.Duration) (uint64, error) {
	body, err := rpcJSON(rpcURL, "eth_blockNumber", []interface{}{}, timeout)
	if err != nil {
		return 0, err
	}
	var wrap struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return 0, err
	}
	return parseHexUint64(wrap.Result)
}

func resolveBlockRef(rpcURL, tag string, timeout time.Duration) (uint64, error) {
	t := strings.ToLower(strings.TrimSpace(tag))
	switch t {
	case "earliest":
		return 0, nil
	case "latest":
		return ethBlockNumber(rpcURL, timeout)
	case "pending":
		return 0, errors.New("pending block tag is not supported")
	default:
		if strings.HasPrefix(t, "0x") {
			return parseHexUint64(t)
		}
		n, err := strconv.ParseUint(strings.TrimSpace(tag), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid block %q: %w", tag, err)
		}
		return n, nil
	}
}

func chunkTotal(from, to, span uint64) int {
	if to < from || span == 0 {
		return 0
	}
	width := to - from + 1
	return int((width + span - 1) / span)
}

func callGetLogs(rpcURL string, fp filterParam, timeout time.Duration) ([]logRow, error) {
	body, err := rpcJSON(rpcURL, "eth_getLogs", []interface{}{fp}, timeout)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, err
	}
	if len(wrap.Result) == 0 || string(wrap.Result) == "null" {
		return nil, nil
	}
	var rows []logRow
	if err := json.Unmarshal(wrap.Result, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
