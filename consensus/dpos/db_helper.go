package dpos

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// dbHelper 数据库操作辅助工具，提供通用的 bucket 操作
type dbHelper struct {
	logger *loggerWrapper
}

// newDBHelper 创建数据库操作辅助工具
func newDBHelper(logger *loggerWrapper) *dbHelper {
	return &dbHelper{logger: logger}
}

// saveToBucket 通用的保存到 bucket 的方法
func (h *dbHelper) saveToBucket(tx *bolt.Tx, bucketName string, key []byte, value interface{}) error {
	bucket, err := tx.CreateBucketIfNotExists([]byte(bucketName))
	if err != nil {
		h.logger.Error("Failed to create bucket", "bucket", bucketName, "error", err)
		return WrapError("create bucket", err)
	}

	data, err := json.Marshal(value)
	if err != nil {
		h.logger.Error("Failed to marshal", "error", err)
		return WrapError("marshal data", err)
	}

	if err := bucket.Put(key, data); err != nil {
		h.logger.Error("Failed to put data", "error", err)
		return WrapError("save data", err)
	}

	return nil
}

// getFromBucket 通用的从 bucket 获取数据的方法
func (h *dbHelper) getFromBucket(tx *bolt.Tx, bucketName string, key []byte, dest interface{}) error {
	bucket := tx.Bucket([]byte(bucketName))
	if bucket == nil {
		return WrapErrorf("get from bucket", "bucket %s not found", bucketName)
	}

	data := bucket.Get(key)
	if data == nil {
		return WrapErrorf("get from bucket", "key not found in bucket %s", bucketName)
	}

	if err := json.Unmarshal(data, dest); err != nil {
		h.logger.Error("Failed to unmarshal", "error", err)
		return WrapError("unmarshal data", err)
	}

	return nil
}

// getFromBucketOptional 从 bucket 获取数据（可选，不存在不报错）
func (h *dbHelper) getFromBucketOptional(tx *bolt.Tx, bucketName string, key []byte, dest interface{}) error {
	bucket := tx.Bucket([]byte(bucketName))
	if bucket == nil {
		return nil // bucket 不存在，返回 nil（表示未找到）
	}

	data := bucket.Get(key)
	if data == nil {
		return nil // key 不存在，返回 nil（表示未找到）
	}

	if err := json.Unmarshal(data, dest); err != nil {
		h.logger.Error("Failed to unmarshal", "error", err)
		return WrapError("unmarshal data", err)
	}

	return nil
}
