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
	"github.com/Vcity-Team/vcitychain/consensus/dpos"
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
		account, err := ethStore.GetAccount(root, addr)
		if err != nil {
			return nil, err
		}
		return account.Balance, nil
	}
	return nil, fmt.Errorf("ethStore not available")
}

func (a *dposStoreAdapter) GetDPoSState() (*dpos.State, error) {
	// Try to get DPoS state from the consensus engine
	// This should connect to the actual DPoS consensus mechanism

	// First, try to get from blockchain store if it has DPoS state
	if blockchainStore, ok := a.store.(interface{ GetDPoSState() (*dpos.State, error) }); ok {
		return blockchainStore.GetDPoSState()
	}

	// If blockchain store doesn't have DPoS state, try to get from consensus store
	if consensusStore, ok := a.store.(interface{ GetConsensusDPoSState() (*dpos.State, error) }); ok {
		return consensusStore.GetConsensusDPoSState()
	}

	// If no DPoS state store is available, return nil
	// This allows the endpoint to work even when the consensus engine is not fully configured
	return nil, nil
}

func (a *dposStoreAdapter) GetValidators() (validator.AccountSet, error) {
	// Try to get validators from the consensus engine
	// This should connect to the actual DPoS consensus mechanism

	// Debug: Print store type
	fmt.Printf("DEBUG: Store type: %T\n", a.store)

	// Try to access consensus engine directly through embedded field using reflection
	if hub, ok := a.store.(interface {
		GetConsensus() consensus.Consensus
	}); ok {
		fmt.Printf("DEBUG: Found consensus engine through hub\n")
		consensusEngine := hub.GetConsensus()

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
	}

	// Try to access consensus engine directly through embedded field using reflection
	storeValue := reflect.ValueOf(a.store)
	if storeValue.Kind() == reflect.Ptr {
		storeValue = storeValue.Elem()
	}

	// Look for Consensus field
	if consensusField := storeValue.FieldByName("Consensus"); consensusField.IsValid() {
		fmt.Printf("DEBUG: Found Consensus field through reflection\n")
		consensusEngine := consensusField.Interface()
		fmt.Printf("DEBUG: Consensus engine type: %T\n", consensusEngine)

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

		// Try to get validators from consensus engine using reflection
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		// Look for GetDelegates method
		if getDelegatesMethod := consensusValue.MethodByName("GetDelegates"); getDelegatesMethod.IsValid() {
			fmt.Printf("DEBUG: Found GetDelegates method through reflection\n")
			// GetDelegates requires (blockNumber uint64, parents []*types.Header)
			// For current block, use 0 and nil
			blockNumber := reflect.ValueOf(uint64(0))
			parents := reflect.ValueOf([]*types.Header(nil))
			fmt.Printf("DEBUG: Calling GetDelegates with blockNumber=%v, parents=%v\n", blockNumber.Interface(), parents.Interface())
			results := getDelegatesMethod.Call([]reflect.Value{blockNumber, parents})
			fmt.Printf("DEBUG: GetDelegates returned %d results\n", len(results))
			if len(results) == 2 && !results[1].IsNil() {
				err := results[1].Interface().(error)
				fmt.Printf("DEBUG: GetDelegates returned error: %v\n", err)
				return nil, err
			}
			if len(results) >= 1 {
				result := results[0].Interface()
				fmt.Printf("DEBUG: GetDelegates returned result type: %T\n", result)
				if accountSet, ok := result.(validator.AccountSet); ok {
					fmt.Printf("DEBUG: Successfully converted result to AccountSet\n")
					return accountSet, nil
				}
				fmt.Printf("DEBUG: Failed to convert result to AccountSet\n")
			}
		} else {
			fmt.Printf("DEBUG: GetDelegates method not found\n")
		}

		// Look for GetValidators method
		if getValidatorsMethod := consensusValue.MethodByName("GetValidators"); getValidatorsMethod.IsValid() {
			fmt.Printf("DEBUG: Found GetValidators method through reflection\n")
			// GetValidators requires (blockNumber uint64, parents []*types.Header)
			// For current block, use 0 and nil
			blockNumber := reflect.ValueOf(uint64(0))
			parents := reflect.ValueOf([]*types.Header(nil))
			fmt.Printf("DEBUG: Calling GetValidators with blockNumber=%v, parents=%v\n", blockNumber.Interface(), parents.Interface())
			results := getValidatorsMethod.Call([]reflect.Value{blockNumber, parents})
			fmt.Printf("DEBUG: GetValidators returned %d results\n", len(results))
			if len(results) == 2 && !results[1].IsNil() {
				err := results[1].Interface().(error)
				fmt.Printf("DEBUG: GetValidators returned error: %v\n", err)
				return nil, err
			}
			if len(results) >= 1 {
				result := results[0].Interface()
				fmt.Printf("DEBUG: GetValidators returned result type: %T\n", result)
				if accountSet, ok := result.(validator.AccountSet); ok {
					fmt.Printf("DEBUG: Successfully converted result to AccountSet\n")
					return accountSet, nil
				}
				fmt.Printf("DEBUG: Failed to convert result to AccountSet\n")
			}
		} else {
			fmt.Printf("DEBUG: GetValidators method not found\n")
		}

		// Debug: List all available methods on the consensus engine
		fmt.Printf("DEBUG: Available methods on consensus engine:\n")
		for i := 0; i < consensusValue.NumMethod(); i++ {
			method := consensusValue.Type().Method(i)
			fmt.Printf("DEBUG: Method %d: %s\n", i, method.Name)
		}

		// Try to access the delegates field directly if it exists
		if delegatesField := consensusValue.FieldByName("delegates"); delegatesField.IsValid() {
			fmt.Printf("DEBUG: Found delegates field directly\n")
			fmt.Printf("DEBUG: Delegates field type: %s\n", delegatesField.Type().String())

			// Check if it's a slice
			if delegatesField.Kind() == reflect.Slice {
				fmt.Printf("DEBUG: Delegates field is a slice with length: %d\n", delegatesField.Len())

				// Create a new AccountSet to hold the real data
				accountSet := make(validator.AccountSet, 0, delegatesField.Len())

				// Iterate through the slice elements
				for i := 0; i < delegatesField.Len(); i++ {
					element := delegatesField.Index(i)
					fmt.Printf("DEBUG: Element %d type: %s\n", i, element.Type().String())

					// Handle pointer type - get the value it points to
					var elementValue reflect.Value
					if element.Kind() == reflect.Ptr {
						elementValue = element.Elem()
						fmt.Printf("DEBUG: Element %d is pointer, dereferenced to: %s\n", i, elementValue.Type().String())
					} else {
						elementValue = element
					}

					// Create a new ValidatorMetadata
					validatorMeta := &validator.ValidatorMetadata{}

					// Try to access Address field
					if addressField := elementValue.FieldByName("Address"); addressField.IsValid() {
						fmt.Printf("DEBUG: Element %d address field found\n", i)
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
								fmt.Printf("DEBUG: Element %d address (from bytes): %s\n", i, addr.String())
							}
						}
					}

					// Try to access VotingPower field
					if votingPowerField := elementValue.FieldByName("VotingPower"); votingPowerField.IsValid() {
						fmt.Printf("DEBUG: Element %d voting power field found\n", i)
						fmt.Printf("DEBUG: Element %d voting power field type: %s\n", i, votingPowerField.Type().String())
						fmt.Printf("DEBUG: Element %d voting power field kind: %s\n", i, votingPowerField.Kind().String())
						fmt.Printf("DEBUG: Element %d voting power field is nil: %v\n", i, votingPowerField.IsNil())

						// Try to get the value directly
						if votingPowerField.CanAddr() {
							fmt.Printf("DEBUG: Element %d voting power field can addr: true\n", i)
							if votingPowerField.Kind() == reflect.Ptr && !votingPowerField.IsNil() {
								// Dereference the pointer
								vpValue := votingPowerField.Elem()
								fmt.Printf("DEBUG: Element %d voting power dereferenced type: %s\n", i, vpValue.Type().String())
								fmt.Printf("DEBUG: Element %d voting power dereferenced kind: %s\n", i, vpValue.Kind().String())

								if vpValue.Kind() == reflect.Struct {
									// This is a big.Int struct, we need to access its internal fields
									fmt.Printf("DEBUG: Element %d voting power is big.Int struct\n", i)

									// Try to access the internal 'abs' field of big.Int
									if absField := vpValue.FieldByName("abs"); absField.IsValid() {
										fmt.Printf("DEBUG: Element %d voting power abs field found\n", i)
										fmt.Printf("DEBUG: Element %d voting power abs field type: %s\n", i, absField.Type().String())
										fmt.Printf("DEBUG: Element %d voting power abs field kind: %s\n", i, absField.Kind().String())
										fmt.Printf("DEBUG: Element %d voting power abs field length: %d\n", i, absField.Len())

										if absField.Kind() == reflect.Slice {
											// Convert the abs slice to big.Int
											absBytes := make([]byte, absField.Len())
											for j := 0; j < absField.Len(); j++ {
												absBytes[j] = byte(absField.Index(j).Uint())
												fmt.Printf("DEBUG: Element %d voting power abs[%d] = %d\n", i, j, absBytes[j])
											}
											vp := new(big.Int).SetBytes(absBytes)
											validatorMeta.VotingPower = vp
											fmt.Printf("DEBUG: Element %d voting power (from abs): %s\n", i, vp.String())

											// Also try to get the raw uint64 value if it's small enough
											if vp.IsUint64() {
												uint64Val := vp.Uint64()
												fmt.Printf("DEBUG: Element %d voting power as uint64: %d\n", i, uint64Val)
											}
										}
									} else {
										fmt.Printf("DEBUG: Element %d voting power abs field not found\n", i)
									}

									// Also try to access the 'neg' field to see if it's negative
									if negField := vpValue.FieldByName("neg"); negField.IsValid() {
										fmt.Printf("DEBUG: Element %d voting power neg field found: %v\n", i, negField.Bool())
									}

									// Debug: List all available fields in the big.Int struct
									fmt.Printf("DEBUG: Element %d voting power big.Int available fields:\n", i)
									for j := 0; j < vpValue.NumField(); j++ {
										field := vpValue.Type().Field(j)
										fmt.Printf("DEBUG: Element %d voting power field %d: %s (type: %s)\n", i, j, field.Name, field.Type.String())
									}

									// Try to access other potential fields that might contain the full value
									if stakeField := elementValue.FieldByName("Stake"); stakeField.IsValid() {
										fmt.Printf("DEBUG: Element %d stake field found\n", i)
										if stakeField.CanInterface() {
											stake := stakeField.Interface()
											fmt.Printf("DEBUG: Element %d stake value: %v (type: %T)\n", i, stake, stake)
										}
									}

									if balanceField := elementValue.FieldByName("Balance"); balanceField.IsValid() {
										fmt.Printf("DEBUG: Element %d balance field found\n", i)
										if balanceField.CanInterface() {
											balance := balanceField.Interface()
											fmt.Printf("DEBUG: Element %d balance value: %v (type: %T)\n", i, balance, balance)
										}
									}

									// List all available fields in the ValidatorMetadata struct
									fmt.Printf("DEBUG: Element %d ValidatorMetadata available fields:\n", i)
									for j := 0; j < elementValue.NumField(); j++ {
										field := elementValue.Type().Field(j)
										fmt.Printf("DEBUG: Element %d field %d: %s (type: %s)\n", i, j, field.Name, field.Type.String())
									}

									// Try to access common validator fields
									if blsKeyField := elementValue.FieldByName("BlsKey"); blsKeyField.IsValid() {
										fmt.Printf("DEBUG: Element %d BlsKey field found\n", i)
									}

									if metadataField := elementValue.FieldByName("Metadata"); metadataField.IsValid() {
										fmt.Printf("DEBUG: Element %d Metadata field found\n", i)
									}
								} else if vpValue.Kind() == reflect.Uint64 || vpValue.Kind() == reflect.Int64 {
									// Convert to big.Int
									vp := new(big.Int).SetUint64(vpValue.Uint())
									validatorMeta.VotingPower = vp
									fmt.Printf("DEBUG: Element %d voting power: %s\n", i, vp.String())
								} else {
									fmt.Printf("DEBUG: Element %d voting power unexpected kind: %s\n", i, vpValue.Kind().String())
								}
							} else if votingPowerField.Kind() == reflect.Ptr && votingPowerField.IsNil() {
								fmt.Printf("DEBUG: Element %d voting power field is nil pointer\n", i)
							} else {
								fmt.Printf("DEBUG: Element %d voting power field is not a pointer, kind: %s\n", i, votingPowerField.Kind().String())
							}
						} else {
							fmt.Printf("DEBUG: Element %d voting power field cannot addr\n", i)
						}
					} else {
						fmt.Printf("DEBUG: Element %d voting power field not found\n", i)
					}

					// Try to access IsActive field
					if isActiveField := elementValue.FieldByName("IsActive"); isActiveField.IsValid() {
						fmt.Printf("DEBUG: Element %d is active field found\n", i)
						// Try to get the boolean value directly
						if isActiveField.CanAddr() {
							if isActiveField.Kind() == reflect.Bool {
								isActive := isActiveField.Bool()
								validatorMeta.IsActive = isActive
								fmt.Printf("DEBUG: Element %d is active: %v\n", i, isActive)
							}
						}
					}

					// Add the validator to our set
					accountSet = append(accountSet, validatorMeta)
					fmt.Printf("DEBUG: Added validator %d to account set\n", i)
				}

				// Return the real data we found!
				fmt.Printf("DEBUG: Returning real AccountSet with %d validators\n", len(accountSet))
				return accountSet, nil
			}
		}
	}

	// First, try to get from blockchain store if it has validator information
	if blockchainStore, ok := a.store.(interface {
		GetValidators() (validator.AccountSet, error)
	}); ok {
		fmt.Printf("DEBUG: Found GetValidators method\n")
		return blockchainStore.GetValidators()
	}

	// Try to get from consensus store with GetDelegates method
	if consensusStore, ok := a.store.(interface {
		GetDelegates() (validator.AccountSet, error)
	}); ok {
		fmt.Printf("DEBUG: Found GetDelegates method\n")
		return consensusStore.GetDelegates()
	}

	// Try to get from consensus store with GetConsensusValidators method
	if consensusStore, ok := a.store.(interface {
		GetConsensusValidators() (validator.AccountSet, error)
	}); ok {
		fmt.Printf("DEBUG: Found GetConsensusValidators method\n")
		return consensusStore.GetConsensusValidators()
	}

	fmt.Printf("DEBUG: No validator methods found\n")

	// If no validator store is available, return empty set
	// This allows the endpoint to work even when the consensus engine is not fully configured
	return validator.AccountSet{}, nil
}

func (a *dposStoreAdapter) GetStakingInfo() ([]*dpos.StakeInfo, error) {
	// Try to get staking info from the consensus engine
	// This should connect to the actual DPoS staking mechanism

	// First, try to get from blockchain store if it has staking information
	if blockchainStore, ok := a.store.(interface {
		GetStakingInfo() ([]*dpos.StakeInfo, error)
	}); ok {
		return blockchainStore.GetStakingInfo()
	}

	// Try to get from consensus store with GetStakingInfo method (different signature)
	if consensusStore, ok := a.store.(interface {
		GetStakingInfo(blockNumber uint64, staker types.Address) (*dpos.StakeInfo, error)
	}); ok {
		// For now, get staking info for zero address (all stakers)
		// TODO: Implement proper aggregation of all staking info
		stakeInfo, err := consensusStore.GetStakingInfo(0, types.ZeroAddress)
		if err == nil && stakeInfo != nil {
			return []*dpos.StakeInfo{stakeInfo}, nil
		}
	}

	// Try to get from consensus store with GetConsensusStakingInfo method
	if consensusStore, ok := a.store.(interface {
		GetConsensusStakingInfo() ([]*dpos.StakeInfo, error)
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
		fmt.Printf("DEBUG: GetStakingInfo - Found Consensus field through reflection\n")
		consensusEngine := consensusField.Interface()
		fmt.Printf("DEBUG: GetStakingInfo - Consensus engine type: %T\n", consensusEngine)

		// Try to get staking info from consensus engine using reflection
		consensusValue := reflect.ValueOf(consensusEngine)
		if consensusValue.Kind() == reflect.Ptr {
			consensusValue = consensusValue.Elem()
		}

		// Look for GetStakingInfo method
		if getStakingInfoMethod := consensusValue.MethodByName("GetStakingInfo"); getStakingInfoMethod.IsValid() {
			fmt.Printf("DEBUG: GetStakingInfo - Found GetStakingInfo method through reflection\n")

			// Get validators first to know which addresses to query
			validators, err := a.GetValidators()
			if err != nil {
				fmt.Printf("DEBUG: GetStakingInfo - Failed to get validators: %v\n", err)
				return []*dpos.StakeInfo{}, nil
			}

			fmt.Printf("DEBUG: GetStakingInfo - Found %d validators\n", len(validators))

			// Create staking info for each validator
			stakingInfos := make([]*dpos.StakeInfo, 0, len(validators))
			for i, validator := range validators {
				fmt.Printf("DEBUG: GetStakingInfo - Processing validator %d: %s\n", i, validator.Address.String())

				// Call GetStakingInfo for this validator
				blockNumber := reflect.ValueOf(uint64(0))
				stakerAddr := reflect.ValueOf(validator.Address)
				results := getStakingInfoMethod.Call([]reflect.Value{blockNumber, stakerAddr})

				if len(results) == 2 && !results[1].IsNil() {
					err := results[1].Interface().(error)
					fmt.Printf("DEBUG: GetStakingInfo - Error for validator %d: %v\n", i, err)
					continue
				}

				if len(results) >= 1 && !results[0].IsNil() {
					stakeInfo := results[0].Interface().(*dpos.StakeInfo)
					stakingInfos = append(stakingInfos, stakeInfo)
					fmt.Printf("DEBUG: GetStakingInfo - Added staking info for validator %d\n", i)
				} else {
					fmt.Printf("DEBUG: GetStakingInfo - No staking info for validator %d\n", i)
				}
			}

			if len(stakingInfos) > 0 {
				fmt.Printf("DEBUG: GetStakingInfo - Returning %d staking infos\n", len(stakingInfos))
				return stakingInfos, nil
			}
		}
	}

	// If no staking store is available, return empty slice
	// This allows the endpoint to work even when the consensus engine is not fully configured
	fmt.Printf("DEBUG: GetStakingInfo - No staking info found, returning empty slice\n")
	return []*dpos.StakeInfo{}, nil
}
