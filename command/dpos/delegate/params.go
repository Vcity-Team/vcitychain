package delegate

import "fmt"

// RegisterDelegateResult represents the result of delegate registration

type RegisterDelegateResult struct {
	Success bool `json:"success"`

	Message string `json:"message"`

	Error string `json:"error,omitempty"`

	ContractAddress string `json:"contractAddress,omitempty"`
}

// GetOutput returns the formatted output string for the command

func (r *RegisterDelegateResult) GetOutput() string {

	if r.Success {
		// 老路径 delegateTest：只关心保证金进入的合约地址
		if r.ContractAddress != "" {
			return fmt.Sprintf("[DELEGATE REGISTRATION RESULT]\ncontractAddress: %s\n", r.ContractAddress)
		}

		return fmt.Sprintf(`[DELEGATE REGISTRATION RESULT]

Status: Success

Message: %s`, r.Message)

	}

	return fmt.Sprintf(`[DELEGATE REGISTRATION RESULT]

Status: Failed

Error: %s`, r.Error)

}

// GetJSONResult returns the JSON result for the command

func (r *RegisterDelegateResult) GetJSONResult() interface{} {

	return r

}
