package parameters

import (
	"encoding/json"
)

type ParametersResult struct {
	Data map[string]interface{} `json:"data"`
}

func (r *ParametersResult) GetOutput() string {
	jsonData, _ := json.MarshalIndent(r.Data, "", "  ")
	return string(jsonData)
}

func (r *ParametersResult) GetJSONResult() interface{} {
	return r.Data
}
