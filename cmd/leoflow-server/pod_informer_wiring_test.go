package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestPodInformer_NotConstructedInApiRole locks the split-role guard (ADR 0049):
// the shared pod informer (PR-10) is built only when this process serves the
// scheduler role AND dispatches to Kubernetes. The api-only role never watches
// pods — its sibling scheduler process owns the read-path — and a non-pod executor
// has no pods to watch. In those cases buildPodInformer returns nil and the reapers
// and reconciler keep their live read paths.
func TestPodInformer_NotConstructedInApiRole(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := fake.NewClientset()

	cfg := func(role, execType string) *config.ServerConfig {
		c := &config.ServerConfig{}
		c.Server.Role = role
		c.Executor.Type = execType
		c.Executor.TaskNamespace = "leoflow"
		return c
	}

	cases := []struct {
		name      string
		role      string
		execType  string
		wantBuilt bool
	}{
		{"api role never watches pods", config.RoleAPI, "kubernetes", false},
		{"scheduler role with pods builds the informer", config.RoleScheduler, "kubernetes", true},
		{"all role with pods builds the informer", config.RoleAll, "kubernetes", true},
		{"scheduler role without pods (subprocess) builds nothing", config.RoleScheduler, "subprocess", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pi := buildPodInformer(ctx, cfg(tc.role, tc.execType), cs, log)
			if built := pi != nil; built != tc.wantBuilt {
				t.Errorf("buildPodInformer built=%v, want %v", built, tc.wantBuilt)
			}
		})
	}
}
