// Package controller reconciles PlatformStack objects: install, preflight, upgrade step by step,
// and roll back to the last verified release when a step fails.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/plan"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/render"
)

const (
	postgresPort int32 = 5432
	rabbitPort   int32 = 5672
	corePort     int32 = 8080
	gatewayPort  int32 = 8443

	defaultReplicas     int32 = 2
	defaultStepTimeout        = 900 * time.Second
	requeueWhileBusy          = 5 * time.Second
	requeueWhileBlocked       = time.Minute

	// FactsConfigMap suffix: the core service publishes database size and queue depth here.
	factsSuffix = "-facts"

	condReady    = "Ready"
	condUpgraded = "UpgradeSucceeded"
)

// Reconciler drives a PlatformStack towards its spec.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Catalog  *release.Catalog
	Images   render.Images
	Recorder record.EventRecorder
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=platform.stacklifecycle.io,resources=platformstacks;platformstacks/status,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=services;configmaps;persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update

// SetupWithManager registers the controller and the objects it owns.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Now == nil {
		r.Now = time.Now
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.PlatformStack{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Named("platformstack").
		Complete(r)
}

// Reconcile is one pass: observe, decide, act on at most one step, record status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.Now == nil {
		r.Now = time.Now
	}
	var s v1.PlatformStack
	if err := r.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := s.Status.DeepCopy()
	res, err := r.reconcile(ctx, &s)
	s.Status.ObservedGeneration = s.Generation
	if !equalStatus(before, &s.Status) {
		if uerr := r.Status().Update(ctx, &s); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, uerr
		}
	}
	return res, err
}

func (r *Reconciler) reconcile(ctx context.Context, s *v1.PlatformStack) (ctrl.Result, error) {
	if _, ok := r.Catalog.Get(s.Spec.Version); !ok {
		r.setPhase(s, v1.PhaseBlocked, "UnknownVersion", fmt.Sprintf("release %q is not in the catalog", s.Spec.Version))
		return ctrl.Result{}, nil
	}
	if err := r.ensureBase(ctx, s); err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case s.Status.Phase == v1.PhaseUpgrading || s.Status.Phase == v1.PhaseRollingBack:
		return r.runSteps(ctx, s)
	case s.Status.Phase == v1.PhaseFailed:
		return ctrl.Result{}, nil // a person decides; see the conditions
	case s.Status.InstalledVersion == "":
		return r.install(ctx, s)
	case s.Spec.Version != s.Status.InstalledVersion && !rolledBackThisGeneration(s):
		return r.startUpgrade(ctx, s)
	default:
		return r.steady(ctx, s)
	}
}

// ensureBase creates what every release shares: credentials, the database and broker services.
func (r *Reconciler) ensureBase(ctx context.Context, s *v1.PlatformStack) error {
	if err := r.ensureCredentials(ctx, s); err != nil {
		return err
	}
	for _, svc := range []*corev1.Service{
		render.Service(s, render.Postgres, postgresPort),
		render.Service(s, render.RabbitMQ, rabbitPort),
		render.Service(s, render.Core, corePort),
		render.Service(s, render.Gateway, gatewayPort),
	} {
		if err := r.createIfMissing(ctx, s, svc); err != nil {
			return err
		}
	}
	return nil
}

// ensureCredentials creates random database and broker passwords once. They are never logged
// and never overwritten, so rotating them is an explicit action.
func (r *Reconciler) ensureCredentials(ctx context.Context, s *v1.PlatformStack) error {
	name := render.Name(s, "credentials")
	var existing corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &existing)
	if err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: render.Labels(s, "credentials")},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"POSTGRES_PASSWORD": randomSecret(), "RABBITMQ_DEFAULT_PASS": randomSecret(), "POSTGRES_USER": "platform", "RABBITMQ_DEFAULT_USER": "platform"},
	}
	return r.createIfMissing(ctx, s, sec)
}

func (r *Reconciler) install(ctx context.Context, s *v1.PlatformStack) (ctrl.Result, error) {
	rel, _ := r.Catalog.Get(s.Spec.Version)
	cfg, findings := plan.MigrateConfig(r.Catalog, s.Spec.Config, []string{rel.Version})
	if msg := errorsOf(findings); msg != "" {
		r.setPhase(s, v1.PhaseBlocked, "InvalidConfig", msg)
		return ctrl.Result{RequeueAfter: requeueWhileBlocked}, nil
	}
	if s.Status.Phase != v1.PhaseInstalling {
		r.setPhase(s, v1.PhaseInstalling, "Installing", "installing "+rel.Version)
		s.Status.TargetVersion = rel.Version
		setConfig(&s.Status, rel.Version, cfg)
		r.event(s, corev1.EventTypeNormal, "Installing", "installing release "+rel.Version)
	}
	cfg, _ = s.Status.ConfigFor(rel.Version)
	if err := r.applyRelease(ctx, s, rel, cfg, r.coreReplicas(s)); err != nil {
		return ctrl.Result{}, err
	}
	ready, err := r.allReady(ctx, s)
	if err != nil || !ready {
		return ctrl.Result{RequeueAfter: requeueWhileBusy}, err
	}
	s.Status.InstalledVersion = rel.Version
	s.Status.SchemaVersion = rel.Schema
	s.Status.TargetVersion = ""
	r.setPhase(s, v1.PhaseReady, "Installed", "release "+rel.Version+" is running")
	r.event(s, corev1.EventTypeNormal, "Installed", "release "+rel.Version+" is running")
	return ctrl.Result{}, nil
}

// applyRelease brings every component to a release with the given config.
func (r *Reconciler) applyRelease(ctx context.Context, s *v1.PlatformStack, rel release.Release, cfg map[string]string, coreReplicas int32) error {
	if err := r.applyConfig(ctx, s, rel.Version, cfg); err != nil {
		return err
	}
	for _, obj := range r.workloads(s, rel, cfg, coreReplicas) {
		if err := r.apply(ctx, s, obj); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) workloads(s *v1.PlatformStack, rel release.Release, cfg map[string]string, coreReplicas int32) []client.Object {
	return []client.Object{
		render.StatefulSet(s, render.Postgres, r.Images.Image(render.Postgres, "16"), postgresPort, s.Spec.Database, render.Name(s, "credentials")),
		render.StatefulSet(s, render.RabbitMQ, r.Images.Image(render.RabbitMQ, rel.Messaging), rabbitPort, s.Spec.Messaging, render.Name(s, "credentials")),
		r.deployment(s, render.Core, rel.Version, cfg, coreReplicas),
		r.deployment(s, render.Gateway, rel.Version, cfg, r.gatewayReplicas(s)),
	}
}

// Manifests returns every object the operator creates for a stack at its spec version, plus the
// lifecycle jobs of an upgrade, so they can be linted against OpenShift's restricted policy
// without a cluster. The credentials Secret is left out: its values only exist in the cluster.
func (r *Reconciler) Manifests(s *v1.PlatformStack) ([]client.Object, error) {
	rel, ok := r.Catalog.Get(s.Spec.Version)
	if !ok {
		return nil, fmt.Errorf("unknown release %s", s.Spec.Version)
	}
	cfg, findings := plan.MigrateConfig(r.Catalog, s.Spec.Config, []string{rel.Version})
	if msg := errorsOf(findings); msg != "" {
		return nil, fmt.Errorf("invalid config: %s", msg)
	}
	objs := []client.Object{
		render.Service(s, render.Postgres, postgresPort),
		render.Service(s, render.RabbitMQ, rabbitPort),
		render.Service(s, render.Core, corePort),
		render.Service(s, render.Gateway, gatewayPort),
		render.ConfigMap(s, rel.Version, cfg),
		render.BackupPVC(s),
	}
	objs = append(objs, r.workloads(s, rel, cfg, r.coreReplicas(s))...)
	objs = append(objs,
		render.Job(s, render.JobName(s, "backup", rel.Schema), r.Images.Image(render.Tools, rel.Version), []string{"backup", "--schema", strconv.Itoa(rel.Schema), "--to", "/backup"}, true),
		render.Job(s, render.JobName(s, "migrate", rel.Schema), r.Images.Image(render.Core, rel.Version), []string{"migrate", "--to", strconv.Itoa(rel.Schema)}, false),
	)
	for _, o := range objs {
		o.SetNamespace(s.Namespace)
	}
	return objs, nil
}

func (r *Reconciler) deployment(s *v1.PlatformStack, component, version string, cfg map[string]string, replicas int32) *appsv1.Deployment {
	port := corePort
	if component == render.Gateway {
		port = gatewayPort
	}
	return render.Deployment(s, component, r.Images.Image(component, version), render.ConfigName(s, version), cfg, replicas, port)
}

func (r *Reconciler) applyConfig(ctx context.Context, s *v1.PlatformStack, version string, cfg map[string]string) error {
	return r.apply(ctx, s, render.ConfigMap(s, version, cfg))
}

// startUpgrade runs the preflight and, if it passes, records the plan. Nothing is changed when
// the preflight fails.
func (r *Reconciler) startUpgrade(ctx context.Context, s *v1.PlatformStack) (ctrl.Result, error) {
	from, to := s.Status.InstalledVersion, s.Spec.Version
	pol := r.policy(s)
	facts, err := r.facts(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}
	current, _ := s.Status.ConfigFor(from)
	cfg := mergeSpec(current, s.Spec.Config)
	pf := plan.RunPreflight(r.Catalog, from, to, cfg, facts, pol)
	s.Status.Preflight = toChecks(pf.Checks)
	if !pol.Preflight {
		// still need a path and a migrated config to act on
		if len(pf.Path) == 0 {
			r.setPhase(s, v1.PhaseBlocked, "NoUpgradePath", pf.Checks[0].Message)
			return ctrl.Result{}, nil
		}
	} else if !pf.OK() {
		r.setPhase(s, v1.PhaseBlocked, "PreflightFailed", fmt.Sprintf("preflight failed: %v; nothing was changed", pf.Failed()))
		r.event(s, corev1.EventTypeWarning, "PreflightFailed", fmt.Sprintf("upgrade %s → %s blocked: %v", from, to, pf.Failed()))
		return ctrl.Result{RequeueAfter: requeueWhileBlocked}, nil
	}
	// configs for every release on the path, so each step and any rollback knows its config
	for i, v := range pf.Path {
		c, _ := plan.MigrateConfig(r.Catalog, cfg, pf.Path[:i+1])
		setConfig(&s.Status, v, c)
	}
	steps := plan.Build(r.Catalog, from, pf.Path, pol)
	s.Status.Steps = toSteps(steps)
	s.Status.Current = 0
	s.Status.TargetVersion = to
	s.Status.UpgradeFrom = from
	s.Status.RollbackTarget = ""
	now := metav1.NewTime(r.Now())
	s.Status.UpgradeStarted = &now
	r.setPhase(s, v1.PhaseUpgrading, "Upgrading", fmt.Sprintf("upgrading %s → %s in %d steps", from, to, len(steps)))
	r.event(s, corev1.EventTypeNormal, "UpgradeStarted", fmt.Sprintf("%s → %s: %v", from, to, steps))
	return ctrl.Result{Requeue: true}, nil
}

// runSteps works on the current step: make it so, check whether it finished, move on, or fail.
func (r *Reconciler) runSteps(ctx context.Context, s *v1.PlatformStack) (ctrl.Result, error) {
	if s.Status.Current >= len(s.Status.Steps) {
		return r.finish(s)
	}
	st := &s.Status.Steps[s.Status.Current]
	if st.State == v1.StepPending {
		now := metav1.NewTime(r.Now())
		st.State, st.StartedAt = v1.StepRunning, &now
	}
	done, failMsg, err := r.execute(ctx, s, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	if failMsg == "" && !done && st.StartedAt != nil && r.Now().Sub(st.StartedAt.Time) > r.timeout(s) {
		failMsg = fmt.Sprintf("%s(%s) did not finish within %s", st.Kind, st.Version, r.timeout(s))
	}
	switch {
	case failMsg != "":
		st.State, st.Message = v1.StepFailed, failMsg
		return r.fail(s, failMsg)
	case done:
		st.State = v1.StepDone
		s.Status.Current++
		return ctrl.Result{Requeue: true}, nil
	default:
		return ctrl.Result{RequeueAfter: requeueWhileBusy}, nil
	}
}

// execute applies one step and reports whether it is complete, or why it failed.
func (r *Reconciler) execute(ctx context.Context, s *v1.PlatformStack, st *v1.StepStatus) (bool, string, error) {
	rel, _ := r.Catalog.Get(st.Version)
	cfg, _ := s.Status.ConfigFor(st.Version)
	switch plan.Kind(st.Kind) {
	case plan.Backup:
		if err := r.createIfMissing(ctx, s, render.BackupPVC(s)); err != nil {
			return false, "", err
		}
		return r.runJob(ctx, s, render.JobName(s, "backup", st.Schema), r.Images.Image(render.Tools, st.Version), []string{"backup", "--schema", strconv.Itoa(st.Schema), "--to", "/backup"}, true)
	case plan.Quiesce:
		d, err := r.getDeployment(ctx, s, render.Core)
		if err != nil {
			return false, "", err
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			zero := int32(0)
			d.Spec.Replicas = &zero
			if err := r.Update(ctx, d); err != nil {
				return false, "", err
			}
		}
		return d.Status.Replicas == 0, "", nil
	case plan.Migrate:
		return r.runJob(ctx, s, render.JobName(s, "migrate", st.Schema), r.Images.Image(render.Core, st.Version), []string{"migrate", "--to", strconv.Itoa(st.Schema)}, false)
	case plan.MigrateDown:
		// the newer release knows how to reverse its own migration
		return r.runJob(ctx, s, render.JobName(s, "migrate-down", st.Schema), r.Images.Image(render.Core, s.Status.TargetVersion), []string{"migrate", "--to", strconv.Itoa(st.Schema)}, false)
	case plan.RestoreBackup:
		return r.runJob(ctx, s, render.JobName(s, "restore", st.Schema), r.Images.Image(render.Tools, s.Status.TargetVersion), []string{"restore", "--schema", strconv.Itoa(st.Schema), "--from", "/backup"}, true)
	case plan.Messaging:
		ss := render.StatefulSet(s, render.RabbitMQ, r.Images.Image(render.RabbitMQ, rel.Messaging), rabbitPort, s.Spec.Messaging, render.Name(s, "credentials"))
		if err := r.apply(ctx, s, ss); err != nil {
			return false, "", err
		}
		ok, err := r.statefulSetReady(ctx, s, render.RabbitMQ)
		return ok, "", err
	case plan.Core, plan.Gateway:
		component := render.Core
		replicas := r.coreReplicas(s)
		if plan.Kind(st.Kind) == plan.Gateway {
			component, replicas = render.Gateway, r.gatewayReplicas(s)
		}
		if err := r.applyConfig(ctx, s, st.Version, cfg); err != nil {
			return false, "", err
		}
		if err := r.apply(ctx, s, r.deployment(s, component, st.Version, cfg, replicas)); err != nil {
			return false, "", err
		}
		return r.rolledOut(ctx, s, component)
	case plan.Verify:
		ok, err := r.allReady(ctx, s)
		if ok {
			s.Status.InstalledVersion = st.Version
			s.Status.SchemaVersion = rel.Schema
		}
		return ok, "", err
	case plan.ManualRestore:
		return false, "the database migration cannot be reversed and no backup was taken; restore it by hand", nil
	}
	return false, "unknown step " + st.Kind, nil
}

// runJob creates a lifecycle job once and reports its outcome.
func (r *Reconciler) runJob(ctx context.Context, s *v1.PlatformStack, name, image string, args []string, withBackup bool) (bool, string, error) {
	var j batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &j)
	if apierrors.IsNotFound(err) {
		return false, "", r.createIfMissing(ctx, s, render.Job(s, name, image, args, withBackup))
	}
	if err != nil {
		return false, "", err
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, "", nil
		case batchv1.JobFailed:
			return false, fmt.Sprintf("job %s failed: %s", name, c.Message), nil
		}
	}
	return false, "", nil
}

// fail starts a rollback, or stops for a person when rollback is off or impossible.
func (r *Reconciler) fail(s *v1.PlatformStack, msg string) (ctrl.Result, error) {
	r.event(s, corev1.EventTypeWarning, "StepFailed", msg)
	if s.Status.Phase == v1.PhaseRollingBack || !r.policy(s).AutoRollback {
		r.setPhase(s, v1.PhaseFailed, "StepFailed", msg)
		r.record(s, "failed", msg)
		return ctrl.Result{}, nil
	}
	steps := fromStatus(s.Status.Steps)
	target, rb := plan.Rollback(r.Catalog, startVersion(s), steps, s.Status.Current)
	s.Status.RollbackTarget = target
	// the forward steps after the failed one are abandoned; continue with the first rollback step
	for i := s.Status.Current + 1; i < len(s.Status.Steps); i++ {
		s.Status.Steps[i].State = v1.StepSkipped
	}
	s.Status.Current = len(s.Status.Steps)
	s.Status.Steps = append(s.Status.Steps, toSteps(rb)...)
	r.setPhase(s, v1.PhaseRollingBack, "RollingBack", fmt.Sprintf("%s; rolling back to %s", msg, target))
	return ctrl.Result{Requeue: true}, nil
}

func (r *Reconciler) finish(s *v1.PlatformStack) (ctrl.Result, error) {
	if s.Status.Phase == v1.PhaseRollingBack {
		msg := fmt.Sprintf("rolled back to %s after a failed step", s.Status.RollbackTarget)
		r.record(s, "rolled-back", msg)
		r.setPhase(s, v1.PhaseReady, "RolledBack", msg)
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: condUpgraded, Status: metav1.ConditionFalse, Reason: "RolledBack", Message: msg, ObservedGeneration: s.Generation})
		r.event(s, corev1.EventTypeWarning, "RolledBack", msg)
		return ctrl.Result{}, nil
	}
	msg := "upgraded to " + s.Status.InstalledVersion
	r.record(s, "succeeded", msg)
	r.setPhase(s, v1.PhaseReady, "Upgraded", msg)
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: condUpgraded, Status: metav1.ConditionTrue, Reason: "Upgraded", Message: msg, ObservedGeneration: s.Generation})
	r.event(s, corev1.EventTypeNormal, "Upgraded", msg)
	return ctrl.Result{}, nil
}

// record adds the finished upgrade to the history and clears the plan.
func (r *Reconciler) record(s *v1.PlatformStack, result, msg string) {
	started := metav1.NewTime(r.Now())
	if s.Status.UpgradeStarted != nil {
		started = *s.Status.UpgradeStarted
	}
	s.Status.History = append(s.Status.History, v1.UpgradeRecord{From: startVersion(s), To: s.Status.TargetVersion, Result: result, Message: msg, Started: started, Finished: metav1.NewTime(r.Now())})
	if len(s.Status.History) > 20 {
		s.Status.History = s.Status.History[len(s.Status.History)-20:]
	}
	if result != "failed" {
		s.Status.Steps, s.Status.Current, s.Status.UpgradeStarted = nil, 0, nil
		s.Status.TargetVersion, s.Status.RollbackTarget, s.Status.UpgradeFrom = "", "", ""
	}
}

// steady keeps a Ready platform on its installed release; config changes for the same release
// are validated and rolled out.
func (r *Reconciler) steady(ctx context.Context, s *v1.PlatformStack) (ctrl.Result, error) {
	rel, _ := r.Catalog.Get(s.Status.InstalledVersion)
	current, _ := s.Status.ConfigFor(rel.Version)
	cfg, findings := plan.MigrateConfig(r.Catalog, mergeSpec(current, s.Spec.Config), []string{rel.Version})
	if msg := errorsOf(findings); msg != "" {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: "ConfigValid", Status: metav1.ConditionFalse, Reason: "InvalidConfig", Message: msg, ObservedGeneration: s.Generation})
		cfg = current // keep running the last good config
	} else {
		meta.RemoveStatusCondition(&s.Status.Conditions, "ConfigValid")
		setConfig(&s.Status, rel.Version, cfg)
	}
	if err := r.applyRelease(ctx, s, rel, cfg, r.coreReplicas(s)); err != nil {
		return ctrl.Result{}, err
	}
	ready, err := r.allReady(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}
	if s.Status.Phase == "" || s.Status.Phase == v1.PhaseBlocked {
		r.setPhase(s, v1.PhaseReady, "Running", "release "+rel.Version+" is running")
	}
	status, reason := metav1.ConditionTrue, "AllComponentsReady"
	if !ready {
		status, reason = metav1.ConditionFalse, "ComponentsNotReady"
	}
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: condReady, Status: status, Reason: reason, Message: "release " + rel.Version, ObservedGeneration: s.Generation})
	return ctrl.Result{}, nil
}

// facts gathers what the preflight needs: workload health, database size and queue depth. The
// core service publishes the last two in the <stack>-facts ConfigMap.
func (r *Reconciler) facts(ctx context.Context, s *v1.PlatformStack) (plan.Facts, error) {
	ready, err := r.allReady(ctx, s)
	if err != nil {
		return plan.Facts{}, err
	}
	f := plan.Facts{ComponentsReady: ready, DatabaseCapacityBytes: s.Spec.Database.Size.Value()}
	var cm corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name + factsSuffix}, &cm)
	if apierrors.IsNotFound(err) {
		// unknown sizes count as a full volume and a non-empty queue: no guessing in our favour
		f.DatabaseUsedBytes = f.DatabaseCapacityBytes
		f.QueueDepth = -1
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if q, err := resource.ParseQuantity(cm.Data["databaseUsed"]); err == nil {
		f.DatabaseUsedBytes = q.Value()
	} else {
		f.DatabaseUsedBytes = f.DatabaseCapacityBytes
	}
	if n, err := strconv.ParseInt(cm.Data["queueDepth"], 10, 64); err == nil {
		f.QueueDepth = n
	} else {
		f.QueueDepth = -1
	}
	return f, nil
}

func (r *Reconciler) allReady(ctx context.Context, s *v1.PlatformStack) (bool, error) {
	for _, c := range []string{render.Postgres, render.RabbitMQ} {
		ok, err := r.statefulSetReady(ctx, s, c)
		if err != nil || !ok {
			return false, err
		}
	}
	for _, c := range []string{render.Core, render.Gateway} {
		ok, _, err := r.rolledOut(ctx, s, c)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (r *Reconciler) statefulSetReady(ctx context.Context, s *v1.PlatformStack, component string) (bool, error) {
	var ss appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: render.Name(s, component)}, &ss); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	want := int32(1)
	if ss.Spec.Replicas != nil {
		want = *ss.Spec.Replicas
	}
	return ss.Status.ObservedGeneration >= ss.Generation && ss.Status.ReadyReplicas == want && ss.Status.UpdatedReplicas == want, nil
}

// rolledOut is true when a Deployment runs its current spec on every replica.
func (r *Reconciler) rolledOut(ctx context.Context, s *v1.PlatformStack, component string) (bool, string, error) {
	d, err := r.getDeployment(ctx, s, component)
	if apierrors.IsNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded" {
			return false, fmt.Sprintf("%s rollout stalled: %s", component, c.Message), nil
		}
	}
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	ok := d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want && d.Status.AvailableReplicas == want && d.Status.Replicas == want
	return ok, "", nil
}

func (r *Reconciler) getDeployment(ctx context.Context, s *v1.PlatformStack, component string) (*appsv1.Deployment, error) {
	var d appsv1.Deployment
	err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: render.Name(s, component)}, &d)
	return &d, err
}

// apply creates an object or brings its spec in line with the desired one.
func (r *Reconciler) apply(ctx context.Context, s *v1.PlatformStack, desired client.Object) error {
	current := desired.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, current, func() error {
		mutate(current, desired)
		return controllerutil.SetControllerReference(s, current, r.Scheme)
	})
	return err
}

func (r *Reconciler) createIfMissing(ctx context.Context, s *v1.PlatformStack, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing); err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(s, obj, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// mutate copies the fields the operator owns from desired into current, leaving fields the API
// server or other controllers set (status, defaults, cluster IPs) alone.
func mutate(current, desired client.Object) {
	current.SetLabels(desired.GetLabels())
	switch c := current.(type) {
	case *appsv1.Deployment:
		d := desired.(*appsv1.Deployment)
		c.Spec.Replicas = d.Spec.Replicas
		c.Spec.Selector = d.Spec.Selector
		c.Spec.Strategy = d.Spec.Strategy
		c.Spec.Template = d.Spec.Template
	case *appsv1.StatefulSet:
		d := desired.(*appsv1.StatefulSet)
		if c.CreationTimestamp.IsZero() {
			c.Spec = d.Spec
		} else {
			// selector, serviceName and volume claim templates are immutable after creation
			c.Spec.Replicas = d.Spec.Replicas
			c.Spec.Template = d.Spec.Template
		}
	case *corev1.ConfigMap:
		c.Data = desired.(*corev1.ConfigMap).Data
	}
}

func (r *Reconciler) setPhase(s *v1.PlatformStack, phase, reason, msg string) {
	s.Status.Phase = phase
	status := metav1.ConditionFalse
	if phase == v1.PhaseReady {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: condReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: s.Generation})
	log.Log.WithName("platformstack").Info(msg, "stack", s.Namespace+"/"+s.Name, "phase", phase)
}

func (r *Reconciler) event(s *v1.PlatformStack, typ, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(s, typ, reason, msg)
	}
}

func (r *Reconciler) policy(s *v1.PlatformStack) plan.Policy {
	p := plan.DefaultPolicy()
	p.Preflight = !s.Spec.Upgrade.SkipPreflight
	p.BackupBeforeMigration = !s.Spec.Upgrade.SkipBackup
	p.AutoRollback = !s.Spec.Upgrade.DisableAutoRollback
	return p
}

func (r *Reconciler) timeout(s *v1.PlatformStack) time.Duration {
	if s.Spec.Upgrade.StepTimeoutSeconds > 0 {
		return time.Duration(s.Spec.Upgrade.StepTimeoutSeconds) * time.Second
	}
	return defaultStepTimeout
}

func (r *Reconciler) coreReplicas(s *v1.PlatformStack) int32 {
	if s.Spec.CoreReplicas > 0 {
		return s.Spec.CoreReplicas
	}
	return defaultReplicas
}

func (r *Reconciler) gatewayReplicas(s *v1.PlatformStack) int32 {
	if s.Spec.GatewayReplicas > 0 {
		return s.Spec.GatewayReplicas
	}
	return defaultReplicas
}

// startVersion is the release that was installed when the current upgrade began.
func startVersion(s *v1.PlatformStack) string {
	if s.Status.UpgradeFrom != "" {
		return s.Status.UpgradeFrom
	}
	return s.Status.InstalledVersion
}

// rolledBackThisGeneration stops the operator from retrying an upgrade that was just rolled
// back. Changing the spec (a fixed config, another version) starts a new attempt.
func rolledBackThisGeneration(s *v1.PlatformStack) bool {
	c := meta.FindStatusCondition(s.Status.Conditions, condUpgraded)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "RolledBack" && c.ObservedGeneration == s.Generation
}

func setConfig(st *v1.PlatformStackStatus, version string, cfg map[string]string) {
	for i := range st.Configs {
		if st.Configs[i].Version == version {
			st.Configs[i].Config = cfg
			return
		}
	}
	st.Configs = append(st.Configs, v1.VersionConfig{Version: version, Config: cfg})
}

// mergeSpec overlays the spec's config on the last applied config of the installed release.
func mergeSpec(applied, spec map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range applied {
		out[k] = v
	}
	for k, v := range spec {
		out[k] = v
	}
	return out
}

func errorsOf(fs []plan.Finding) string {
	msg := ""
	for _, f := range fs {
		if f.Severity == plan.Error {
			if msg != "" {
				msg += "; "
			}
			msg += f.Message
		}
	}
	return msg
}

func toChecks(cs []plan.Check) []v1.CheckResult {
	out := make([]v1.CheckResult, len(cs))
	for i, c := range cs {
		out[i] = v1.CheckResult{Name: c.Name, OK: c.OK, Message: c.Message}
	}
	return out
}

func toSteps(ss []plan.Step) []v1.StepStatus {
	out := make([]v1.StepStatus, len(ss))
	for i, s := range ss {
		out[i] = v1.StepStatus{Kind: string(s.Kind), Version: s.Version, Schema: s.Schema, State: v1.StepPending}
	}
	return out
}

func fromStatus(ss []v1.StepStatus) []plan.Step {
	out := make([]plan.Step, len(ss))
	for i, s := range ss {
		out[i] = plan.Step{Kind: plan.Kind(s.Kind), Version: s.Version, Schema: s.Schema}
	}
	return out
}

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func equalStatus(a, b *v1.PlatformStackStatus) bool {
	return equality.Semantic.DeepEqual(a, b)
}
