// 用法（先停掉节点再执行）:
//   go run scripts/update_dpos_delegate_threshold.go -db "C:\work\nodes\node0\consensus\dpos\dpos.db" -value "10000000000000000000"
// 将 dpos_delegate_threshold 改为 10 VCITY（10 * 10^18 wei）。-value 不填则默认 10 VCITY。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

type parameterCurrentValue struct {
	ParameterName string      `json:"parameter_name"`
	CurrentValue  interface{} `json:"current_value"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Source        string     `json:"source"`
}

func main() {
	dbPath := flag.String("db", "", "BoltDB 路径，例如 C:\\work\\nodes\\node0\\consensus\\dpos\\dpos.db")
	value := flag.String("value", "10000000000000000000", "新门槛值（wei 字符串），10 VCITY = 10000000000000000000")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("请用 -db 指定 dpos.db 路径，例如: -db C:\\work\\nodes\\node0\\consensus\\dpos\\dpos.db")
	}

	if _, err := os.Stat(*dbPath); err != nil {
		log.Fatalf("数据库文件不存在或无法访问: %v", err)
	}

	db, err := bolt.Open(*dbPath, 0600, nil)
	if err != nil {
		log.Fatalf("打开数据库失败（请先停止节点）: %v", err)
	}
	defer db.Close()

	param := parameterCurrentValue{
		ParameterName: "dpos_delegate_threshold",
		CurrentValue:  *value,
		UpdatedAt:     time.Now().UTC(),
		Source:        "manual_db_edit",
	}
	data, err := json.Marshal(param)
	if err != nil {
		log.Fatalf("序列化失败: %v", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("parameters"))
		if b == nil {
			return fmt.Errorf("bucket 'parameters' 不存在")
		}
		return b.Put([]byte("dpos_delegate_threshold"), data)
	})
	if err != nil {
		log.Fatalf("写入失败: %v", err)
	}

	log.Printf("已更新 dpos_delegate_threshold = %s (10 VCITY)", *value)
}
