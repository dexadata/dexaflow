package domain

import (
	"strings"
	"testing"
)

func remoteBuilderCfg(cb *CloudBuildConfig) *LeoflowConfig {
	c := &LeoflowConfig{SchemaVersion: "1.0", DagID: "sales", Build: &BuildConfig{RemoteBuilder: &RemoteBuilderConfig{CloudBuild: cb}}}
	c.ApplyDefaults()
	return c
}

// A Cloud Build remote builder with every field set in the documented forms
// loads.
func TestValidateAcceptsACloudBuildRemoteBuilder(t *testing.T) {
	for _, cb := range []*CloudBuildConfig{
		{Project: "acme-ci"},
		{Project: "example.com:acme-ci", Region: "us-central1"},
		{Project: "acme-ci", Region: "europe-west1",
			WorkerPool:     "projects/acme-ci/locations/europe-west1/workerPools/private",
			ServiceAccount: "projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com"},
	} {
		if err := remoteBuilderCfg(cb).Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want it accepted", cb, err)
		}
	}
}

// What gcloud would receive is checked when the project loads: a missing
// project, and any value gcloud could read as a flag or that is not in the
// form Cloud Build takes, is refused with the field named.
func TestValidateRefusesABadCloudBuildRemoteBuilder(t *testing.T) {
	for name, tc := range map[string]struct {
		cb    *CloudBuildConfig
		field string
	}{
		"no backend":          {nil, "build.remote_builder"},
		"no project":          {&CloudBuildConfig{}, "build.remote_builder.cloud_build.project"},
		"flag project":        {&CloudBuildConfig{Project: "--impersonate-service-account=x"}, "build.remote_builder.cloud_build.project"},
		"bad region":          {&CloudBuildConfig{Project: "acme-ci", Region: "us central1"}, "build.remote_builder.cloud_build.region"},
		"bad worker pool":     {&CloudBuildConfig{Project: "acme-ci", WorkerPool: "private"}, "build.remote_builder.cloud_build.worker_pool"},
		"pool without region": {&CloudBuildConfig{Project: "acme-ci", WorkerPool: "projects/acme-ci/locations/us-central1/workerPools/p"}, "build.remote_builder.cloud_build.region"},
		"pool in another region": {&CloudBuildConfig{Project: "acme-ci", Region: "europe-west1",
			WorkerPool: "projects/acme-ci/locations/us-central1/workerPools/p"}, "build.remote_builder.cloud_build.worker_pool"},
		"bare email account": {&CloudBuildConfig{Project: "acme-ci", ServiceAccount: "builder@acme-ci.iam.gserviceaccount.com"}, "build.remote_builder.cloud_build.service_account"},
	} {
		err := remoteBuilderCfg(tc.cb).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: Validate = %v, want a refusal naming %s", name, err, tc.field)
		}
	}
}

// The workspace file and targets carry the same block; its own Validate
// names the field under the prefix it is given.
func TestRemoteBuilderValidateNamesThePrefix(t *testing.T) {
	err := (&RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{}}).Validate("defaults.remote_builder")
	if err == nil || !strings.Contains(err.Error(), "defaults.remote_builder.cloud_build.project") {
		t.Errorf("Validate = %v, want the prefixed field", err)
	}
}

// The in-cluster BuildKit backend loads with its namespace alone, or with a
// pod selector and a kubeconfig context.
func TestValidateAcceptsABuildKitRemoteBuilder(t *testing.T) {
	for _, bk := range []*BuildKitConfig{
		{Namespace: "dexaflow-builder"},
		{Namespace: "builds", PodSelector: "app.kubernetes.io/instance=dexaflow,app.kubernetes.io/component=builder", Context: "prod-eu"},
		{Namespace: "builds", PodSelector: "app=buildkitd", Context: "arn:aws:eks:eu-west-1:123456789012:cluster/prod"},
	} {
		if err := (&RemoteBuilderConfig{BuildKit: bk}).Validate("build.remote_builder"); err != nil {
			t.Errorf("Validate(%+v) = %v, want it accepted", bk, err)
		}
	}
}

// What kubectl and buildctl would receive is checked when the file loads:
// no namespace, a value that is not a namespace name or an equality
// selector, a context kubectl could read as a flag, and two backends at
// once are refused with the field named.
func TestValidateRefusesABadBuildKitRemoteBuilder(t *testing.T) {
	for name, tc := range map[string]struct {
		rb    *RemoteBuilderConfig
		field string
	}{
		"no namespace":   {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{}}, "build.remote_builder.buildkit.namespace"},
		"bad namespace":  {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{Namespace: "Builds_1"}}, "build.remote_builder.buildkit.namespace"},
		"flag namespace": {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{Namespace: "--all-namespaces"}}, "build.remote_builder.buildkit.namespace"},
		"bad selector":   {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{Namespace: "builds", PodSelector: "app in (a b)"}}, "build.remote_builder.buildkit.pod_selector"},
		"flag context":   {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{Namespace: "builds", Context: "-v=9"}}, "build.remote_builder.buildkit.context"},
		"ctl context":    {&RemoteBuilderConfig{BuildKit: &BuildKitConfig{Namespace: "builds", Context: "a\nb"}}, "build.remote_builder.buildkit.context"},
		"two backends": {&RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{Project: "acme-ci"}, BuildKit: &BuildKitConfig{Namespace: "builds"}},
			"build.remote_builder"},
	} {
		err := tc.rb.Validate("build.remote_builder")
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: Validate = %v, want a refusal naming %s", name, err, tc.field)
		}
	}
	err := (&RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{Project: "acme-ci"}, BuildKit: &BuildKitConfig{Namespace: "builds"}}).Validate("build.remote_builder")
	if err == nil || !strings.Contains(err.Error(), "one backend") {
		t.Errorf("two backends: Validate = %v, want it to say one backend is set", err)
	}
}

// An unset pod selector selects the pods of the chart's builder.
func TestBuildKitDefaultPodSelector(t *testing.T) {
	if got := (BuildKitConfig{Namespace: "builds"}).Selector(); got != "app.kubernetes.io/component=builder" {
		t.Errorf("Selector() = %q, want the chart's component label", got)
	}
	if got := (BuildKitConfig{Namespace: "builds", PodSelector: "app=buildkitd"}).Selector(); got != "app=buildkitd" {
		t.Errorf("Selector() = %q, want the configured selector", got)
	}
}
