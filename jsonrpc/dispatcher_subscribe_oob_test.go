package jsonrpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestHandleSubscribe_LogsMissingFilter_NoPanic(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		store,
		&dispatcherParams{
			chainID:                 100,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)

	mockConnection, _ := newMockWsConnWithMsgCh()
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs"]}`)

	done := make(chan struct{})
	var panicked interface{}
	var resp []byte
	var err error

	go func() {
		defer func() {
			panicked = recover()
			close(done)
		}()
		resp, err = dispatcher.HandleWs(req, mockConnection)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HandleWs hung")
	}

	require.Nil(t, panicked, "eth_subscribe [\"logs\"] must not panic: %v", panicked)
	require.NoError(t, err)

	var envelope ErrorResponse
	require.NoError(t, json.Unmarshal(resp, &envelope))
	require.NotNil(t, envelope.Error)
	require.Equal(t, NewInvalidParamsError("Invalid params").ErrorCode(), envelope.Error.Code)
}

func TestHandleSubscribe_LogsWithFilter_Supported(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		store,
		&dispatcherParams{
			chainID:                 100,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)

	mockConnection, _ := newMockWsConnWithMsgCh()
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs",{}]}`)
	resp, err := dispatcher.HandleWs(req, mockConnection)
	require.NoError(t, err)

	var envelope SuccessResponse
	require.NoError(t, json.Unmarshal(resp, &envelope))
	require.NotNil(t, envelope.Result)
}

// panicDispatcher always panics from HandleWs — used to verify handleWs recover.
type panicDispatcher struct{}

func (p *panicDispatcher) Handle([]byte) ([]byte, error) { return nil, nil }
func (p *panicDispatcher) HandleWs([]byte, wsConn) ([]byte, error) {
	panic("intentional ws panic")
}
func (p *panicDispatcher) RemoveFilterByWs(wsConn) {}

func TestHandleWs_RecoversFromHandlerPanic(t *testing.T) {
	t.Parallel()

	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{},
		dispatcher: &panicDispatcher{},
	}
	srv := httptest.NewServer(http.HandlerFunc(j.handleWs))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`),
	))

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err, "server must stay up and reply after recover")
	require.Contains(t, string(msg), "internal error")

	// Second request proves the accept/read loop survived the panicked handler goroutine.
	require.NoError(t, conn.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`),
	))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, msg2, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Contains(t, string(msg2), "internal error")
}
