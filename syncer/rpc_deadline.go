package syncer

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// isRPCDeadlineExceeded 识别 GetPeerStatus 等 RPC 超时（含多层 wrap / 字符串描述），用于降噪日志。
func isRPCDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Is(e, context.DeadlineExceeded) {
			return true
		}
		if st, ok := grpcstatus.FromError(e); ok && st.Code() == codes.DeadlineExceeded {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadlineexceeded") || strings.Contains(msg, "deadline exceeded")
}
