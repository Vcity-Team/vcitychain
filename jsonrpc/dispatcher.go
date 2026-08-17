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

	// enabledAPIs restricts registered JSON-RPC namespaces (eth, net, web3, ...).
	// Empty / nil means DefaultJSONRPCAPIs.
	enabledAPIs map[string]struct{}
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
	enabled := d.params.enabledAPIs
	if len(enabled) == 0 {
		enabled = parseJSONRPCAPIList(DefaultJSONRPCAPIs)
	}

	apiEnabled := func(name string) bool {
		_, ok := enabled[name]
		return ok
	}

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
	d.endpoints.Debug = NewDebug(store, d.params.concurrentRequestsDebug, d.params.chainID)

	// Add DPOS endpoint with store adapter
	d.endpoints.DPOS = NewDPOS(d.logger, &dposStoreAdapter{store: store}, d.params.chainID)

	var err error

	if apiEnabled("eth") {
		if err = d.registerService("eth", d.endpoints.Eth); err != nil {
			return err
		}
	}

	if apiEnabled("net") {
		if err = d.registerService("net", d.endpoints.Net); err != nil {
			return err
		}
	}

	if apiEnabled("web3") {
		if err = d.registerService("web3", d.endpoints.Web3); err != nil {
			return err
		}
	}

	if apiEnabled("txpool") {
		if err = d.registerService("txpool", d.endpoints.TxPool); err != nil {
			return err
		}
	}

	if apiEnabled("bridge") {
		if err = d.registerService("bridge", d.endpoints.Bridge); err != nil {
			return err
		}
	}

	if apiEnabled("debug") {
		if err = d.registerService("debug", d.endpoints.Debug); err != nil {
			return err
		}
	}

	if apiEnabled("dpos") {
		return d.registerService("dpos", d.endpoints.DPOS)
	}

	return nil
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
	service, fd, ferr := d.getFnHandler(req)
	if ferr != nil {
		return nil, ferr
	}

	inArgs := make([]reflect.Value, fd.inNum)
	inArgs[0] = service.sv

	// Handle parameters based on method signature
	if fd.numParams() > 0 {
		// Check if the last parameter is interface{} type
		if fd.reqt[fd.inNum-1].Kind() == reflect.Interface {
			// For interface{} parameters, pass the raw params directly
			var paramValue interface{}
			if err := json.Unmarshal(req.Params, &paramValue); err != nil {
				plen, preview := paramsLogMeta(req.Params)
				d.logger.Error("failed to unmarshal params",
					"error", err,
					"method", req.Method,
					"params_len", plen,
					"params_preview", preview)
				return nil, NewInvalidParamsError("Invalid Params")
			}

			// Check if paramValue is nil or contains nil values and return error if so
			if paramValue == nil {
				plen, preview := paramsLogMeta(req.Params)
				d.logger.Error("params is nil, cannot proceed",
					"method", req.Method,
					"params_len", plen,
					"params_preview", preview)
				return nil, NewInvalidParamsError("Params cannot be null")
			}

			// Check if paramValue is an array containing nil values
			if arr, ok := paramValue.([]interface{}); ok {
				for i, v := range arr {
					if v == nil {
						plen, preview := paramsLogMeta(req.Params)
						d.logger.Error("params array contains nil value",
							"method", req.Method,
							"index", i,
							"params_len", plen,
							"params_preview", preview)
						return nil, NewInvalidParamsError(fmt.Sprintf("Param at index %d cannot be null", i))
					}
				}
			}

			// Set context.Context as the second parameter
			inArgs[1] = reflect.ValueOf(context.Background())

			// For methods with only 2 parameters (receiver + context), params go in context
			// For methods with 3 parameters (receiver + context + params), params go in index 2
			if fd.inNum == 3 {
				inArgs[2] = reflect.ValueOf(paramValue)
			}
		} else {
			// For multiple parameters, use the original logic
			inputs := make([]interface{}, fd.numParams())

			for i := 0; i < fd.inNum-1; i++ {
				val := reflect.New(fd.reqt[i+1])
				inputs[i] = val.Interface()
				inArgs[i+1] = val.Elem()
			}

			// Special handling for eth_estimateGas with optional second parameter
			if req.Method == "eth_estimateGas" && fd.numParams() == 2 {
				// Parse the raw params to check actual parameter count
				var rawParams []interface{}
				if err := json.Unmarshal(req.Params, &rawParams); err != nil {
					d.logger.Error("failed to unmarshal raw params", "error", err)
					return nil, NewInvalidParamsError("Invalid Params")
				}

				// If only one parameter provided, add default BlockNumber (latest)
				if len(rawParams) == 1 {
					// Create a default BlockNumber (latest) for the second parameter
					latestBlockNumber := "latest"
					rawParams = append(rawParams, latestBlockNumber)

					// Re-marshal the modified params
					modifiedParams, err := json.Marshal(rawParams)
					if err != nil {
						d.logger.Error("failed to marshal modified params", "error", err)
						return nil, NewInvalidParamsError("Invalid Params")
					}
					req.Params = modifiedParams
				}
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
				return nil, NewInternalError(sanitizeRPCErrorMessage(err.Error()))
			}
		}

		// Sanitize client-facing messages (may include hex keys from callers).
		return data, NewInternalError(sanitizeRPCErrorMessage(err.Error()))
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
	// d.logger.Warn("failed to dispatch", "method", method, "err", err)
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

// GetDPoSEngine gets the DPoS engine from the underlying store
func (a *dposStoreAdapter) GetDPoSEngine() interface{} {
	// Try to access GetDPoSEngine through the underlying store
	if dposStore, ok := a.store.(interface {
		GetDPoSEngine() interface{}
	}); ok {
		return dposStore.GetDPoSEngine()
	}
	return nil
}

func (a *dposStoreAdapter) GetDPoSState() (*consensusdpos.State, error) {
	// 通过 GetDPoSEngine 获取 DPoS 引擎，然后从引擎获取 State
	if dposEngine := a.GetDPoSEngine(); dposEngine != nil {
		if dpos, ok := dposEngine.(*consensusdpos.DPoS); ok {
			if state := dpos.GetState(); state != nil {
				return state, nil
			}
		}
	}

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
			fmt.Printf("DEBUG: [dposStoreAdapter.GetValidators] 使用 GetDelegates 方法\n")
			validators, err := dposEngine.GetDelegates()
			fmt.Printf("DEBUG: [dposStoreAdapter.GetValidators] GetDelegates 返回: count=%d, error=%v\n", len(validators), err)
			return validators, err
		}

		if dposEngine, ok := consensusEngine.(interface {
			GetValidators() validator.AccountSet
		}); ok {
			fmt.Printf("DEBUG: [dposStoreAdapter.GetValidators] 使用 GetValidators 方法\n")
			validators := dposEngine.GetValidators()
			fmt.Printf("DEBUG: [dposStoreAdapter.GetValidators] GetValidators 返回: count=%d\n", len(validators))
			return validators, nil
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
	fmt.Printf("DEBUG: GetStakingInfo - 开始调用\n")

	dposState, err := a.GetDPoSState()
	if err != nil {
		fmt.Printf("DEBUG: GetStakingInfo - GetDPoSState 返回错误: %v\n", err)
	} else if dposState == nil {
		fmt.Printf("DEBUG: GetStakingInfo - GetDPoSState 返回 nil，尝试直接通过 GetDPoSEngine 获取\n")
		// 如果 GetDPoSState 返回 nil，直接通过 GetDPoSEngine 获取 DPoS 引擎
		if dposEngine := a.GetDPoSEngine(); dposEngine != nil {
			fmt.Printf("DEBUG: GetStakingInfo - 成功获取 DPoS 引擎: %T\n", dposEngine)
			if dpos, ok := dposEngine.(*consensusdpos.DPoS); ok {
				if state := dpos.GetState(); state != nil && state.StakeStore != nil {
					fmt.Printf("DEBUG: GetStakingInfo - 从 DPoS 引擎获取 State 成功，调用 StakeStore.GetStakingInfo()\n")
					stakingInfos, err := state.StakeStore.GetStakingInfo()
					if err != nil {
						fmt.Printf("DEBUG: GetStakingInfo - Error from StakeStore.GetStakingInfo: %v\n", err)
					} else if len(stakingInfos) > 0 {
						fmt.Printf("DEBUG: GetStakingInfo - Successfully retrieved %d staking infos from StakeStore.GetStakingInfo\n", len(stakingInfos))
						return stakingInfos, nil
					} else {
						fmt.Printf("DEBUG: GetStakingInfo - StakeStore.GetStakingInfo returned empty slice\n")
					}
				} else {
					fmt.Printf("DEBUG: GetStakingInfo - DPoS 引擎的 GetState() 返回 nil 或 StakeStore 为 nil\n")
				}
			} else {
				fmt.Printf("DEBUG: GetStakingInfo - DPoS 引擎类型断言失败: %T\n", dposEngine)
			}
		} else {
			fmt.Printf("DEBUG: GetStakingInfo - GetDPoSEngine 返回 nil\n")
		}
	} else if dposState.StakeStore == nil {
		fmt.Printf("DEBUG: GetStakingInfo - dposState.StakeStore 为 nil\n")
	} else {
		fmt.Printf("DEBUG: GetStakingInfo - 调用 StakeStore.GetStakingInfo()\n")
		stakingInfos, err := dposState.StakeStore.GetStakingInfo()
		if err != nil {
			fmt.Printf("DEBUG: GetStakingInfo - Error from StakeStore.GetStakingInfo: %v\n", err)
		} else if len(stakingInfos) > 0 {
			fmt.Printf("DEBUG: GetStakingInfo - Successfully retrieved %d staking infos from StakeStore.GetStakingInfo\n", len(stakingInfos))
			return stakingInfos, nil
		} else {
			fmt.Printf("DEBUG: GetStakingInfo - StakeStore.GetStakingInfo returned empty slice\n")
		}
	}

	// If no staking store is available, return empty slice
	// This allows the endpoint to work even when the consensus engine is not fully configured
	fmt.Printf("DEBUG: GetStakingInfo - No staking info found, returning empty slice\n")
	return []*consensusdpos.StakeInfo{}, nil
}

func (a *dposStoreAdapter) GetHeaderByNumber(number uint64) (*types.Header, bool) {
	// Try to get header from blockchain store
	if blockchainStore, ok := a.store.(interface {
		GetHeaderByNumber(uint64) (*types.Header, bool)
	}); ok {
		return blockchainStore.GetHeaderByNumber(number)
	}

	// Fallback: try to use ethBlockchainStore and iterate
	if ethBC, ok := a.store.(ethBlockchainStore); ok {
		// Try to get header by number using HeaderByNumber method if available
		if headerStore, ok := ethBC.(interface {
			HeaderByNumber(uint64) (*types.Header, bool)
		}); ok {
			return headerStore.HeaderByNumber(number)
		}

		// Last resort: iterate from latest header
		latestHeader := ethBC.Header()
		if latestHeader != nil && number <= latestHeader.Number {
			currentHeader := latestHeader
			for currentHeader != nil && currentHeader.Number > number {
				parentHash := currentHeader.ParentHash
				parentBlock, ok := a.store.(ethBlockchainStore).GetBlockByHash(parentHash, false)
				if !ok || parentBlock == nil {
					break
				}
				currentHeader = parentBlock.Header
			}
			if currentHeader != nil && currentHeader.Number == number {
				return currentHeader, true
			}
		}
	}

	return nil, false
}
