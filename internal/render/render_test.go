package render

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
)

func stack() *v1.PlatformStack {
	return &v1.PlatformStack{
		ObjectMeta: metav1.ObjectMeta{Name: "site", Namespace: "ns"},
		Spec: v1.PlatformStackSpec{
			Version:   "5.0.0",
			Database:  v1.StorageSpec{Size: resource.MustParse("100Gi")},
			Messaging: v1.StorageSpec{Size: resource.MustParse("10Gi")},
		},
	}
}

// restricted checks a pod against OpenShift's restricted-v2 SCC and Kubernetes' restricted
// Pod Security Standard.
func restricted(t *testing.T, what string, pod corev1.PodSpec) {
	t.Helper()
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Errorf("%s: runAsNonRoot must be true", what)
	}
	if pod.SecurityContext.RunAsUser != nil {
		t.Errorf("%s: no fixed UID; OpenShift assigns one from the namespace range", what)
	}
	if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: seccomp must be RuntimeDefault", what)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Errorf("%s: workloads do not talk to the API server and must not mount a token", what)
	}
	for _, c := range append(pod.InitContainers, pod.Containers...) {
		sc := c.SecurityContext
		if sc == nil {
			t.Fatalf("%s/%s: no security context", what, c.Name)
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("%s/%s: allowPrivilegeEscalation must be false", what, c.Name)
		}
		if sc.Privileged != nil && *sc.Privileged {
			t.Errorf("%s/%s: privileged", what, c.Name)
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0 {
			t.Errorf("%s/%s: must drop ALL capabilities and add none", what, c.Name)
		}
		if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Errorf("%s/%s: root filesystem must be read-only", what, c.Name)
		}
		if c.Resources.Limits.Memory().IsZero() || c.Resources.Requests.Cpu().IsZero() {
			t.Errorf("%s/%s: needs a memory limit and a CPU request", what, c.Name)
		}
		if strings.HasSuffix(c.Image, ":latest") || !strings.Contains(c.Image, ":") {
			t.Errorf("%s/%s: image must be pinned, got %s", what, c.Name, c.Image)
		}
	}
	for _, v := range pod.Volumes {
		if v.HostPath != nil {
			t.Errorf("%s: hostPath volume %s", what, v.Name)
		}
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC {
		t.Errorf("%s: host namespaces", what)
	}
}

func TestEveryWorkloadIsRestricted(t *testing.T) {
	s, img := stack(), Images{Registry: "r"}
	restricted(t, "postgres", StatefulSet(s, Postgres, img.Image(Postgres, "16"), 5432, s.Spec.Database, "c").Spec.Template.Spec)
	restricted(t, "rabbitmq", StatefulSet(s, RabbitMQ, img.Image(RabbitMQ, "3.13"), 5672, s.Spec.Messaging, "c").Spec.Template.Spec)
	restricted(t, "core", Deployment(s, Core, img.Image(Core, "5.0.0"), "cfg", map[string]string{"a": "b"}, 2, 8080).Spec.Template.Spec)
	restricted(t, "job", Job(s, "site-migrate-20", img.Image(Core, "5.0.0"), []string{"migrate"}, true).Spec.Template.Spec)
}

func TestDeploymentNeverDropsBelowCapacity(t *testing.T) {
	d := Deployment(stack(), Core, "r/core:5.0.0", "cfg", nil, 3, 8080)
	ru := d.Spec.Strategy.RollingUpdate
	if ru == nil || ru.MaxUnavailable.IntValue() != 0 {
		t.Fatal("a rollout must not take serving pods away before new ones are ready")
	}
	if d.Spec.Template.Spec.Containers[0].ReadinessProbe == nil {
		t.Fatal("readiness gates the rollout and needs a probe")
	}
}

func TestConfigChangeRestartsPods(t *testing.T) {
	a := Deployment(stack(), Core, "r/core:5.0.0", "cfg", map[string]string{"retention.days": "90"}, 1, 8080)
	b := Deployment(stack(), Core, "r/core:5.0.0", "cfg", map[string]string{"retention.days": "30"}, 1, 8080)
	if a.Spec.Template.Annotations["platform.stacklifecycle.io/config-hash"] == b.Spec.Template.Annotations["platform.stacklifecycle.io/config-hash"] {
		t.Fatal("a config change must change the pod template so pods pick it up")
	}
}

func TestJobsRunOnceAndAreKeptForInspection(t *testing.T) {
	j := Job(stack(), "site-migrate-20", "r/core:5.0.0", []string{"migrate"}, false)
	if *j.Spec.BackoffLimit != 0 {
		t.Fatal("a schema migration must not be retried blindly")
	}
	if j.Spec.TTLSecondsAfterFinished == nil || *j.Spec.TTLSecondsAfterFinished < 86400 {
		t.Fatal("finished jobs are kept long enough to read their logs")
	}
}

func TestNamesAndKeys(t *testing.T) {
	s := stack()
	if got := ConfigName(s, "5.1.0"); got != "site-config-5-1-0" {
		t.Fatal(got)
	}
	if got := EnvKey("handover.format"); got != "HANDOVER_FORMAT" {
		t.Fatal(got)
	}
	if got := EnvKey("tls.minVersion"); got != "TLS_MIN_VERSION" {
		t.Fatal(got)
	}
	if Hash(map[string]string{"a": "1", "b": "2"}) != Hash(map[string]string{"b": "2", "a": "1"}) {
		t.Fatal("hash must not depend on map order")
	}
}
