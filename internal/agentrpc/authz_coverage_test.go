package agentrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// Every agent RPC authenticates and authorizes inside its own handler
// (identify, then requireAttemptToken or requireWarmWorkerToken). Nothing in
// front of the handlers refuses a call on their behalf, so an RPC added
// without those calls would serve tenant secrets, task specs or XCom to any
// caller that reaches the port. These tests close that gap from the outside:
// the policy table below must name every method of the generated service
// descriptor, and each method is called through a real gRPC client to prove
// it refuses the credentials its policy rules out.

// rpcPolicy is the credential an agent RPC accepts.
type rpcPolicy int

const (
	// attemptOnly accepts a task (attempt) token and refuses a warm worker's
	// control channel token (ADR 0058 D2).
	attemptOnly rpcPolicy = iota
	// warmOnly accepts only a warm worker's control channel token.
	warmOnly
	// anyAgentToken accepts either agent token.
	anyAgentToken
	// projectedToken accepts a Kubernetes projected ServiceAccount token, which
	// the handler reviews before minting an agent token.
	projectedToken
)

// agentRPCPolicies is the credential each AgentService method accepts. A new
// RPC fails TestAgentRPCPolicyCoversEveryMethod until it is listed here, and
// TestAgentRPCsRefuseCredentialsTheirPolicyRulesOut then checks the handler
// enforces what the entry says.
var agentRPCPolicies = map[string]rpcPolicy{
	agentv1.AgentService_ExchangeToken_FullMethodName:   projectedToken,
	agentv1.AgentService_Register_FullMethodName:        anyAgentToken,
	agentv1.AgentService_GetTaskSpec_FullMethodName:     attemptOnly,
	agentv1.AgentService_FetchXCom_FullMethodName:       attemptOnly,
	agentv1.AgentService_FetchXComBatch_FullMethodName:  attemptOnly,
	agentv1.AgentService_PushXCom_FullMethodName:        attemptOnly,
	agentv1.AgentService_StreamLogs_FullMethodName:      attemptOnly,
	agentv1.AgentService_ReportState_FullMethodName:     attemptOnly,
	agentv1.AgentService_Heartbeat_FullMethodName:       attemptOnly,
	agentv1.AgentService_GetVariables_FullMethodName:    attemptOnly,
	agentv1.AgentService_GetConnections_FullMethodName:  attemptOnly,
	agentv1.AgentService_AwaitAssignment_FullMethodName: warmOnly,
}

// serviceMethods lists the full method names of every unary and streaming RPC
// in the generated AgentService descriptor.
func serviceMethods() []string {
	desc := agentv1.AgentService_ServiceDesc
	out := make([]string, 0, len(desc.Methods)+len(desc.Streams))
	for _, m := range desc.Methods {
		out = append(out, "/"+desc.ServiceName+"/"+m.MethodName)
	}
	for _, s := range desc.Streams {
		out = append(out, "/"+desc.ServiceName+"/"+s.StreamName)
	}
	slices.Sort(out)
	return out
}

func TestAgentRPCPolicyCoversEveryMethod(t *testing.T) {
	methods := serviceMethods()
	for _, m := range methods {
		if _, ok := agentRPCPolicies[m]; !ok {
			t.Errorf("%s has no entry in agentRPCPolicies: state which credential it accepts and make its handler enforce it", m)
		}
	}
	for m := range agentRPCPolicies {
		if !slices.Contains(methods, m) {
			t.Errorf("agentRPCPolicies lists %s, which the service no longer has", m)
		}
	}
	if len(rpcCallers) != len(methods) {
		t.Errorf("rpcCallers has %d entries, the service has %d methods", len(rpcCallers), len(methods))
	}
}

// rpcCaller invokes one RPC with an empty request and returns the status the
// client receives.
type rpcCaller func(ctx context.Context, c agentv1.AgentServiceClient) error

// rpcCallers calls each method through the generated client, the way an agent
// does.
var rpcCallers = map[string]rpcCaller{
	agentv1.AgentService_ExchangeToken_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.ExchangeToken(ctx, &agentv1.ExchangeTokenRequest{})
		return err
	},
	agentv1.AgentService_Register_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.Register(ctx, &agentv1.RegisterRequest{})
		return err
	},
	agentv1.AgentService_GetTaskSpec_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
		return err
	},
	agentv1.AgentService_FetchXCom_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.FetchXCom(ctx, &agentv1.FetchXComRequest{})
		return err
	},
	agentv1.AgentService_FetchXComBatch_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.FetchXComBatch(ctx, &agentv1.FetchXComBatchRequest{})
		return err
	},
	agentv1.AgentService_PushXCom_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.PushXCom(ctx, &agentv1.PushXComRequest{})
		return err
	},
	agentv1.AgentService_StreamLogs_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		stream, err := c.StreamLogs(ctx)
		if err != nil {
			return err
		}
		if cerr := stream.CloseSend(); cerr != nil {
			return cerr
		}
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	},
	agentv1.AgentService_ReportState_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.ReportState(ctx, &agentv1.ReportStateRequest{})
		return err
	},
	agentv1.AgentService_Heartbeat_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
		return err
	},
	agentv1.AgentService_GetVariables_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.GetVariables(ctx, &agentv1.GetVariablesRequest{})
		return err
	},
	agentv1.AgentService_GetConnections_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		_, err := c.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
		return err
	},
	agentv1.AgentService_AwaitAssignment_FullMethodName: func(ctx context.Context, c agentv1.AgentServiceClient) error {
		stream, err := c.AwaitAssignment(ctx)
		if err != nil {
			return err
		}
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	},
}

// startPolicyServer serves a fully wired agent Server (token exchange, warm
// pools, logs) over an in-memory listener, so no handler refuses for being
// unconfigured before it gets to the credential check.
func startPolicyServer(t *testing.T) (agentv1.AgentServiceClient, *auth.JWTAuthenticator) {
	t.Helper()
	srv, a := newServer(&fakeStore{})
	srv.SetTokenExchange(&fakeReviewer{err: errors.New("token review refused")}, &fakeResolver{id: testIdentity()}, a, time.Hour, true)
	srv.EnableWarmPools(nil)
	srv.SetLogSink(&refSink{})

	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialing the in-memory server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentv1.NewAgentServiceClient(conn), a
}

// outgoingToken returns a client context carrying a bearer token for id.
func outgoingToken(t *testing.T, a *auth.JWTAuthenticator, id auth.AgentIdentity) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func TestAgentRPCsRefuseCredentialsTheirPolicyRulesOut(t *testing.T) {
	client, a := startPolicyServer(t)
	creds := map[string]func() context.Context{
		"no token": context.Background,
		"forged token": func() context.Context {
			return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer forged")
		},
		"warm token": func() context.Context { return outgoingToken(t, a, warmTokenIdentity()) },
		"task token": func() context.Context { return outgoingToken(t, a, testIdentity()) },
	}
	for _, method := range serviceMethods() {
		call, ok := rpcCallers[method]
		if !ok {
			t.Errorf("%s has no caller in rpcCallers", method)
			continue
		}
		policy := agentRPCPolicies[method]
		for _, name := range []string{"no token", "forged token", "warm token", "task token"} {
			want, refused := refusal(policy, name)
			if !refused {
				continue
			}
			t.Run(method+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(creds[name](), 5*time.Second)
				defer cancel()
				err := call(ctx, client)
				if got := status.Code(err); got != want {
					t.Fatalf("code = %v (%v), want %v", got, err, want)
				}
			})
		}
	}
}

// refusal reports the code a method with policy must return for the named
// credential, and false when the credential is one the policy accepts (the
// call then proceeds past authorization, which other tests cover).
func refusal(policy rpcPolicy, cred string) (codes.Code, bool) {
	switch cred {
	case "no token", "forged token":
		return codes.Unauthenticated, true
	case "warm token":
		if policy == attemptOnly {
			return codes.PermissionDenied, true
		}
	case "task token":
		if policy == warmOnly {
			return codes.PermissionDenied, true
		}
	}
	return codes.OK, false
}
