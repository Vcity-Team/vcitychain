package rewards

import "encoding/json"

type ValidatorRewardsResult struct {
	Data interface{} `json:"data"`
}

func (r *ValidatorRewardsResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *ValidatorRewardsResult) GetJSONResult() interface{} {
	return r.Data
}
