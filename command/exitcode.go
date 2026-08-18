package command

// Standard CLI exit codes shared by all VCityChain commands.
const (
	// ExitCodeOK indicates the command completed successfully.
	ExitCodeOK = 0
	// ExitCodeParams indicates invalid arguments or input.
	ExitCodeParams = 1
	// ExitCodeRPC indicates RPC or network errors.
	ExitCodeRPC = 2
	// ExitCodeChain indicates an on-chain transaction failure.
	ExitCodeChain = 3
	// ExitCodeInternal indicates an internal or unknown error.
	ExitCodeInternal = 4
)
