package config

import (
	"fmt"
)

// LogsSection configures task log shipping.
type LogsSection struct {
	// Dir is the root directory for the disk log sink (the default backend).
	Dir string `mapstructure:"dir"`
	// Backend selects the durable task-log store: "disk" (default) writes files
	// under Dir; "s3" ships each attempt to an S3-compatible bucket (AWS S3,
	// MinIO, Ceph RGW); "gcs" ships to Google Cloud Storage via its native SDK.
	// Object storage is opt-in, Lite and every deployment that does not set this
	// keep the exact on-disk path unchanged.
	Backend string `mapstructure:"backend"`
	// PersistenceEnabled indicates whether the log sink storage is backed by
	// durable persistence (a PVC in Kubernetes or host disk in Lite). When false
	// with backend "disk", logs are kept in an ephemeral emptyDir and will not
	// survive a pod restart.
	PersistenceEnabled bool `mapstructure:"persistence_enabled"`
	// Sink configures the object-store backend; read only when Backend is "s3" or
	// "gcs".
	Sink ObjectLogSection `mapstructure:"sink"`
	// Tail configures the live-tail fan-out of task log lines.
	Tail LogTailSection `mapstructure:"tail"`
}

// Live-tail publish modes (logs.tail.publish).
const (
	// LogTailPublishAlways publishes every received log line for the live
	// tail as it arrives (the default).
	LogTailPublishAlways = "always"
	// LogTailPublishOnDemand publishes only while a follower is subscribed,
	// probing at most once a second per log stream and replaying the lines
	// held since the last probe that found nobody.
	LogTailPublishOnDemand = "on_demand"
)

// LogTailSection configures the live-tail fan-out.
type LogTailSection struct {
	// Publish is "always" (default: every line is published, as before) or
	// "on_demand" (lines are published only while someone follows the attempt;
	// a new follower may see its first live lines up to about a second late).
	// Bind via DEXAFLOW_LOGS_TAIL_PUBLISH (legacy LEOFLOW_LOGS_TAIL_PUBLISH).
	Publish string `mapstructure:"publish"`
	// ReplayBudgetBytes bounds what the on_demand gates of all log streams hold
	// for replay together (#1621). Each stream holds at most 1 MiB, so without
	// it a thousand silent streams can hold about 500 MiB; over the budget the
	// longest silent stream's held lines are dropped first, and a follower of
	// that stream reads them from the stored log. 0 (the default) bounds
	// nothing, as before. Read only with Publish "on_demand".
	ReplayBudgetBytes int64 `mapstructure:"replay_budget_bytes"`
}

// ObjectLogSection configures the object-store log backend for both the "s3" and
// "gcs" providers. Auth is keyless-first (ADR 0035): leave the credential fields
// empty to use the ambient chain, IRSA / instance profile for S3, GKE Workload
// Identity (ADC) for GCS. Static keys and credential files are a discouraged
// escape hatch for dev and clusters without an identity broker.
//
// Bucket and Prefix apply to both providers. Region, Endpoint, ForcePathStyle,
// AccessKeyID and SecretAccessKey are S3-only. CredentialsFile is GCS-only. A
// field set for the other provider is simply ignored.
type ObjectLogSection struct {
	// Bucket is the target bucket. Required when Backend is "s3" or "gcs".
	Bucket string `mapstructure:"bucket"`
	// Prefix is an optional key prefix under which attempt objects are laid out.
	Prefix string `mapstructure:"prefix"`
	// Region is the S3 store region (e.g. "us-east-1"). Required by AWS S3;
	// ignored by some S3-compatible stores. S3-only.
	Region string `mapstructure:"region"`
	// Endpoint overrides the S3 endpoint for S3-compatible stores (MinIO, Ceph
	// RGW). Empty uses the AWS default endpoint. S3-only, it is NOT the way to
	// reach GCS, which has its own keyless "gcs" backend.
	Endpoint string `mapstructure:"endpoint"`
	// ForcePathStyle uses path-style addressing (bucket in the path, not the
	// host). Required by MinIO and some S3-compatible stores. S3-only.
	ForcePathStyle bool `mapstructure:"force_path_style"`
	// AccessKeyID is a static S3 access key. Empty (recommended) uses the keyless
	// credential chain (ADR 0035). S3-only.
	AccessKeyID string `mapstructure:"access_key_id"`
	// SecretAccessKey pairs with AccessKeyID. Discouraged; prefer keyless. S3-only.
	SecretAccessKey string `mapstructure:"secret_access_key"`
	// CredentialsFile is a path to a GCS service-account JSON key. Empty
	// (recommended) uses Application Default Credentials, GKE Workload Identity
	// keyless. GCS-only.
	CredentialsFile string `mapstructure:"credentials_file"`
	// Layout selects how new attempts are written to the bucket: "single"
	// (default) keeps one object per attempt at {try}.log, rewritten on every
	// flush; "segmented" writes numbered segments under {try}.log.d/
	// ({try}.e{epoch}.log.d/ for a later execution of the try) so a flush
	// uploads only the open segment. Both layouts are always readable. Turn
	// segmented on only once every replica runs a version that reads it.
	Layout string `mapstructure:"layout"`
}

// AlertsSection guards the destinations of native on-failure alerts (#424).
// An alert's URL and headers come from a tenant's connection, so on a shared
// engine a tenant could otherwise point one at the control plane's own network:
// loopback, a private service, or the cloud metadata endpoint.
type AlertsSection struct {
	// BlockPrivateDestinations refuses alert requests to loopback, private,
	// link-local (including 169.254.169.254), shared, unspecified, multicast and
	// broadcast addresses. The check runs on the address actually dialed, after
	// DNS resolution and on every redirect, and the guarded client does not use
	// the proxy environment. Off by default, so an existing install that alerts
	// an in-cluster endpoint keeps working.
	BlockPrivateDestinations bool `mapstructure:"block_private_destinations"`
	// AllowedCIDRs exempts these ranges (CIDRs or single addresses) from the
	// block, e.g. an on-premises chat server. Validated at startup even while the
	// block is off, so a typo surfaces before anyone turns it on; applied only
	// while it is on.
	AllowedCIDRs []string `mapstructure:"allowed_cidrs"`
}

// validateLogs rejects an unknown logs.backend and requires a bucket when an
// object backend is selected, so a misconfigured object sink fails closed at
// boot instead of losing every task log to a nonexistent bucket. Empty and
// "disk" are always valid, the on-disk default is unaffected.
func (c *ServerConfig) validateLogs() error {
	switch c.Logs.Tail.Publish {
	case "", LogTailPublishAlways, LogTailPublishOnDemand:
	default:
		return fmt.Errorf(`unknown logs.tail.publish %q (want %q or %q)`, c.Logs.Tail.Publish, LogTailPublishAlways, LogTailPublishOnDemand)
	}
	if c.Logs.Tail.ReplayBudgetBytes < 0 {
		return fmt.Errorf("logs.tail.replay_budget_bytes must not be negative (got %d); 0 means no budget", c.Logs.Tail.ReplayBudgetBytes)
	}
	switch c.Logs.Backend {
	case "", "disk":
		return nil
	case "s3", "gcs":
		if c.Logs.Sink.Bucket == "" {
			return fmt.Errorf(`logs.sink.bucket is required when logs.backend is %q (set LEOFLOW_LOGS_SINK_BUCKET)`, c.Logs.Backend)
		}
		switch c.Logs.Sink.Layout {
		case "", "single", "segmented":
			return nil
		default:
			return fmt.Errorf(`unknown logs.sink.layout %q (want "single" or "segmented")`, c.Logs.Sink.Layout)
		}
	default:
		return fmt.Errorf(`unknown logs.backend %q (want "disk", "s3" or "gcs")`, c.Logs.Backend)
	}
}
