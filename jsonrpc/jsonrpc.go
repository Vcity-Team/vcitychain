package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/versioning"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
)

type serverType int

const (
	serverIPC serverType = iota
	serverHTTP
	serverWS
)

func (s serverType) String() string {
	switch s {
	case serverIPC:
		return "ipc"
	case serverHTTP:
		return "http"
	case serverWS:
		return "ws"
	default:
		panic("BUG: Not expected") //nolint:gocritic
	}
}

// JSONRPC is an API consensus
type JSONRPC struct {
	logger      hclog.Logger
	config      *Config
	dispatcher  dispatcher
	rateLimiter *ipRateLimiter
}

type dispatcher interface {
	RemoveFilterByWs(conn wsConn)
	HandleWs(reqBody []byte, conn wsConn) ([]byte, error)
	Handle(reqBody []byte) ([]byte, error)
}

// JSONRPCStore defines all the methods required
// by all the JSON RPC endpoints
type JSONRPCStore interface {
	ethStore
	networkStore
	txPoolStore
	filterManagerStore
	bridgeStore
	debugStore
}

type Config struct {
	Store                    JSONRPCStore
	Addr                     *net.TCPAddr
	ChainID                  uint64
	ChainName                string
	AccessControlAllowOrigin []string
	PriceLimit               uint64
	BatchLengthLimit         uint64
	BlockRangeLimit          uint64

	ConcurrentRequestsDebug uint64
	WebSocketReadLimit      uint64

	// APIs lists enabled JSON-RPC namespaces. Empty uses DefaultJSONRPCAPIs.
	APIs []string

	// HTTPRequestsPerSecond caps requests per client IP (0 = default 200/s).
	HTTPRequestsPerSecond int
}

// NewJSONRPC returns the JSONRPC http server
func NewJSONRPC(logger hclog.Logger, config *Config) (*JSONRPC, error) {
	apis := config.APIs
	if len(apis) == 0 {
		apis = DefaultJSONRPCAPIs
	}

	d, err := newDispatcher(
		logger,
		config.Store,
		&dispatcherParams{
			chainID:                 config.ChainID,
			chainName:               config.ChainName,
			priceLimit:              config.PriceLimit,
			jsonRPCBatchLengthLimit: config.BatchLengthLimit,
			blockRangeLimit:         config.BlockRangeLimit,
			concurrentRequestsDebug: config.ConcurrentRequestsDebug,
			enabledAPIs:             parseJSONRPCAPIList(apis),
		},
	)

	if err != nil {
		return nil, err
	}

	srv := &JSONRPC{
		logger:      logger.Named("jsonrpc"),
		config:      config,
		dispatcher:  d,
		rateLimiter: newIPRateLimiter(config.HTTPRequestsPerSecond, time.Second),
	}

	// start http server
	if err := srv.setupHTTP(); err != nil {
		return nil, err
	}

	return srv, nil
}

func (j *JSONRPC) setupHTTP() error {
	j.logger.Info("http server started", "addr", j.config.Addr.String())
	if j.config.Addr != nil && j.config.Addr.IP != nil && j.config.Addr.IP.IsUnspecified() {
		j.logger.Warn("JSON-RPC is bound to all interfaces; restrict with firewall or bind 127.0.0.1 in production")
	}

	lis, err := net.Listen("tcp", j.config.Addr.String())
	if err != nil {
		return err
	}

	// NewServeMux must be used, as it disables all debug features.
	// For some strange reason, with DefaultServeMux debug/vars is always enabled (but not debug/pprof).
	// If pprof need to be enabled, this should be DefaultServeMux
	mux := http.NewServeMux()

	// The middleware factory returns a handler, so we need to wrap the handler function properly.
	jsonRPCHandler := http.HandlerFunc(j.handle)
	mux.Handle("/", middlewareFactory(j.config)(jsonRPCHandler))

	mux.HandleFunc("/ws", j.handleWs)

	srv := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 60 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			j.logger.Error("closed http connection", "err", err)
		}
	}()

	return nil
}

// The middlewareFactory builds a middleware which enables CORS using the provided config.
func middlewareFactory(config *Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			for _, allowedOrigin := range config.AccessControlAllowOrigin {
				if allowedOrigin == "*" {
					w.Header().Set("Access-Control-Allow-Origin", "*")

					break
				}

				if allowedOrigin == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)

					break
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// wsUpgrader defines upgrade parameters for the WS connection
var wsUpgrader = websocket.Upgrader{
	// Uses the default HTTP buffer sizes for Read / Write buffers.
	// Documentation specifies that they are 4096B in size.
	// There is no need to have them be 4x in size when requests / responses
	// shouldn't exceed 1024B
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// wsWrapper is a wrapping object for the web socket connection and logger
type wsWrapper struct {
	sync.Mutex

	ws       *websocket.Conn // the actual WS connection
	logger   hclog.Logger    // module logger
	filterID string          // filter ID
}

func (w *wsWrapper) SetFilterID(filterID string) {
	w.filterID = filterID
}

func (w *wsWrapper) GetFilterID() string {
	return w.filterID
}

// WriteMessage writes out the message to the WS peer
func (w *wsWrapper) WriteMessage(messageType int, data []byte) error {
	w.Lock()
	defer w.Unlock()
	writeErr := w.ws.WriteMessage(messageType, data)

	if writeErr != nil {
		w.logger.Error(
			fmt.Sprintf("Unable to write WS message, %s", writeErr.Error()),
		)
	}

	return writeErr
}

// isSupportedWSType returns a status indicating if the message type is supported
func isSupportedWSType(messageType int) bool {
	return messageType == websocket.TextMessage ||
		messageType == websocket.BinaryMessage
}

func (j *JSONRPC) handleWs(w http.ResponseWriter, req *http.Request) {
	upgrader := wsUpgrader
	upgrader.CheckOrigin = j.allowedWSOrigin

	// Upgrade the connection to a WS one
	ws, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		j.logger.Error(fmt.Sprintf("Unable to upgrade to a WS connection, %s", err.Error()))

		return
	}

	// Set a read limit (maximum message size) for this connection
	if j.config.WebSocketReadLimit != 0 {
		ws.SetReadLimit(int64(j.config.WebSocketReadLimit))
	}

	// Defer WS closure
	defer func(ws *websocket.Conn) {
		err = ws.Close()
		if err != nil {
			j.logger.Error(
				fmt.Sprintf("Unable to gracefully close WS connection, %s", err.Error()),
			)
		}
	}(ws)

	wrapConn := &wsWrapper{ws: ws, logger: j.logger}

	j.logger.Info("Websocket connection established")
	// Run the listen loop
	for {
		// Read the incoming message
		msgType, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
			) {
				// Accepted close codes
				j.logger.Info("Closing WS connection gracefully")
			} else {
				j.logger.Error(fmt.Sprintf("Unable to read WS message, %s", err.Error()))
				j.logger.Info("Closing WS connection with error")
			}

			j.dispatcher.RemoveFilterByWs(wrapConn)

			break
		}

		if isSupportedWSType(msgType) {
			go func() {
				resp, handleErr := j.dispatcher.HandleWs(message, wrapConn)
				if handleErr != nil {
					j.logger.Error(fmt.Sprintf("Unable to handle WS request, %s", handleErr.Error()))

					_ = wrapConn.WriteMessage(
						msgType,
						[]byte(fmt.Sprintf("WS Handle error: %s", handleErr.Error())),
					)
				} else {
					_ = wrapConn.WriteMessage(msgType, resp)
				}
			}()
		}
	}
}

func (j *JSONRPC) handle(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization",
	)

	switch req.Method {
	case "POST":
		j.handleJSONRPCRequest(w, req)
	case "GET":
		j.handleGetRequest(w)
	case "OPTIONS":
		// nothing to return
	default:
		_, _ = w.Write([]byte("method " + req.Method + " not allowed"))
	}
}

func (j *JSONRPC) handleJSONRPCRequest(w http.ResponseWriter, req *http.Request) {
	if j.rateLimiter != nil && !j.rateLimiter.allow(clientIP(req)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Cap request body to limit memory exhaustion (JSON-RPC params may be large but not unbounded).
	const maxJSONRPCBody = 5 << 20 // 5 MiB
	req.Body = http.MaxBytesReader(w, req.Body, maxJSONRPCBody)

	data, err := io.ReadAll(req.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "request body unreadable", http.StatusBadRequest)
		return
	}

	resp, err := j.dispatcher.Handle(data)
	if err != nil {
		_, _ = w.Write([]byte(err.Error()))
	} else {
		_, _ = w.Write(resp)
	}
}

// allowedWSOrigin mirrors AccessControlAllowOrigin for browser WebSocket upgrades.
// Empty Origin (non-browser clients) is allowed. "*" in the allowlist keeps legacy open behavior.
func (j *JSONRPC) allowedWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if j.config == nil {
		return false
	}
	for _, allowed := range j.config.AccessControlAllowOrigin {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

type GetResponse struct {
	Name    string `json:"name"`
	ChainID uint64 `json:"chain_id"`
	Version string `json:"version"`
}

func (j *JSONRPC) handleGetRequest(writer io.Writer) {
	data := &GetResponse{
		Name:    j.config.ChainName,
		ChainID: j.config.ChainID,
		Version: versioning.Version,
	}

	resp, err := json.Marshal(data)
	if err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}

	if _, err = writer.Write(resp); err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}
}
