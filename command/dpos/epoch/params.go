package epoch

import "encoding/json"

type EpochResult struct {
	Data map[string]interface{} `json:"data"`
}

func (r *EpochResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *EpochResult) GetJSONResult() interface{} {
	return r.Data
}
