package jsonrpc

import (
	"math/big"
	"testing"
)

func TestParseVoteRequestCreatePath(t *testing.T) {
	d := &DPOS{}

	req, errResp := d.parseVoteRequest([]interface{}{
		"0x1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222",
		"1000000000000000000",
	}, false)
	if errResp != nil {
		t.Fatalf("unexpected error: %s", errResp.Error)
	}
	if req.Voter == "" || req.Candidate == "" || req.Amount == "" {
		t.Fatalf("missing fields: %+v", req)
	}
	if req.PrivateKey != "" {
		t.Fatalf("private key should be empty on create path")
	}
}

func TestParseVoteRequestRequiresPrivateKey(t *testing.T) {
	d := &DPOS{}
	_, errResp := d.parseVoteRequest([]interface{}{
		"0x1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222",
		"1",
	}, true)
	if errResp == nil {
		t.Fatal("expected error when private key missing")
	}
}

func TestToHexHelpers(t *testing.T) {
	if got := toHexUint64(100000); got != "0x186a0" {
		t.Fatalf("toHexUint64: got %s", got)
	}
	if got := toHexBig(big.NewInt(1000000000)); got != "0x3b9aca00" {
		t.Fatalf("toHexBig: got %s", got)
	}
	if got := toHexBytes([]byte("DP")); got != "0x4450" {
		t.Fatalf("toHexBytes: got %s", got)
	}
}
