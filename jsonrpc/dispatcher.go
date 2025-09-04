package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/consensus"
	consensusdpos "github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

type serviceData struct {
	sv      reflect.Value
	funcMap map[string]*funcData
}

type funcData struct {
	inNum int
	reqt  []reflect.Type
	fv    reflect.Value
	isDyn bool
}

func (f *funcData) numParams() int {
	return f.inNum - 1
}

type endpoints struct {
	Eth    *Eth
	Web3   *Web3
	Net    *Net
	TxPool *TxPool
	Bridge *Bridge
	Debug  *Debug
	DPOS   *DPOS
}

// Dispatcher handles all json rpc requests by delegating
// the execution flow to the corresponding service
type Dispatcher struct {
	logger        hclog.Logger
	serviceMap    map[string]*serviceData
	filterManager *FilterManager
	endpoints     endpoints

	params *dispatcherParams
}

type dispatcherParams struct {
	chainID   uint64
	chainName string

	priceLimit              uint64
	jsonRPCBatchLengthLimit uint64
	blockRangeLimit         uint64

	concurrentRequestsDebug uint64
}

func (dp dispatcherParams) isExceedingBatchLengthLimit(value uint64) bool {
	return dp.jsonRPCBatchLengthLimit != 0 && value > dp.jsonRPCBatchLengthLimit
}

func newDispatcher(
	logger hclog.Logger,
	store JSONRPCStore,
	params *dispatcherParams,
) (*Dispatcher, error) {
	d := &Dispatcher{
		logger: logger.Named("dispatcher"),
		params: params,
	}

	if store != nil {
		d.filterManager = NewFilterManager(logger, store, params.blockRangeLimit)
		go d.filterManager.Run()
	}

	if err := d.registerEndpoints(store); err != nil {
		return nil, err
	}

	return d, nil
}

func (d *Dispatcher) registerEndpoints(store JSONRPCStore) error {
	d.endpoints.Eth = &Eth{
		d.logger,
		store,
		d.params.chainID,
		d.filterManager,
		d.params.priceLimit,
	}
	d.endpoints.Net = &Net{
		store,
		d.params.chainID,
	}
	d.endpoints.Web3 = &Web3{
		d.params.chainName,
	}
	d.endpoints.TxPool = &TxPool{
		store,
	}
	d.endpoints.Bridge = &Bridge{
		store,
	}
	d.endpoints.Debug = NewDebug(store, d.params.concurrentRequestsDebug)

	// Add DPOS endpoint with store adapter
	d.endpoints.DPOS = NewDPOS(d.logger, &dposStoreAdapter{store: store})

	var err error

	if err = d.registerService("eth", d.endpoints.Eth); err != nil {
		return err
	}

	if err = d.registerService("net", d.endpoints.Net); err != nil {
		return err
	}

	if err = d.registerService("web3", d.endpoints.Web3); err != nil {
		return err
	}

	if err = d.registerService("txpool", d.endpoints.TxPool); err != nil {
		return err
	}

	if err = d.registerService("bridge", d.endpoints.Bridge); err != nil {
		return err
	}

	if err = d.registerService("debug", d.endpoints.Debug); err != nil {
		return err
	}

	return d.registerService("dpos", d.endpoints.DPOS)
}

func (d *Dispatcher) getFnHandler(req Request) (*serviceData, *funcData, Error) {
	callName := strings.SplitN(req.Method, "_", 2)
	if len(callName) != 2 {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	serviceName, funcName := callName[0], callName[1]

	service, ok := d.serviceMap[serviceName]
	if !ok {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	fd, ok := service.funcMap[funcName]

	if !ok {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	return service, fd, nil
}

type wsConn interface {
	WriteMessage(messageType int, data []byte) error
	GetFilterID() string
	SetFilterID(string)
}

// as per https://www.jsonrpc.org/specification, the `id` in JSON-RPC 2.0
// can only be a string or a non-decimal integer
func formatID(id interface{}) (interface{}, Error) {
	switch t := id.(type) {
	case string:
		return t, nil
	case float64:
		if t == math.Trunc(t) {
			return int(t), nil
		} else {
			return "", NewInvalidRequestError("Invalid json request")
		}
	case nil:
		return nil, nil
	default:
		return "", NewInvalidRequestError("Invalid json request")
	}
}

func (d *Dispatcher) handleSubscribe(req Request, conn wsConn) (string, Error) {
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return "", NewInvalidRequestError("Invalid json request")
	}

	if len(params) == 0 {
		return "", NewInvalidParamsError("Invalid params")
	}

	subscribeMethod, ok := params[0].(string)
	if !ok {
		return "", NewSubscriptionNotFoundError(subscribeMethod)
	}

	var filterID string
	if subscribeMethod == "newHeads" {
		filterID = d.filterManager.NewBlockFilter(conn)
	} else if subscribeMethod == "logs" {
		logQuery, err := decodeLogQueryFromInterface(params[1])
		if err != nil {
			return "", NewInternalError(err.Error())
		}
		filterID = d.filterManager.NewLogFilter(logQuery, conn)
	} else if subscribeMethod == "newPendingTransactions" {
		filterID = d.filterManager.NewPendingTxFilter(conn)
	} else {
		return "", NewSubscriptionNotFoundError(subscribeMethod)
	}

	return filterID, nil
}

func (d *Dispatcher) handleUnsubscribe(req Request) (bool, Error) {
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return false, NewInvalidRequestError("Invalid json request")
	}

	if len(params) != 1 {
		return false, NewInvalidParamsError("Invalid params")
	}

	filterID, ok := params[0].(string)
	if !ok {
		return false, NewSubscriptionNotFoundError(filterID)
	}

	return d.filterManager.Uninstall(filterID), nil
}

func (d *Dispatcher) RemoveFilterByWs(conn wsConn) {
	d.filterManager.RemoveFilterByWs(conn)
}

func (d *Dispatcher) HandleWs(reqBody []byte, conn wsConn) ([]byte, error) {
	const (
		openSquareBracket  byte = '['
		closeSquareBracket byte = ']'
		comma              byte = ','
	)

	reqBody = bytes.TrimLeft(reqBody, " \t\r\n")

	// if body begins with [ consider it as a batch request
	if len(reqBody) > 0 && reqBody[0] == openSquareBracket {
		var batchReq BatchRequest

		err := json.Unmarshal(reqBody, &batchReq)
		if err != nil {
			return NewRPCResponse(nil, "2.0", nil,
				NewInvalidRequestError("Invalid json batch request")).Bytes()
		}

		// if not disabled, avoid handling long batch requests
		if d.params.isExceedingBatchLengthLimit(uint64(len(batchReq))) {
			return NewRPCResponse(
				nil,
				"2.0",
				nil,
				NewInvalidRequestError("Batch request length too long"),
			).Bytes()
		}

		responses := make([][]byte, len(batchReq))

		for i, req := range batchReq {
			responses[i], err = d.handleSingleWs(req, conn).Bytes()
			if err != nil {
				return nil, err
			}
		}

		var buf bytes.Buffer

		// batch output should look like:
		// [ { "requestId": "1", "status": 200 }, { "requestId": "2", "status": 200 } ]
		buf.WriteByte(openSquareBracket)                // [
		buf.Write(bytes.Join(responses, []byte{comma})) // join responses with the comma separator
		buf.WriteByte(closeSquareBracket)               // ]

		return buf.Bytes(), nil
	}

	var req Request
	if err := json.Unmarshal(reqBody, &req); err != nil {
		return NewRPCResponse(req.ID, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
	}

	return d.handleSingleWs(req, conn).Bytes()
}

func (d *Dispatcher) handleSingleWs(req Request, conn wsConn) Response {
	id, err := formatID(req.ID)
	if err != nil {
		return NewRPCResponse(nil, "2.0", nil, err)
	}

	var response []byte

	switch req.Method {
	case "eth_subscribe":
		var filterID string

		// if the request method is eth_subscribe we need to create a new filter with ws connection
		if filterID, err = d.handleSubscribe(req, conn); err == nil {
			response = []byte(fmt.Sprintf("\"%s\"", filterID))
		}
	case "eth_unsubscribe":
		var ok bool

		if ok, err = d.handleUnsubscribe(req); err == nil {
			response = []byte(strconv.FormatBool(ok))
		}
	default:
		// its a normal query that we handle with the dispatcher
		response, err = d.handleReq(req)
	}

	return NewRPCResponse(id, "2.0", response, err)
}

func (d *Dispatcher) Handle(reqBody []byte) ([]byte, error) {
	x := bytes.TrimLeft(reqBody, " \t\r\n")
	if len(x) == 0 {
		return NewRPCResponse(nil, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
	}

	if x[0] == '{' {
		var req Request
		if err := json.Unmarshal(reqBody, &req); err != nil {
			return NewRPCResponse(nil, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
		}

		if req.Method == "" {
			return NewRPCResponse(req.ID, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
		}

		// Debug logging for JSON-RPC request
		d.logger.Info("JSON-RPC request parsed",
			"method", req.Method,
			"id", req.ID,
			"params_raw", string(req.Params),
			"params_length", len(req.Params),
			"reqBody", string(reqBody))

		resp, err := d.handleReq(req)

		return NewRPCResponse(req.ID, "2.0", resp, err).Bytes()
	}

	// handle batch requests
	var requests BatchRequest
	if err := json.Unmarshal(reqBody, &requests); err != nil {
		return NewRPCResponse(
			nil,
			"2.0",
			nil,
			NewInvalidRequestError("Invalid json request"),
		).Bytes()
	}

	// if not disabled, avoid handling long batch requests
	if d.params.isExceedingBatchLengthLimit(uint64(len(requests))) {
		return NewRPCResponse(
			nil,
			"2.0",
			nil,
			NewInvalidRequestError("Batch request length too long"),
		).Bytes()
	}

	responses := make([]Response, 0)

	for _, req := range requests {
		var response, err = d.handleReq(req)
		if err != nil {
			errorResponse := NewRPCResponse(req.ID, "2.0", response, err)
			responses = append(responses, errorResponse)

			continue
		}

		resp := NewRPCResponse(req.ID, "2.0", response, nil)
		responses = append(responses, resp)
	}

	respBytes, err := json.Marshal(responses)
	if err != nil {
		return NewRPCResponse(nil, "2.0", nil, NewInternalError("Internal error")).Bytes()
	}

	return respBytes, nil
}

func (d *Dispatcher) handleReq(req Request) ([]byte, Error) {
	d.logger.Info("request received", "method", req.Method, "id", req.ID, "params_raw", string(req.Params), "params_length", len(req.Params))

	service, fd, ferr := d.getFnHandler(req)
	if ferr != nil {
		return nil, ferr
	}

	inArgs := make([]reflect.Value, fd.inNum)
	inArgs[0] = service.sv

	// Info logging to understand the method signature
	d.logger.Info("method signature analysis",
		"method", req.Method,
		"fd.inNum", fd.inNum,
		"fd.numParams()", fd.numParams(),
		"fd.reqt", fd.reqt)

	// Handle parameters based on method signature
	if fd.numParams() > 0 {
		// Check if the last parameter is interface{} type
		if fd.reqt[fd.inNum-1] == reflect.TypeOf((*interface{})(nil)).Elem() {
			// For interface{} parameters, pass the raw params directly
			d.logger.Info("entering interface{} parameter handling",
				"method", req.Method,
				"req.Params", string(req.Params),
				"req.Params length", len(req.Params))

			var paramValue interface{}
			if err := json.Unmarshal(req.Params, &paramValue); err != nil {
				d.logger.Error("failed to unmarshal params", "error", err, "params", string(req.Params))
				return nil, NewInvalidParamsError("Invalid Params")
			}

			d.logger.Info("successfully unmarshaled params",
				"method", req.Method,
				"paramValue", paramValue,
				"paramValue type", fmt.Sprintf("%T", paramValue))

			// Check if paramValue is nil or contains nil values and return error if so
			if paramValue == nil {
				d.logger.Error("params is nil, cannot proceed",
					"method", req.Method,
					"params", string(req.Params))
				return nil, NewInvalidParamsError("Params cannot be null")
			}

			// Check if paramValue is an array containing nil values
			if arr, ok := paramValue.([]interface{}); ok {
				for i, v := range arr {
					if v == nil {
						d.logger.Error("params array contains nil value",
							"method", req.Method,
							"index", i,
							"params", string(req.Params))
						return nil, NewInvalidParamsError(fmt.Sprintf("Param at index %d cannot be null", i))
					}
				}
			}

			// Set context.Context as the second parameter
			inArgs[1] = reflect.ValueOf(context.Background())

			// Set params as the third parameter
			inArgs[2] = reflect.ValueOf(paramValue)

			d.logger.Info("interface{} parameter handling completed",
				"method", req.Method,
				"inArgs[1] (context)", inArgs[1].Interface(),
				"inArgs[2] (params)", inArgs[2].Interface())
		} else {
			// For multiple parameters, use the original logic
			inputs := make([]interface{}, fd.numParams())

			for i := 0; i < fd.inNum-1; i++ {
				val := reflect.New(fd.reqt[i+1])
				inputs[i] = val.Interface()
				inArgs[i+1] = val.Elem()
			}

			// Unmarshal parameters
			if err := json.Unmarshal(req.Params, &inputs); err != nil {
				return nil, NewInvalidParamsError("Invalid Params")
			}
		}
	}

	var (
		data []byte
		err  error
		ok   bool
	)

	start := time.Now().UTC()
	output := fd.fv.Call(inArgs) // call rpc endpoint function
	// measure execution time of rpc endpoint function
	metrics.SetGauge([]string{jsonRPCMetric, req.Method + "_time"}, float32(time.Now().UTC().Sub(start).Seconds()))

	if err := getError(output[1]); err != nil {
		// measure error on the rpc endpoint function
		metrics.IncrCounter([]string{jsonRPCMetric, req.Method + "_errors"}, 1)
		d.logInternalError(req.Method, err)

		if res := output[0].Interface(); res != nil {
			data, ok = res.([]byte)

			if !ok {
				return nil, NewInternalError(err.Error())
			}
		}

		return data, NewInvalidRequestError(err.Error())
	}

	if res := output[0].Interface(); res != nil {
		data, err = json.Marshal(res)
		if err != nil {
			d.logInternalError(req.Method, err)

			return nil, NewInternalError("Internal error")
		}
	}

	return data, nil
}

func (d *Dispatcher) logInternalError(method string, err error) {
	d.logger.Warn("failed to dispatch", "method", method, "err", err)
}

func (d *Dispatcher) registerService(serviceName string, service interface{}) error {
	if d.serviceMap == nil {
		d.serviceMap = map[string]*serviceData{}
	}

	if serviceName == "" {
		return errors.New("jsonrpc: serviceName cannot be empty")
	}

	st := reflect.TypeOf(service)
	if st.Kind() == reflect.Struct {
		return errors.New(fmt.Sprintf("jsonrpc: service '%s' must be a pointer to struct", serviceName))
	}

	funcMap := make(map[string]*funcData)

	for i := 0; i < st.NumMethod(); i++ {
		mv := st.Method(i)
		if mv.PkgPath != "" {
			// skip unexported methods
			continue
		}

		name := lowerCaseFirst(mv.Name)
		funcName := serviceName + "_" + name
		fd := &funcData{
			fv: mv.Func,
		}

		var err error

		if fd.inNum, fd.reqt, err = validateFunc(funcName, fd.fv, true); err != nil {
			return fmt.Errorf("jsonrpc: %w", err)
		}
		// check if last item is a pointer
		if fd.numParams() != 0 {
			last := fd.reqt[fd.numParams()]
			if last.Kind() == reflect.Ptr {
				fd.isDyn = true
			}
		}

		funcMap[name] = fd
	}

	d.serviceMap[serviceName] = &serviceData{
		sv:      reflect.ValueOf(service),
		funcMap: funcMap,
	}

	return nil
}

func validateFunc(funcName string, fv reflect.Value, _ bool) (inNum int, reqt []reflect.Type, err error) {
	if funcName == "" {
		err = fmt.Errorf("funcName cannot be empty")

		return
	}

	ft := fv.Type()
	if ft.Kind() != reflect.Func {
		err = fmt.Errorf("function '%s' must be a function instead of %s", funcName, ft)

		return
	}

	inNum = ft.NumIn()

	if outNum := ft.NumOut(); ft.NumOut() != 2 {
		err = fmt.Errorf("unexpected number of output arguments in the function '%s': %d. Expected 2", funcName, outNum)

		return
	}

	if !isErrorType(ft.Out(1)) {
		err = fmt.Errorf(
			"unexpected type for the second return value of the function '%s': '%s'. Expected '%s'",
			funcName,
			ft.Out(1),
			errt,
		)

		return
	}

	reqt = make([]reflect.Type, inNum)
	for i := 0; i < inNum; i++ {
		reqt[i] = ft.In(i)
	}

	return
}

var errt = reflect.TypeOf((*error)(nil)).Elem()

func isErrorType(t reflect.Type) bool {
	return t.Implements(errt)
}

func getError(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}

	extractedErr, ok := v.Interface().(error)
	if !ok {
		return errors.New("invalid type assertion, unable to extract error")
	}

	return extractedErr
}

func lowerCaseFirst(str string) string {
	for i, v := range str {
		return string(unicode.ToLower(v)) + str[i+1:]
	}

	return ""
}

// dposStoreAdapter adapts JSONRPCStore to dposStore
type dposStoreAdapter struct {
	store JSONRPCStore
}

func (a *dposStoreAdapter) GetAccount(root types.Hash, addr types.Address) (*Account, error) {
	// Use ethStore methods from JSONRPCStore
	if ethStore, ok := a.store.(ethStore); ok {
		return ethStore.GetAccount(root, addr)
	}
	return nil, fmt.Errorf("ethStore not available")
}

func (a *dposStoreAdapter) GetBalance(root types.Hash, addr types.Address) (*big.Int, error) {
	// Use ethStore methods from JSONRPCStore
	if ethStore, ok := a.store.(ethStore); ok {
		// If root is zero hash, try to get from latest header first
		if root == (types.Hash{}) {
			// Try to get the latest header to get a valid state root
			if blockchainStore, ok := a.store.(ethBlockchainStore); ok {
				if latestHeader := blockchainStore.Header(); latestHeader != nil {
					// Use the latest header's state root
					root = latestHeader.StateRoot
				}
			}
		}

		account, err := ethStore.GetAccount(root, addr)
		if err != nil {
			return nil, err
		}
		return account.Balance, nil
	}
	return nil, fmt.Errorf("ethStore not available")
}

// AddTx adds a new transaction to the transaction pool
func (a *dposStoreAdapter) AddTx(tx *types.Transaction) error {
	// Try to access AddTx through the underlying JSONRPCStore
	if addTxStore, ok := a.store.(interface {
		AddTx(tx *types.Transaction) error
	}); ok {
		return addTxStore.AddTx(tx)
	}
	return fmt.Errorf("AddTx method not available on underlying store")
}

// GetPendingTx gets the pending transaction from the transaction pool
func (a *dposStoreAdapter) GetPendingTx(txHash types.Hash) (*types.Transaction, bool) {
	// Try to access GetPendingTx through the underlying JSONRPCStore
	if getPendingTxStore, ok := a.store.(interface {
		GetPendingTx(txHash types.Hash) (*types.Transaction, bool)
	}); ok {
		return getPendingTxStore.GetPendingTx(txHash)
	}
	return nil, false
}

// GetNonce returns the next nonce for this address
func (a *dposStoreAdapter) GetNonce(addr types.Address) uint64 {
	// Try to access GetNonce through the underlying JSONRPCStore
	if nonceStore, ok := a.store.(interface {
		GetNonce(addr types.Address) uint64
	}); ok {
		return nonceStore.GetNonce(addr)
	}
	return 0
}

// GetBaseFee returns the current base fee of TxPool
func (a *dposStoreAdapter) GetBaseFee() uint64 {
	// Try to access GetBaseFee through the underlying JSONRPCStore
	if baseFeeStore, ok := a.store.(interface {
		GetBaseFee() uint64
	}); ok {
		return baseFeeStore.GetBaseFee()
	}
	return 0
}

// GetConsensus gets the consensus engine from the underlying store
func (a *dposStoreAdapter) GetConsensus() interface{} {
	// Prefer strongly-typed hub getter if available
	if hub, ok := a.store.(interface {
		GetConsensus() consensus.Consensus // Changed interface{} to consensus.Consensus
	}); ok {
		return hub.GetConsensus()
	}
	// Fallback to generic interface{} if specific type not found
	if consensusStore, ok := a.store.(interface {
		GetConsensus() interface{}
	}); ok {
		return consensusStore.GetConsensus()
	}
	return nil
}

// GetNetwork gets the network layer from the underlying store
func (a *dposStoreAdapter) GetNetwork() interface{} {
	if networkStore, ok := a.store.(interface {
		GetNetwork() interface{}
	}); ok {
		return networkStore.GetNetwork()
	}
	return nil
}

// GetServer gets the server instance from the underlying store
func (a *dposStoreAdapter) GetServer() interface{} {
	if serverStore, ok := a.store.(interface {
		GetServer() interface{}
	}); ok {
		return serverStore.GetServer()
	}
	return nil
}

// GetTxPool gets the transaction pool from the underlying store
func (a *dposStoreAdapter) GetTxPool() interface{} {
	if txpoolStore, ok := a.store.(interface {
		GetTxPool() interface{}
	}); ok {
		return txpoolStore.GetTxPool()
	}
	return nil
}

// ReadTxLookup returns the block hash using the transaction hash
func (a *dposStoreAdapter) ReadTxLookup(hash types.Hash) (types.Hash, bool) {
	// Try to access ReadTxLookup through the underlying store
	if blockchainStore, ok := a.store.(interface {
		ReadTxLookup(hash types.Hash) (types.Hash, bool)
	}); ok {
		return blockchainStore.ReadTxLookup(hash)
	}
	return types.ZeroHash, false
}

// GetBlockByHash gets a block using the provided hash
func (a *dposStoreAdapter) GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool) {
	// Try to access GetBlockByHash through the underlying store
	if blockchainStore, ok := a.store.(interface {
		GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)
	}); ok {
		return blockchainStore.GetBlockByHash(hash, full)
	}
	return nil, false
}

func (a *dposStoreAdapter) GetDPoSState() (*consensusdpos.State, error) {
	// Try to get DPoS state from the consensus engine
	// This should connect to the actual DPoS consensus mechanism

	// First, try to get from blockchain store if it has DPoS state
	if blockchainStore, ok := a.store.(interface {
		GetDPoSState() (*consensusdpos.State, error)
	}); ok {
		return blockchainStore.GetDPoSState()
	}

	// If blockchain store doesn't have DPoS state, try to get from consensus store
	if consensusStore, ok := a.store.(interface {
		GetConsensusDPoSState() (*consensusdpos.State, error)
	}); ok {
		return consensusStore.GetConsensusDPoSState()
	}

	// If no DPoS state store is available, return nil
	// This allows the endpoint to work even when the consensus engine is not fully configured
	return nil, nil
}

func (a *dposStoreAdapter) GetValidators() (validator.AccountSet, error) {
	// Try to get validators from the consensus engine
	// This should connect to the actual DPoS consensus mechanism

	// Try to access consensus engine directly through embedded field using reflection
	if hub, ok := a.store.(interface {
		GetConsensus() consensus.Consensus
	}); ok {
		consensusEngine := hub.GetConsensus()

		// Try to get validators from consensus engine
		if dposEngine, ok := consensusEngine.(interface {
			GetDelegates() (validator.AccountSet, error)
		}); ok {
			return dposEngine.GetDelegates()
		}

		if dposEngine, ok := consensusEngine.(interface {
			GetValidators() (validator.AccountSet, error)
		}); ok {
			return dposEngine.GetValidators()
		}
	}

	// Try to access consensus engine directly through embedded field using reflection
	storeValue := reflect.ValueOf(a.store)
	if storeValue.Kind() == reflect.Ptr {
		storeValue = storeValue.Elem()
	}

	// Look for Consensus field
	if consensusField := storeValue.FieldByName("Consensus"); consensusField.IsValid() {
		consensusEngine := consensusField.Interface()

		// Try to get validators from consensus engine
		if dposEngine, ok := consensusEngine.(interface {
			GetDelegates() (validator.AccountSet, error)
		}); ok {
			fmt.Printf("DEBUG: Found GetDelegates method in consensus engine\n")
			return dposEngine.GetDelegates()
		}

		if dposEngine, ok := consensusEngine.(interface {
			GetValidators() (validator.AccountSet, error)
		}); ok {
			fmt.Printf("DEBUG: Found GetValidators method in consensus engine\n")
			return dposEngine.GetValidators()
		}

		// Try to access the delegates field directly if it exists
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		if delegatesField := consensusValue.FieldByName("delegates"); delegatesField.IsValid() {
			// Check if it's a slice
			if delegatesField.Kind() == reflect.Slice {
				fmt.Printf("DEBUG: Found delegates field, length: %d\n", delegatesField.Len())

				// Create a new AccountSet to hold the real data
				accountSet := make(validator.AccountSet, 0, delegatesField.Len())

				// Iterate through the slice elements
				for i := 0; i < delegatesField.Len(); i++ {
					element := delegatesField.Index(i)

					// Handle pointer type - get the value it points to
					var elementValue reflect.Value
					if element.Kind() == reflect.Ptr {
						elementValue = element.Elem()
					} else {
						elementValue = element
					}

					// Create a new ValidatorMetadata
					validatorMeta := &validator.ValidatorMetadata{}

					// Try to access Address field
					if addressField := elementValue.FieldByName("Address"); addressField.IsValid() {
						// Try to get the value directly without Interface()
						if addressField.CanAddr() {
							// Try to get the value using reflect methods
							addrValue := addressField
							if addrValue.Kind() == reflect.Array {
								// Handle array type (Address is usually [20]byte)
								addrBytes := make([]byte, addrValue.Len())
								for j := 0; j < addrValue.Len(); j++ {
									addrBytes[j] = byte(addrValue.Index(j).Uint())
								}
								addr := types.BytesToAddress(addrBytes)
								validatorMeta.Address = addr
							}
						}
					}

					// Try to access VotingPower field
					if votingPowerField := elementValue.FieldByName("VotingPower"); votingPowerField.IsValid() {
						// Try to get the value directly
						if votingPowerField.CanAddr() {
							if votingPowerField.Kind() == reflect.Ptr && !votingPowerField.IsNil() {
								// Dereference the pointer
								vpValue := votingPowerField.Elem()
								// Voting power processing - simplified logging
								if vpValue.Kind() == reflect.Struct {
									// This is a big.Int struct, we need to access its internal fields
									// Try to access the internal 'abs' field of big.Int
									if absField := vpValue.FieldByName("abs"); absField.IsValid() {
										if absField.Kind() == reflect.Slice {
											// Convert the abs slice to big.Int
											absBytes := make([]byte, absField.Len())
											for j := 0; j < absField.Len(); j++ {
												absBytes[j] = byte(absField.Index(j).Uint())
											}
											vp := new(big.Int).SetBytes(absBytes)
											validatorMeta.VotingPower = vp
										}
									}
								} else if vpValue.Kind() == reflect.Uint64 || vpValue.Kind() == reflect.Int64 {
									// Convert to big.Int
									vp := new(big.Int).SetUint64(vpValue.Uint())
									validatorMeta.VotingPower = vp
								}
							}
						}
					}

					// Try to access IsActive field
					if isActiveField := elementValue.FieldByName("IsActive"); isActiveField.IsValid() {
						if isActiveField.CanInterface() {
							if isActive, ok := isActiveField.Interface().(bool); ok {
								validatorMeta.IsActive = isActive
							}
						}
					}

					// Add the validator to the account set
					accountSet = append(accountSet, validatorMeta)
				}

				// 循环完成后，返回完整的 AccountSet
				if len(accountSet) > 0 {
					return accountSet, nil
				}
			}
		}
	}

	// If no validators found, return empty set
	return validator.AccountSet{}, nil
}

func (a *dposStoreAdapter) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	// Try to get validators from the consensus engine with filtering control
	// This should connect to the actual DPoS consensus mechanism

	// Try to access consensus engine directly through embedded field using reflection
	if hub, ok := a.store.(interface {
		GetConsensus() consensus.Consensus
	}); ok {
		consensusEngine := hub.GetConsensus()

		// Try to get validators from consensus engine with filtering control
		if dposEngine, ok := consensusEngine.(interface {
			GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error)
		}); ok {
			fmt.Printf("DEBUG: Calling GetValidatorsWithFilter on consensus engine with filterZeroVotingPower=%v\n", filterZeroVotingPower)
			return dposEngine.GetValidatorsWithFilter(filterZeroVotingPower)
		}

		// Fallback to regular GetValidators if GetValidatorsWithFilter is not available
		if dposEngine, ok := consensusEngine.(interface {
			GetDelegates() (validator.AccountSet, error)
		}); ok {
			return dposEngine.GetDelegates()
		}

		if dposEngine, ok := consensusEngine.(interface {
			GetValidators() (validator.AccountSet, error)
		}); ok {
			return dposEngine.GetValidators()
		}
	}

	// Try to access consensus engine directly through embedded field using reflection
	storeValue := reflect.ValueOf(a.store)
	if storeValue.Kind() == reflect.Ptr {
		storeValue = storeValue.Elem()
	}

	// Look for Consensus field
	if consensusField := storeValue.FieldByName("Consensus"); consensusField.IsValid() {
		consensusEngine := consensusField.Interface()

		// Try to get validators from consensus engine with filtering control
		if dposEngine, ok := consensusEngine.(interface {
			GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error)
		}); ok {
			return dposEngine.GetValidatorsWithFilter(filterZeroVotingPower)
		}

		// Fallback to regular methods
		if dposEngine, ok := consensusEngine.(interface {
			GetDelegates() (validator.AccountSet, error)
		}); ok {
			fmt.Printf("DEBUG: Found GetDelegates method in consensus engine\n")
			return dposEngine.GetDelegates()
		}

		if dposEngine, ok := consensusEngine.(interface {
			GetValidators() (validator.AccountSet, error)
		}); ok {
			fmt.Printf("DEBUG: Found GetValidators method in consensus engine\n")
			return dposEngine.GetValidators()
		}

		// Try to access the delegates field directly if it exists
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		if delegatesField := consensusValue.FieldByName("delegates"); delegatesField.IsValid() {
			// Check if it's a slice
			if delegatesField.Kind() == reflect.Slice {
				fmt.Printf("DEBUG: Found delegates field, length: %d\n", delegatesField.Len())

				// Create a new AccountSet to hold the real data
				accountSet := make(validator.AccountSet, 0, delegatesField.Len())

				// Iterate through the slice elements
				for i := 0; i < delegatesField.Len(); i++ {
					element := delegatesField.Index(i)

					// Handle pointer type - get the value it points to
					var elementValue reflect.Value
					if element.Kind() == reflect.Ptr {
						elementValue = element.Elem()
					} else {
						elementValue = element
					}

					// Create a new ValidatorMetadata
					validatorMeta := &validator.ValidatorMetadata{}

					// Try to access Address field
					if addressField := elementValue.FieldByName("Address"); addressField.IsValid() {
						// Try to get the value directly without Interface()
						if addressField.CanAddr() {
							// Try to get the value using reflect methods
							addrValue := addressField
							if addrValue.Kind() == reflect.Array {
								// Handle array type (Address is usually [20]byte)
								addrBytes := make([]byte, addrValue.Len())
								for j := 0; j < addrValue.Len(); j++ {
									addrBytes[j] = byte(addrValue.Index(j).Uint())
								}
								addr := types.BytesToAddress(addrBytes)
								validatorMeta.Address = addr
							}
						}
					}

					// Try to access VotingPower field
					if votingPowerField := elementValue.FieldByName("VotingPower"); votingPowerField.IsValid() {
						// Try to get the value directly
						if votingPowerField.CanAddr() {
							if votingPowerField.Kind() == reflect.Ptr && !votingPowerField.IsNil() {
								// Dereference the pointer
								vpValue := votingPowerField.Elem()
								// Voting power processing - simplified logging
								if vpValue.Kind() == reflect.Struct {
									// This is a big.Int struct, we need to access its internal fields
									// Try to access the internal 'abs' field of big.Int
									if absField := vpValue.FieldByName("abs"); absField.IsValid() {
										if absField.Kind() == reflect.Slice {
											// Convert the abs slice to big.Int
											absBytes := make([]byte, absField.Len())
											for j := 0; j < absField.Len(); j++ {
												absBytes[j] = byte(absField.Index(j).Uint())
											}
											vp := new(big.Int).SetBytes(absBytes)
											validatorMeta.VotingPower = vp
										}
									}
								} else if vpValue.Kind() == reflect.Uint64 || vpValue.Kind() == reflect.Int64 {
									// Convert to big.Int
									vp := new(big.Int).SetUint64(vpValue.Uint())
									validatorMeta.VotingPower = vp
								}
							}
						}
					}

					// Try to access IsActive field
					if isActiveField := elementValue.FieldByName("IsActive"); isActiveField.IsValid() {
						if isActiveField.CanInterface() {
							if isActive, ok := isActiveField.Interface().(bool); ok {
								validatorMeta.IsActive = isActive
							}
						}
					}

					// Apply filtering based on the parameter
					if filterZeroVotingPower && validatorMeta.VotingPower != nil && validatorMeta.VotingPower.Cmp(big.NewInt(0)) <= 0 {
						fmt.Printf("DEBUG: Filtering out validator with zero voting power: %s\n", validatorMeta.Address.String())
						continue
					}

					// Add the validator to the account set
					accountSet = append(accountSet, validatorMeta)
				}

				// 循环完成后，返回完整的 AccountSet
				if len(accountSet) > 0 {
					return accountSet, nil
				}
			}
		}
	}

	// If no validators found, return empty set
	return validator.AccountSet{}, nil
}

func (a *dposStoreAdapter) GetStakingInfo() ([]*consensusdpos.StakeInfo, error) {
	// Try to get staking info from the consensus engine
	// This should connect to the actual DPoS staking mechanism

	// First, try to get from blockchain store if it has staking information
	if blockchainStore, ok := a.store.(interface {
		GetStakingInfo() ([]*consensusdpos.StakeInfo, error)
	}); ok {
		return blockchainStore.GetStakingInfo()
	}

	// Try to get from consensus store with GetStakingInfo method (different signature)
	if consensusStore, ok := a.store.(interface {
		GetStakingInfo(blockNumber uint64, staker types.Address) (*consensusdpos.StakeInfo, error)
	}); ok {
		// For now, get staking info for zero address (all stakers)
		// TODO: Implement proper aggregation of all staking info
		stakeInfo, err := consensusStore.GetStakingInfo(0, types.ZeroAddress)
		if err == nil && stakeInfo != nil {
			return []*consensusdpos.StakeInfo{stakeInfo}, nil
		}
	}

	// Try to get from consensus store with GetConsensusStakingInfo method
	if consensusStore, ok := a.store.(interface {
		GetConsensusStakingInfo() ([]*consensusdpos.StakeInfo, error)
	}); ok {
		return consensusStore.GetConsensusStakingInfo()
	}

	// Try to access consensus engine directly through embedded field using reflection
	storeValue := reflect.ValueOf(a.store)
	if storeValue.Kind() == reflect.Ptr {
		storeValue = storeValue.Elem()
	}

	// Look for Consensus field
	if consensusField := storeValue.FieldByName("Consensus"); consensusField.IsValid() {
		consensusEngine := consensusField.Interface()

		// Try to get staking info from consensus engine using reflection
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		// Look for GetStakingInfo method on the original consensus engine (pointer type)
		originalConsensus := consensusField.Interface()
		originalType := reflect.TypeOf(originalConsensus)

		if getStakingInfoMethod, found := originalType.MethodByName("GetStakingInfo"); found {
			// Get validators first to know which addresses to query
			validators, err := a.GetValidators()
			if err != nil {
				return []*consensusdpos.StakeInfo{}, nil
			}

			// Create staking info for each validator
			stakingInfos := make([]*consensusdpos.StakeInfo, 0, len(validators))
			for i, validator := range validators {
				// Call GetStakingInfo for this validator using the original consensus engine
				// Note: GetStakingInfo is a method on *DPoS, so we need to call it on the pointer
				blockNumber := reflect.ValueOf(uint64(0))
				stakerAddr := reflect.ValueOf(validator.Address)

				results := getStakingInfoMethod.Func.Call([]reflect.Value{
					reflect.ValueOf(originalConsensus), // receiver (the *DPoS instance)
					blockNumber,                        // blockNumber parameter
					stakerAddr,                         // stakerAddr parameter
				})

				fmt.Printf("DEBUG: GetStakingInfo - Method call returned %d results\n", len(results))
				for j, result := range results {
					fmt.Printf("DEBUG: GetStakingInfo - Result %d: type=%T, value=%v, isNil=%v\n", j, result.Interface(), result.Interface(), result.IsNil())
				}

				if len(results) == 2 && !results[1].IsNil() {
					err := results[1].Interface().(error)
					fmt.Printf("DEBUG: GetStakingInfo - Error for validator %d: %v\n", i, err)
					continue
				}

				if len(results) >= 1 && !results[0].IsNil() {
					stakeInfo := results[0].Interface().(*consensusdpos.StakeInfo)
					fmt.Printf("DEBUG: GetStakingInfo - SUCCESS! Got staking info for validator %d:\n", i)
					fmt.Printf("DEBUG: GetStakingInfo -   Staker: %s\n", stakeInfo.Staker.String())
					fmt.Printf("DEBUG: GetStakingInfo -   Amount: %s\n", stakeInfo.Amount.String())
					fmt.Printf("DEBUG: GetStakingInfo -   StartTime: %d\n", stakeInfo.StartTime)
					fmt.Printf("DEBUG: GetStakingInfo -   EndTime: %d\n", stakeInfo.EndTime)
					fmt.Printf("DEBUG: GetStakingInfo -   IsLocked: %v\n", stakeInfo.IsLocked)
					fmt.Printf("DEBUG: GetStakingInfo -   IsActive: %v\n", stakeInfo.IsActive)
					fmt.Printf("DEBUG: GetStakingInfo -   Rewards: %s\n", stakeInfo.Rewards.String())
					fmt.Printf("DEBUG: GetStakingInfo -   Delegate: %s\n", stakeInfo.Delegate.String())

					stakingInfos = append(stakingInfos, stakeInfo)
					fmt.Printf("DEBUG: GetStakingInfo - Added staking info for validator %d\n", i)
				} else {
					fmt.Printf("DEBUG: GetStakingInfo - No staking info for validator %d (result is nil or empty)\n", i)
				}
			}

			if len(stakingInfos) > 0 {
				fmt.Printf("DEBUG: GetStakingInfo - Returning %d staking infos\n", len(stakingInfos))
				return stakingInfos, nil
			}
		} else {
			fmt.Printf("DEBUG: GetStakingInfo - GetStakingInfo method not found on original consensus engine\n")

			// Debug: List all available methods on the consensus engine
			fmt.Printf("DEBUG: GetStakingInfo - Available methods on consensus engine:\n")
			fmt.Printf("DEBUG: GetStakingInfo - consensusValue type: %T\n", consensusValue.Interface())
			fmt.Printf("DEBUG: GetStakingInfo - consensusValue kind: %v\n", consensusValue.Kind())
			fmt.Printf("DEBUG: GetStakingInfo - consensusValue can addr: %v\n", consensusValue.CanAddr())
			fmt.Printf("DEBUG: GetStakingInfo - consensusValue can interface: %v\n", consensusValue.CanInterface())

			consensusType := consensusValue.Type()
			fmt.Printf("DEBUG: GetStakingInfo - consensusType: %v\n", consensusType)
			fmt.Printf("DEBUG: GetStakingInfo - consensusType name: %s\n", consensusType.Name())
			fmt.Printf("DEBUG: GetStakingInfo - consensusType pkg path: %s\n", consensusType.PkgPath())
			fmt.Printf("DEBUG: GetStakingInfo - consensusType num methods: %d\n", consensusType.NumMethod())

			// Try to get methods using different approaches
			if consensusType.NumMethod() > 0 {
				for i := 0; i < consensusType.NumMethod(); i++ {
					method := consensusType.Method(i)
					fmt.Printf("DEBUG: GetStakingInfo - Method %d: %s\n", i, method.Name)
				}
			} else {
				fmt.Printf("DEBUG: GetStakingInfo - No methods found on consensusType\n")

				// Try to get methods from the interface
				if consensusInterface, ok := consensusValue.Interface().(interface{}); ok {
					fmt.Printf("DEBUG: GetStakingInfo - Got consensus interface\n")
					interfaceType := reflect.TypeOf(consensusInterface)
					fmt.Printf("DEBUG: GetStakingInfo - Interface type: %v\n", interfaceType)
					fmt.Printf("DEBUG: GetStakingInfo - Interface num methods: %d\n", interfaceType.NumMethod())

					for i := 0; i < interfaceType.NumMethod(); i++ {
						method := interfaceType.Method(i)
						fmt.Printf("DEBUG: GetStakingInfo - Interface Method %d: %s\n", i, method.Name)
					}
				}

				// Try to get methods from the original consensus engine
				originalConsensus := consensusField.Interface()
				fmt.Printf("DEBUG: GetStakingInfo - Original consensus type: %T\n", originalConsensus)
				originalType := reflect.TypeOf(originalConsensus)
				fmt.Printf("DEBUG: GetStakingInfo - Original type: %v\n", originalType)
				fmt.Printf("DEBUG: GetStakingInfo - Original type num methods: %d\n", originalType.NumMethod())

				if originalType.NumMethod() > 0 {
					for i := 0; i < originalType.NumMethod(); i++ {
						method := originalType.Method(i)
						fmt.Printf("DEBUG: GetStakingInfo - Original Method %d: %s\n", i, method.Name)
					}
				}
			}

			// Debug: Check if method exists with different case
			if getStakingInfoMethod := consensusValue.MethodByName("getStakingInfo"); getStakingInfoMethod.IsValid() {
				fmt.Printf("DEBUG: GetStakingInfo - Found getStakingInfo (lowercase) method\n")
			}
			if getStakingInfoMethod := consensusValue.MethodByName("GetStakingInfoWithTx"); getStakingInfoMethod.IsValid() {
				fmt.Printf("DEBUG: GetStakingInfo - Found GetStakingInfoWithTx method\n")
			}
			if getStakingInfoMethod := consensusValue.MethodByName("GetStakingInfoAtBlock"); getStakingInfoMethod.IsValid() {
				fmt.Printf("DEBUG: GetStakingInfo - Found GetStakingInfoAtBlock method\n")
			}

			// Try to access delegates field directly to get staking info
			if delegatesField := consensusValue.FieldByName("delegates"); delegatesField.IsValid() {
				fmt.Printf("DEBUG: GetStakingInfo - Found delegates field directly\n")

				// Check if delegates field is a slice/array
				if delegatesField.Kind() == reflect.Slice {
					fmt.Printf("DEBUG: GetStakingInfo - Delegates field is a slice with length: %d\n", delegatesField.Len())

					stakingInfos := make([]*consensusdpos.StakeInfo, 0, delegatesField.Len())

					// Iterate through delegates using reflection
					for i := 0; i < delegatesField.Len(); i++ {
						delegateElement := delegatesField.Index(i)
						fmt.Printf("DEBUG: GetStakingInfo - Processing delegate %d\n", i)

						// Check if delegateElement is a pointer and dereference it
						var delegateValue reflect.Value
						if delegateElement.Kind() == reflect.Ptr {
							delegateValue = delegateElement.Elem()
							fmt.Printf("DEBUG: GetStakingInfo - Delegate %d is pointer, dereferenced\n", i)
						} else {
							delegateValue = delegateElement
							fmt.Printf("DEBUG: GetStakingInfo - Delegate %d is not pointer\n", i)
						}

						// Safely access delegate fields using reflection on dereferenced value
						addressField := delegateValue.FieldByName("Address")
						if !addressField.IsValid() {
							fmt.Printf("DEBUG: GetStakingInfo - Address field not found for delegate %d\n", i)
							continue
						}

						// Convert address to types.Address safely
						var validatorAddr types.Address
						if addressField.CanInterface() {
							if addr, ok := addressField.Interface().(types.Address); ok {
								validatorAddr = addr
								fmt.Printf("DEBUG: GetStakingInfo - Delegate %d address: %s\n", i, validatorAddr.String())
							} else {
								fmt.Printf("DEBUG: GetStakingInfo - Failed to convert address for delegate %d\n", i)
								continue
							}
						} else {
							fmt.Printf("DEBUG: GetStakingInfo - Address field cannot interface for delegate %d\n", i)
							continue
						}

						// Get isActive field safely
						isActiveField := delegateValue.FieldByName("IsActive")
						isActive := false
						if isActiveField.IsValid() && isActiveField.CanInterface() {
							if active, ok := isActiveField.Interface().(bool); ok {
								isActive = active
							}
						}

						// Try to get real staking amount from genesis or other sources
						realStakeAmount := a.getRealStakeAmount(validatorAddr)
						fmt.Printf("DEBUG: GetStakingInfo - Real stake amount for %s: %s\n",
							validatorAddr.String(), realStakeAmount.String())

						// Create staking info from delegate with real stake amount
						stakeInfo := &consensusdpos.StakeInfo{
							Staker:    validatorAddr,
							Amount:    realStakeAmount,           // Use real stake amount instead of VotingPower
							StartTime: uint64(time.Now().Unix()), // Use current time as default
							EndTime:   0,                         // No end time
							IsLocked:  false,                     // Not locked
							IsActive:  isActive,
							Rewards:   big.NewInt(0), // No rewards yet
							Delegate:  validatorAddr, // Self-delegation
						}

						stakingInfos = append(stakingInfos, stakeInfo)
						fmt.Printf("DEBUG: GetStakingInfo - Created staking info for delegate %d: %s, real amount: %s\n",
							i, validatorAddr.String(), realStakeAmount.String())
					}

					if len(stakingInfos) > 0 {
						fmt.Printf("DEBUG: GetStakingInfo - Returning %d staking infos from delegates\n", len(stakingInfos))
						return stakingInfos, nil
					}
				} else {
					fmt.Printf("DEBUG: GetStakingInfo - Delegates field is not a slice, kind: %v\n", delegatesField.Kind())
				}
			}
		}
	}

	// If no staking store is available, return empty slice
	// This allows the endpoint to work even when the consensus engine is not fully configured
	fmt.Printf("DEBUG: GetStakingInfo - No staking info found, returning empty slice\n")
	return []*consensusdpos.StakeInfo{}, nil
}

// getRealStakeAmount tries to get the real staking amount for a validator
// This should return the actual ETH amount (e.g., 1000 ETH) instead of the voting power weight (e.g., 54)
func (a *dposStoreAdapter) getRealStakeAmount(validatorAddr types.Address) *big.Int {
	// Try to access consensus engine to get real staking info
	storeValue := reflect.ValueOf(a.store)
	if storeValue.Kind() == reflect.Ptr {
		storeValue = storeValue.Elem()
	}

	// Look for Consensus field
	if consensusField := storeValue.FieldByName("Consensus"); consensusField.IsValid() {
		consensusEngine := consensusField.Interface()
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		// Try to access config field to get initial delegates
		if configField := consensusValue.FieldByName("config"); configField.IsValid() {
			fmt.Printf("DEBUG: getRealStakeAmount - Found config field\n")

			// Check if config field is a pointer and get its value
			var configValue reflect.Value
			if configField.Kind() == reflect.Ptr {
				configValue = configField.Elem()
			} else {
				configValue = configField
			}

			// Look for InitialDelegates field
			if initialDelegatesField := configValue.FieldByName("InitialDelegates"); initialDelegatesField.IsValid() {
				fmt.Printf("DEBUG: getRealStakeAmount - Found InitialDelegates field\n")

				// Check if it's a slice
				if initialDelegatesField.Kind() == reflect.Slice {
					fmt.Printf("DEBUG: getRealStakeAmount - InitialDelegates is slice with length: %d\n", initialDelegatesField.Len())

					// Iterate through initial delegates using reflection
					for i := 0; i < initialDelegatesField.Len(); i++ {
						delegateElement := initialDelegatesField.Index(i)
						fmt.Printf("DEBUG: getRealStakeAmount - Processing initial delegate %d\n", i)

						// Check if delegateElement is a pointer and dereference it
						var delegateValue reflect.Value
						if delegateElement.Kind() == reflect.Ptr {
							delegateValue = delegateElement.Elem()
							fmt.Printf("DEBUG: getRealStakeAmount - Initial delegate %d is pointer, dereferenced\n", i)
						} else {
							delegateValue = delegateElement
							fmt.Printf("DEBUG: getRealStakeAmount - Initial delegate %d is not pointer\n", i)
						}

						// Get address field safely
						addressField := delegateValue.FieldByName("Address")
						if !addressField.IsValid() {
							fmt.Printf("DEBUG: getRealStakeAmount - Address field not found for initial delegate %d\n", i)
							continue
						}

						// Convert address safely
						var delegateAddr types.Address
						if addressField.CanInterface() {
							if addr, ok := addressField.Interface().(types.Address); ok {
								delegateAddr = addr
							} else {
								fmt.Printf("DEBUG: getRealStakeAmount - Failed to convert address for initial delegate %d\n", i)
								continue
							}
						} else {
							fmt.Printf("DEBUG: getRealStakeAmount - Address field cannot interface for initial delegate %d\n", i)
							continue
						}

						// Check if this is the validator we're looking for
						if delegateAddr == validatorAddr {
							fmt.Printf("DEBUG: getRealStakeAmount - Found validator %s at index %d\n", validatorAddr.String(), i)

							// Try to get stake amount from GenesisValidator
							if stakeField := delegateValue.FieldByName("Stake"); stakeField.IsValid() && stakeField.CanInterface() {
								if stakeAmount, ok := stakeField.Interface().(*big.Int); ok && stakeAmount != nil {
									fmt.Printf("DEBUG: getRealStakeAmount - Found stake amount: %s\n", stakeAmount.String())
									return stakeAmount
								}
							}

							// If no stake field, try to get from VotingPower
							if votingPowerField := delegateValue.FieldByName("VotingPower"); votingPowerField.IsValid() && votingPowerField.CanInterface() {
								if votingPower, ok := votingPowerField.Interface().(*big.Int); ok && votingPower != nil {
									fmt.Printf("DEBUG: getRealStakeAmount - Using VotingPower as stake: %s\n", votingPower.String())
									return votingPower
								}
							}

							// If still no amount, use default 1000 ETH
							defaultAmount, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 ETH in Wei
							fmt.Printf("DEBUG: getRealStakeAmount - Using default amount: %s\n", defaultAmount.String())
							return defaultAmount
						}
					}
				} else {
					fmt.Printf("DEBUG: getRealStakeAmount - InitialDelegates field is not a slice, kind: %v\n", initialDelegatesField.Kind())
				}
			}
		}

		// Try to access state field to get current staking info
		if stateField := consensusValue.FieldByName("state"); stateField.IsValid() {
			fmt.Printf("DEBUG: getRealStakeAmount - Found state field\n")
			// TODO: Implement state-based staking amount retrieval
		}
	}

	// If no real stake amount found, return default 1000 ETH
	defaultAmount, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 ETH in Wei
	fmt.Printf("DEBUG: getRealStakeAmount - No real stake amount found, using default: %s\n", defaultAmount.String())
	return defaultAmount
}
