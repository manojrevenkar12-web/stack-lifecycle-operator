package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/render"
)

// These tests run the reconciler against a real API server (envtest). No kubelet or built-in
// controllers run there, so the tests play the cluster: they mark workloads ready and finish
// jobs, and can make a job fail or a rollout hang.

var (
	k8s    client.Client
	scheme = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Println("skipping controller tests: set KUBEBUILDER_ASSETS to the envtest binaries (make envtest)")
		os.Exit(0)
	}
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

type harness struct {
	t   *testing.T
	ctx context.Context
	r   *Reconciler
	key client.ObjectKey
	now time.Time
	// failJob makes the job whose name ends with this suffix fail
	failJob string
	// hang keeps a component's rollout from finishing once it runs this version
	hang map[string]string
}

func newHarness(t *testing.T, version string, cfg map[string]string, mutate ...func(*v1.PlatformStack)) *harness {
	t.Helper()
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "t-"}}
	if err := k8s.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	s := &v1.PlatformStack{
		ObjectMeta: metav1.ObjectMeta{Name: "site", Namespace: ns.Name},
		Spec: v1.PlatformStackSpec{
			Version:   version,
			Config:    cfg,
			Database:  v1.StorageSpec{Size: resource.MustParse("100Gi")},
			Messaging: v1.StorageSpec{Size: resource.MustParse("10Gi")},
		},
	}
	for _, m := range mutate {
		m(s)
	}
	if err := k8s.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, key: client.ObjectKeyFromObject(s), now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), hang: map[string]string{}}
	h.r = &Reconciler{Client: k8s, Scheme: scheme, Catalog: release.Default(), Images: render.Images{Registry: "registry.test/platform"}, Now: func() time.Time { return h.now }}
	return h
}

func legacyConfig() map[string]string {
	return map[string]string{"retention.days": "180", "export.format": "xml", "mq.prefetch": "50", "audit.level": "full"}
}

func (h *harness) stack() *v1.PlatformStack {
	var s v1.PlatformStack
	if err := k8s.Get(h.ctx, h.key, &s); err != nil {
		h.t.Fatal(err)
	}
	return &s
}

func (h *harness) reconcile() {
	h.t.Helper()
	if _, err := h.r.Reconcile(h.ctx, ctrl.Request{NamespacedName: h.key}); err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
}

// run alternates reconcile passes with the simulated cluster until the phase settles.
func (h *harness) run(until ...string) *v1.PlatformStack {
	h.t.Helper()
	for i := 0; i < 60; i++ {
		h.reconcile()
		h.cluster()
		s := h.stack()
		for _, p := range until {
			if s.Status.Phase == p && i > 0 {
				return s
			}
		}
		h.now = h.now.Add(30 * time.Second)
	}
	s := h.stack()
	h.t.Fatalf("phase did not settle on %v: %s, steps %+v", until, s.Status.Phase, s.Status.Steps)
	return nil
}

// cluster plays kubelet and the built-in controllers: rollouts finish, jobs complete.
func (h *harness) cluster() {
	var deps appsv1.DeploymentList
	_ = k8s.List(h.ctx, &deps, client.InNamespace(h.key.Namespace))
	for i := range deps.Items {
		d := &deps.Items[i]
		image := d.Spec.Template.Spec.Containers[0].Image
		if v, ok := h.hang[d.Labels["app.kubernetes.io/name"]]; ok && strings.HasSuffix(image, ":"+v) {
			continue
		}
		n := *d.Spec.Replicas
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: n, UpdatedReplicas: n, ReadyReplicas: n, AvailableReplicas: n}
		h.must(k8s.Status().Update(h.ctx, d))
	}
	var sss appsv1.StatefulSetList
	_ = k8s.List(h.ctx, &sss, client.InNamespace(h.key.Namespace))
	for i := range sss.Items {
		ss := &sss.Items[i]
		n := *ss.Spec.Replicas
		ss.Status = appsv1.StatefulSetStatus{ObservedGeneration: ss.Generation, Replicas: n, ReadyReplicas: n, UpdatedReplicas: n, AvailableReplicas: n, CurrentReplicas: n}
		h.must(k8s.Status().Update(h.ctx, ss))
	}
	var jobs batchv1.JobList
	_ = k8s.List(h.ctx, &jobs, client.InNamespace(h.key.Namespace))
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if len(j.Status.Conditions) > 0 {
			continue
		}
		start := metav1.NewTime(h.now.Add(-time.Minute))
		end := metav1.NewTime(h.now)
		j.Status.StartTime = &start
		if h.failJob != "" && strings.HasSuffix(j.Name, h.failJob) {
			j.Status.Failed = 1
			j.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "migration step 17 failed", LastTransitionTime: end},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "migration step 17 failed", LastTransitionTime: end},
			}
		} else {
			j.Status.Succeeded = 1
			j.Status.CompletionTime = &end
			j.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: end},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: end},
			}
		}
		h.must(k8s.Status().Update(h.ctx, j))
	}
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) facts(used string, queue int) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: h.key.Name + factsSuffix, Namespace: h.key.Namespace}}
	var existing corev1.ConfigMap
	if err := k8s.Get(h.ctx, client.ObjectKeyFromObject(cm), &existing); err == nil {
		existing.Data = map[string]string{"databaseUsed": used, "queueDepth": fmt.Sprint(queue)}
		h.must(k8s.Update(h.ctx, &existing))
		return
	}
	cm.Data = map[string]string{"databaseUsed": used, "queueDepth": fmt.Sprint(queue)}
	h.must(k8s.Create(h.ctx, cm))
}

func (h *harness) setVersion(v string) {
	s := h.stack()
	s.Spec.Version = v
	h.must(k8s.Update(h.ctx, s))
}

func (h *harness) image(component string) string {
	var d appsv1.Deployment
	h.must(k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: h.key.Name + "-" + component}, &d))
	return d.Spec.Template.Spec.Containers[0].Image
}

func (h *harness) jobExists(name string) bool {
	var j batchv1.Job
	return k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: h.key.Name + "-" + name}, &j) == nil
}

func kinds(s *v1.PlatformStack) []string {
	var out []string
	for _, st := range s.Status.Steps {
		out = append(out, st.Kind+"("+st.Version+")")
	}
	return out
}

func TestInstallCreatesARestrictedStack(t *testing.T) {
	h := newHarness(t, "4.3.0", legacyConfig())
	s := h.run(v1.PhaseReady)
	if s.Status.InstalledVersion != "4.3.0" || s.Status.SchemaVersion != 13 {
		t.Fatalf("installed %s schema %d", s.Status.InstalledVersion, s.Status.SchemaVersion)
	}
	var d appsv1.Deployment
	h.must(k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: "site-core"}, &d))
	pod := d.Spec.Template.Spec
	c := pod.Containers[0].SecurityContext
	if pod.SecurityContext.RunAsUser != nil || !*pod.SecurityContext.RunAsNonRoot || *c.AllowPrivilegeEscalation || !*c.ReadOnlyRootFilesystem || c.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("pod is not restricted-SCC ready: %+v %+v", pod.SecurityContext, c)
	}
	if d.OwnerReferences[0].Name != "site" {
		t.Fatal("objects must be owned by the stack")
	}
	var sec corev1.Secret
	h.must(k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: "site-credentials"}, &sec))
	if len(sec.Data["POSTGRES_PASSWORD"]) < 20 {
		t.Fatal("expected a generated password")
	}
	var cm corev1.ConfigMap
	h.must(k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: "site-config-4-3-0"}, &cm))
	if cm.Data["EXPORT_COMPRESSION"] != "none" || cm.Data["RETENTION_DAYS"] != "180" {
		t.Fatalf("config not migrated with defaults: %v", cm.Data)
	}
}

func TestPreflightBlocksAndChangesNothing(t *testing.T) {
	h := newHarness(t, "4.3.0", legacyConfig())
	h.run(v1.PhaseReady)
	h.facts("20Gi", 1500) // messages still queued before a broker upgrade
	h.setVersion("5.0.0")
	s := h.run(v1.PhaseBlocked)
	failed := []string{}
	for _, c := range s.Status.Preflight {
		if !c.OK {
			failed = append(failed, c.Name)
		}
	}
	if strings.Join(failed, ",") != "queues-drained" {
		t.Fatalf("failed checks = %v", failed)
	}
	if img := h.image("core"); !strings.HasSuffix(img, ":4.3.0") || h.jobExists("backup-13") {
		t.Fatalf("nothing may change on a failed preflight; core image %s", img)
	}
	// the queue drains: the next pass starts the upgrade
	h.facts("20Gi", 0)
	h.now = h.now.Add(2 * time.Minute)
	s = h.run(v1.PhaseReady)
	if s.Status.InstalledVersion != "5.0.0" {
		t.Fatalf("expected the upgrade to run once the queue drained, got %s", s.Status.InstalledVersion)
	}
}

func TestUpgradeRunsBackupMigrationAndOrderedRollout(t *testing.T) {
	h := newHarness(t, "4.3.0", legacyConfig())
	h.run(v1.PhaseReady)
	h.facts("20Gi", 0)
	h.setVersion("5.0.0")
	h.reconcile()
	s := h.stack()
	if got := strings.Join(kinds(s), " "); got != "Backup(5.0.0) Quiesce(5.0.0) Migrate(5.0.0) Messaging(5.0.0) Core(5.0.0) Gateway(5.0.0) Verify(5.0.0)" {
		t.Fatalf("plan = %s", got)
	}
	s = h.run(v1.PhaseReady)
	if s.Status.InstalledVersion != "5.0.0" || s.Status.SchemaVersion != 20 {
		t.Fatalf("installed %s schema %d", s.Status.InstalledVersion, s.Status.SchemaVersion)
	}
	if !h.jobExists("backup-13") || !h.jobExists("migrate-20") {
		t.Fatal("backup and migration jobs must have run")
	}
	if h.image("core") != "registry.test/platform/core:5.0.0" || h.image("gateway") != "registry.test/platform/gateway:5.0.0" {
		t.Fatal("services not on 5.0.0")
	}
	var cm corev1.ConfigMap
	h.must(k8s.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: "site-config-5-0-0"}, &cm))
	if cm.Data["HANDOVER_FORMAT"] != "xml" || cm.Data["MESSAGING_PREFETCH"] != "50" || cm.Data["EXPORT_FORMAT"] != "" {
		t.Fatalf("renamed keys not migrated: %v", cm.Data)
	}
	last := s.Status.History[len(s.Status.History)-1]
	if last.From != "4.3.0" || last.To != "5.0.0" || last.Result != "succeeded" {
		t.Fatalf("history = %+v", last)
	}
	if c := meta.FindStatusCondition(s.Status.Conditions, condUpgraded); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatal("UpgradeSucceeded condition missing")
	}
}

func TestFailedIrreversibleMigrationRestoresTheBackup(t *testing.T) {
	h := newHarness(t, "4.3.0", legacyConfig())
	h.run(v1.PhaseReady)
	h.facts("20Gi", 0)
	h.failJob = "migrate-20"
	h.setVersion("5.0.0")
	s := h.run(v1.PhaseRollingBack)
	if s.Status.RollbackTarget != "4.3.0" {
		t.Fatalf("rollback target %s", s.Status.RollbackTarget)
	}
	h.failJob = ""
	s = h.run(v1.PhaseReady)
	if s.Status.InstalledVersion != "4.3.0" || !h.jobExists("restore-13") {
		t.Fatalf("expected 4.3.0 restored from backup, got %s", s.Status.InstalledVersion)
	}
	if h.image("core") != "registry.test/platform/core:4.3.0" {
		t.Fatal("core not back on 4.3.0")
	}
	if last := s.Status.History[len(s.Status.History)-1]; last.Result != "rolled-back" {
		t.Fatalf("history = %+v", last)
	}
	// the spec still says 5.0.0: the operator must not retry until the spec changes
	h.reconcile()
	if s := h.stack(); s.Status.Phase != v1.PhaseReady || len(s.Status.Steps) != 0 {
		t.Fatalf("retried a rolled-back upgrade: %s %v", s.Status.Phase, kinds(s))
	}
}

func TestStalledRolloutTimesOutAndReversesAReversibleMigration(t *testing.T) {
	cfg := map[string]string{"retention.days": "90", "handover.format": "asn1"}
	h := newHarness(t, "5.0.0", cfg, func(s *v1.PlatformStack) { s.Spec.Upgrade.StepTimeoutSeconds = 300 })
	h.run(v1.PhaseReady)
	h.facts("20Gi", 0)
	h.hang["gateway"] = "5.1.0" // the new gateway never becomes ready
	h.setVersion("5.1.0")
	s := h.run(v1.PhaseRollingBack)
	if got := strings.Join(kinds(s)[len(kinds(s))-5:], " "); got != "Gateway(5.0.0) Quiesce(5.0.0) MigrateDown(5.0.0) Core(5.0.0) Verify(5.0.0)" {
		t.Fatalf("rollback plan = %s", got)
	}
	s = h.run(v1.PhaseReady)
	if s.Status.InstalledVersion != "5.0.0" || s.Status.SchemaVersion != 20 || !h.jobExists("migrate-down-20") {
		t.Fatalf("expected 5.0.0 with schema 20 after a down-migration; got %s %d", s.Status.InstalledVersion, s.Status.SchemaVersion)
	}
}

func TestWithoutAutoRollbackAFailureWaitsForAPerson(t *testing.T) {
	h := newHarness(t, "4.3.0", legacyConfig(), func(s *v1.PlatformStack) { s.Spec.Upgrade.DisableAutoRollback = true })
	h.run(v1.PhaseReady)
	h.facts("20Gi", 0)
	h.failJob = "migrate-20"
	h.setVersion("5.0.0")
	s := h.run(v1.PhaseFailed)
	if c := meta.FindStatusCondition(s.Status.Conditions, condReady); c == nil || !strings.Contains(c.Message, "migrate-20 failed") {
		t.Fatalf("the condition must say what failed: %+v", c)
	}
}

func TestInvalidConfigBlocksTheInstall(t *testing.T) {
	cfg := legacyConfig()
	cfg["retention.day"] = "30"
	h := newHarness(t, "4.3.0", cfg)
	h.reconcile()
	s := h.stack()
	if s.Status.Phase != v1.PhaseBlocked || !strings.Contains(meta.FindStatusCondition(s.Status.Conditions, condReady).Message, "did you mean retention.days") {
		t.Fatalf("expected a blocked install with a hint, got %s %+v", s.Status.Phase, s.Status.Conditions)
	}
}
