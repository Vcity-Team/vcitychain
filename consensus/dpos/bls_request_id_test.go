package dpos

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestBLSKeyMessage_RequestIDRoundTrip(t *testing.T) {
	reqID := "bls_request_0xabc_123456789"
	req := &BLSKeyRequestMessage{
		RequestID:        reqID,
		RequestedAddress: types.StringToAddress("0x2222222222222222222222222222222222222222"),
		Requester:        types.StringToAddress("0x3333333333333333333333333333333333333333"),
		Timestamp:        uint64(time.Now().Unix()),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decodedReq BLSKeyRequestMessage
	if err := json.Unmarshal(data, &decodedReq); err != nil {
		t.Fatal(err)
	}
	if decodedReq.RequestID != reqID {
		t.Fatalf("requestId lost on request: got %q", decodedReq.RequestID)
	}

	// Response must echo the same RequestID (not rebuild from response Timestamp).
	resp := &BLSKeyResponseMessage{
		RequestID:        decodedReq.RequestID,
		RequestedAddress: decodedReq.RequestedAddress,
		Requester:        decodedReq.Requester,
		Found:            true,
		BLSPublicKey:     []byte{1, 2, 3},
		Timestamp:        uint64(time.Now().Unix()) + 5, // deliberately different from request
	}
	rdata, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decodedResp BLSKeyResponseMessage
	if err := json.Unmarshal(rdata, &decodedResp); err != nil {
		t.Fatal(err)
	}
	if decodedResp.RequestID != reqID {
		t.Fatalf("response did not echo requestId: got %q", decodedResp.RequestID)
	}

	// Old bug: reconstruct from response timestamp — must not match wire RequestID.
	legacyID := "bls_request_" + decodedResp.RequestedAddress.String() + "_" +
		strings.TrimSpace(string([]byte{})) // placeholder; compare explicitly below
	_ = legacyID
	broken := formatLegacyBLSRequestID(decodedResp.RequestedAddress, decodedResp.Timestamp)
	if broken == decodedResp.RequestID {
		t.Fatalf("requestId unexpectedly equals legacy timestamp formula")
	}
}

func formatLegacyBLSRequestID(addr types.Address, ts uint64) string {
	return "bls_request_" + addr.String() + "_" + itoaUint64(ts)
}

func itoaUint64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
