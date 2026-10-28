package agentrpc

import (
	"time"

	"google.golang.org/grpc/keepalive"
)

// KeepaliveEnforcementPolicy is the client-ping policy the agent gRPC server
// enforces. It permits a ping every 10 seconds, with or without an active
// stream, so neither a warm worker's idle assignment stream nor an agent's
// client keepalive (#946) is closed as abusive ("too_many_pings"). It only
// relaxes the gRPC default (5 minutes, streams only).
//
// The agent's own keepalive interval must stay at or above MinTime; the
// agent's tests pin that.
func KeepaliveEnforcementPolicy() keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime:             10 * time.Second,
		PermitWithoutStream: true,
	}
}
