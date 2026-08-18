// 用法（只读，不改库，可以在节点运行时执行）:
//
//	go run scripts/show_dpos_delegate_threshold.go -db "C:\work\nodes\node0\consensus\dpos\dpos.db"
//
// 输出当前 dpos_delegate_threshold 的存储值（CurrentValue）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Vcity-Team/vcitychain/command"
)

type parameterCurrentValue struct {
	ParameterName string      `json:"parameter_name"`
	CurrentValue  interface{} `json:"current_value"`
}

func main() {
	dbPath := flag.String("db", "", "BoltDB 路径，例如 C:\\work\\nodes\\node0\\consensus\\dpos\\dpos.db")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "请用 -db 指定 dpos.db 路径，例如: -db /home/testnet/nodes/node1/consensus/dpos/dpos.db")
		os.Exit(command.ExitCodeParams)
	}

	fmt.Fprintln(os.Stdout, "[show_dpos_delegate_threshold] 使用数据库路径:", *dbPath)

	if _, err := os.Stat(*dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "错误: 数据库文件不存在或无法访问:", err)
		os.Exit(command.ExitCodeParams)
	}

	// 加超时：节点运行时会把 db 锁住，Open 会一直等，这里 3 秒后报错
	db, err := bolt.Open(*dbPath, 0444, &bolt.Options{
		ReadOnly: true,
		Timeout:  3 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: 打开数据库失败（若节点正在运行，请先停节点或复制 dpos.db 再读）:", err)
		os.Exit(command.ExitCodeInternal)
	}
	defer db.Close()

	var raw []byte
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("parameters"))
		if b == nil {
			return fmt.Errorf("bucket 'parameters' 不存在")
		}
		raw = b.Get([]byte("dpos_delegate_threshold"))
		if raw == nil {
			return fmt.Errorf("未找到 key 'dpos_delegate_threshold'")
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: 读取失败:", err)
		os.Exit(command.ExitCodeInternal)
	}

	var param parameterCurrentValue
	if err := json.Unmarshal(raw, &param); err != nil {
		fmt.Fprintln(os.Stderr, "错误: 解析 JSON 失败:", err)
		os.Exit(command.ExitCodeInternal)
	}

	fmt.Fprintln(os.Stdout, "当前 dpos_delegate_threshold:", param.CurrentValue)
	fmt.Fprintln(os.Stdout, "raw JSON:", string(raw))
}
