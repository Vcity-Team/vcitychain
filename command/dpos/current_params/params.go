package current_params

import (
	"encoding/json"
)

type CurrentParamsResult struct {
	Data map[string]interface{} `json:"data"`
}

func (r *CurrentParamsResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *CurrentParamsResult) GetJSONResult() interface{} {
	return r.Data
}
