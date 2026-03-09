// 用法（只读，不改库，可以在节点运行时执行）:
//   go run scripts/show_dpos_delegate_threshold.go -db "C:\work\nodes\node0\consensus\dpos\dpos.db"
// 输出当前 dpos_delegate_threshold 的存储值（CurrentValue）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	bolt "go.etcd.io/bbolt"
)

type parameterCurrentValue struct {
	ParameterName string      `json:"parameter_name"`
	CurrentValue  interface{} `json:"current_value"`
}

func main() {
	dbPath := flag.String("db", "", "BoltDB 路径，例如 C:\\work\\nodes\\node0\\consensus\\dpos\\dpos.db")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("请用 -db 指定 dpos.db 路径，例如: -db C:\\work\\nodes\\node0\\consensus\\dpos\\dpos.db")
	}

	if _, err := os.Stat(*dbPath); err != nil {
		log.Fatalf("数据库文件不存在或无法访问: %v", err)
	}

	db, err := bolt.Open(*dbPath, 0600, &bolt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
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
		log.Fatalf("读取失败: %v", err)
	}

	var param parameterCurrentValue
	if err := json.Unmarshal(raw, &param); err != nil {
		log.Fatalf("解析 JSON 失败: %v", err)
	}

	log.Printf("当前 dpos_delegate_threshold: %v (raw JSON: %s)", param.CurrentValue, string(raw))
}

