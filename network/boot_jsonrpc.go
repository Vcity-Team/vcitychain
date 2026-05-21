package network

import (
	"fmt"
	"net"
	"strconv"

	"github.com/Vcity-Team/vcitychain/network/common"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// BootnodeJSONRPCByPeerID builds HTTP JSON-RPC URLs for trusted tip:
// host from genesis bootnodes multiaddr (ip4/ip6/dns), port from jsonrpcListen (e.g. node-config jsonrpc_addr).
func (s *Server) BootnodeJSONRPCByPeerID(jsonrpcListen string) map[peer.ID]string {
	if s.config == nil || s.config.Chain == nil {
		return nil
	}
	rpcPort, err := portFromListenAddr(jsonrpcListen)
	if err != nil || rpcPort <= 0 {
		return nil
	}
	out := make(map[peer.ID]string)
	for _, raw := range s.config.Chain.Bootnodes {
		host, err := hostFromBootMultiaddr(raw)
		if err != nil {
			continue
		}
		queryHost := hostForJSONRPCQuery(host)
		info, err := common.StringToAddrInfo(raw)
		if err != nil {
			continue
		}
		out[info.ID] = "http://" + net.JoinHostPort(queryHost, strconv.Itoa(rpcPort))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func portFromListenAddr(listen string) (int, error) {
	if listen == "" {
		return 0, fmt.Errorf("empty jsonrpc listen address")
	}
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, err
	}
	return port, nil
}

func hostFromBootMultiaddr(raw string) (string, error) {
	ma, err := multiaddr.NewMultiaddr(raw)
	if err != nil {
		return "", err
	}
	if ip4, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil && ip4 != "" {
		return ip4, nil
	}
	if ip6, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil && ip6 != "" {
		return ip6, nil
	}
	if dns, err := ma.ValueForProtocol(multiaddr.P_DNS); err == nil && dns != "" {
		return dns, nil
	}
	if dns4, err := ma.ValueForProtocol(multiaddr.P_DNS4); err == nil && dns4 != "" {
		return dns4, nil
	}
	if dns6, err := ma.ValueForProtocol(multiaddr.P_DNS6); err == nil && dns6 != "" {
		return dns6, nil
	}
	return "", fmt.Errorf("no ip4/ip6/dns in boot multiaddr: %s", raw)
}

// hostForJSONRPCQuery maps listen addresses to dialable hosts for local JSON-RPC.
func hostForJSONRPCQuery(host string) string {
	switch host {
	case "0.0.0.0", "::", "":
		return "127.0.0.1"
	default:
		return host
	}
}
