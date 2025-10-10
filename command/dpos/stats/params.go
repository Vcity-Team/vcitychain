package stats

import "encoding/json"

type ValidatorStatsResult struct {
	Data map[string]interface{} `json:"data"`
}

func (r *ValidatorStatsResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *ValidatorStatsResult) GetJSONResult() interface{} {
	return r.Data
}
