// 用法（必须先停掉节点再执行）:
//
//	# 写入主网推荐值（奖励账户 + 出块奖励四参数）
//	go run scripts/fix_dpos_reward_parameters.go -db ./node1/consensus/dpos/dpos.db
//
//	# 只查看当前 Bolt 里存的值（只读，节点运行中可能锁库失败）
//	go run scripts/fix_dpos_reward_parameters.go -db ./node1/consensus/dpos/dpos.db -show
//
// 在仓库根目录执行（需 go.mod）；生产机无源码时可:
//   mkdir -p ~/fixtool && cd ~/fixtool && go mod init fixtool && go get go.etcd.io/bbolt@v1.3.8
//   再复制本文件到 ~/fixtool 后 go run fix_dpos_reward_parameters.go -db /path/to/dpos.db
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
	Source        string      `json:"source"`
}

var defaultFixes = map[string]interface{}{
	"dpos_reward_distribution_account":          "0xc07b407F375109239e9097dACa87898bc4584ddb",
	"dpos_reward_distribution_activation_epoch": uint64(803),
	"dpos_block_producer_reward_per_block":      "7600000000000000",
	"dpos_producer_reward_activation_epoch":     uint64(803),
}

func main() {
	dbPath := flag.String("db", "", "dpos.db 路径，例如 ./node1/consensus/dpos/dpos.db")
	showOnly := flag.Bool("show", false, "只读查看上述四个参数，不写入")
	rewardAccount := flag.String("reward-account", "0xc07b407F375109239e9097dACa87898bc4584ddb", "dpos_reward_distribution_account")
	rewardEpoch := flag.Uint64("reward-epoch", 803, "dpos_reward_distribution_activation_epoch")
	perBlock := flag.String("per-block", "7600000000000000", "dpos_block_producer_reward_per_block (wei string)")
	producerEpoch := flag.Uint64("producer-epoch", 803, "dpos_producer_reward_activation_epoch")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("请用 -db 指定 dpos.db 路径")
	}
	if _, err := os.Stat(*dbPath); err != nil {
		log.Fatalf("数据库不存在: %v", err)
	}

	fixes := map[string]interface{}{
		"dpos_reward_distribution_account":          *rewardAccount,
		"dpos_reward_distribution_activation_epoch": *rewardEpoch,
		"dpos_block_producer_reward_per_block":      *perBlock,
		"dpos_producer_reward_activation_epoch":     *producerEpoch,
	}

	opts := &bolt.Options{Timeout: 5 * time.Second}
	if *showOnly {
		opts.ReadOnly = true
	}

	db, err := bolt.Open(*dbPath, 0600, opts)
	if err != nil {
		log.Fatalf("打开数据库失败（请先停止节点）: %v", err)
	}
	defer db.Close()

	if *showOnly {
		if err := showParameters(db, *dbPath, fixes); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := writeParameters(db, fixes); err != nil {
		log.Fatal(err)
	}
	log.Println("✅ 已写入四个 DPoS 经济参数，请启动节点后用 dpos_getVotableCurrentParameters 验证")
}

func listBuckets(tx *bolt.Tx) []string {
	var names []string
	_ = tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
		names = append(names, string(name))
		return nil
	})
	return names
}

func showParameters(db *bolt.DB, dbPath string, keys map[string]interface{}) error {
	info, err := os.Stat(dbPath)
	if err == nil {
		fmt.Printf("db=%s size=%d bytes\n", dbPath, info.Size())
	}
	return db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("parameters"))
		if b == nil {
			buckets := listBuckets(tx)
			fmt.Printf("bucket 'parameters' 不存在；当前库内 bucket 共 %d 个:\n", len(buckets))
			for _, name := range buckets {
				fmt.Printf("  - %s\n", name)
			}
			if len(buckets) == 0 {
				return fmt.Errorf("库为空或未初始化：请确认 -db 路径是否为 nodeX/consensus/dpos/dpos.db（不是 dpos.db.rewards）")
			}
			return fmt.Errorf("库已打开但无 parameters bucket：可能指错了 dpos.db，或该库从未成功启动过 DPoS")
		}
		for name := range keys {
			raw := b.Get([]byte(name))
			if raw == nil {
				fmt.Printf("%s: (missing)\n", name)
				continue
			}
			var pv parameterCurrentValue
			if err := json.Unmarshal(raw, &pv); err != nil {
				fmt.Printf("%s: raw=%s\n", name, string(raw))
				continue
			}
			fmt.Printf("%s: current_value=%v source=%s\n", name, pv.CurrentValue, pv.Source)
		}
		return nil
	})
}

func writeParameters(db *bolt.DB, fixes map[string]interface{}) error {
	now := time.Now().UTC()
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("parameters"))
		if err != nil {
			return fmt.Errorf("创建 parameters bucket 失败: %w", err)
		}
		for name, val := range fixes {
			pv := parameterCurrentValue{
				ParameterName: name,
				CurrentValue:  val,
				UpdatedAt:     now,
				Source:        "manual_db_edit",
			}
			raw, err := json.Marshal(pv)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(name), raw); err != nil {
				return err
			}
			log.Printf("写入 %s = %v", name, val)
		}
		return nil
	})
}
