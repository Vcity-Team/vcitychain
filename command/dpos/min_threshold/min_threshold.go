package min_threshold

import (
	"encoding/json"
)

// MinThresholdResult 最小投票门槛查询结果
type MinThresholdResult struct {
	Data map[string]interface{} `json:"data"`
}

func (r *MinThresholdResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *MinThresholdResult) GetJSONResult() interface{} {
	return r.Data
}
