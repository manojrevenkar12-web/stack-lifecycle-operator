// Package render builds the Kubernetes objects of a PlatformStack. Every pod runs under the
// OpenShift restricted-v2 SCC and the Kubernetes "restricted" Pod Security Standard: non-root with
// a UID chosen by the platform, no privilege escalation, all capabilities dropped, RuntimeDefault
// seccomp, read-only root filesystem with an emptyDir for scratch space.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
)

// Component names.
const (
	Postgres = "postgres"
	RabbitMQ = "rabbitmq"
	Core     = "core"
	Gateway  = "gateway"
	Tools    = "db-tools"
)

// ManagedBy labels every object the operator owns.
const ManagedBy = "stack-lifecycle-operator"

// Images resolves image references from one registry.
type Images struct{ Registry string }

// Image of a component at a version.
func (i Images) Image(component, version string) string {
	return fmt.Sprintf("%s/%s:%s", i.Registry, component, version)
}

// Name of a component's object for a stack.
func Name(s *v1.PlatformStack, component string) string { return s.Name + "-" + component }

// ConfigName is the ConfigMap holding the migrated config of one release.
func ConfigName(s *v1.PlatformStack, version string) string {
	return fmt.Sprintf("%s-config-%s", s.Name, sanitize(version))
}

// BackupClaim is the volume that holds database backups.
func BackupClaim(s *v1.PlatformStack) string { return s.Name + "-backup" }

// JobName of a lifecycle job, e.g. migrate to schema 20.
func JobName(s *v1.PlatformStack, action string, schema int) string {
	return fmt.Sprintf("%s-%s-%d", s.Name, action, schema)
}

// Labels for a component.
func Labels(s *v1.PlatformStack, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       component,
		"app.kubernetes.io/instance":   s.Name,
		"app.kubernetes.io/part-of":    "platform",
		"app.kubernetes.io/managed-by": ManagedBy,
	}
}

func meta(s *v1.PlatformStack, name, component string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: Labels(s, component)}
}

// PodSecurity is the pod-level security context: no fixed UID, so OpenShift can assign one.
func PodSecurity() *corev1.PodSecurityContext {
	yes := true
	return &corev1.PodSecurityContext{RunAsNonRoot: &yes, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

// ContainerSecurity is the container-level security context.
func ContainerSecurity() *corev1.SecurityContext {
	no, yes := false, true
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &no,
		ReadOnlyRootFilesystem:   &yes,
		RunAsNonRoot:             &yes,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func scratch() (corev1.Volume, corev1.VolumeMount) {
	return corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"}
}

func resources(cpu, mem string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)},
	}
}

func claim(name string, st v1.StorageSpec) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: st.StorageClassName,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: st.Size}},
		},
	}
}

func probe(port int32, path string) *corev1.Probe {
	h := corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}
	if path != "" {
		h = corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port)}}
	}
	return &corev1.Probe{ProbeHandler: h, PeriodSeconds: 10, FailureThreshold: 3}
}

// StatefulSet for the database or the broker.
func StatefulSet(s *v1.PlatformStack, component, image string, port int32, st v1.StorageSpec, secret string) *appsv1.StatefulSet {
	one := int32(1)
	tmpV, tmpM := scratch()
	name := Name(s, component)
	ss := &appsv1.StatefulSet{
		ObjectMeta: meta(s, name, component),
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &one,
			ServiceName: name,
			Selector:    &metav1.LabelSelector{MatchLabels: Labels(s, component)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: Labels(s, component)},
				Spec: corev1.PodSpec{
					SecurityContext:              PodSecurity(),
					AutomountServiceAccountToken: ptr(false),
					Volumes:                      []corev1.Volume{tmpV},
					Containers: []corev1.Container{{
						Name:            component,
						Image:           image,
						Ports:           []corev1.ContainerPort{{Name: component, ContainerPort: port}},
						SecurityContext: ContainerSecurity(),
						Resources:       resources("250m", "512Mi"),
						ReadinessProbe:  probe(port, ""),
						EnvFrom:         []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret}}}},
						VolumeMounts:    []corev1.VolumeMount{tmpM, {Name: "data", MountPath: "/var/lib/" + component}},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{claim("data", st)},
		},
	}
	return ss
}

// Deployment for the core service or the gateway at a release, reading that release's config.
// The config hash in the pod template restarts pods when the config changes.
func Deployment(s *v1.PlatformStack, component, image, configMap string, cfg map[string]string, replicas int32, port int32) *appsv1.Deployment {
	tmpV, tmpM := scratch()
	maxUnavailable := intstr.FromInt32(0)
	maxSurge := intstr.FromInt32(1)
	return &appsv1.Deployment{
		ObjectMeta: meta(s, Name(s, component), component),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: Labels(s, component)},
			Strategy: appsv1.DeploymentStrategy{
				Type:          appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &maxUnavailable, MaxSurge: &maxSurge},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: Labels(s, component), Annotations: map[string]string{"platform.stacklifecycle.io/config-hash": Hash(cfg)}},
				Spec: corev1.PodSpec{
					SecurityContext:              PodSecurity(),
					AutomountServiceAccountToken: ptr(false),
					Volumes:                      []corev1.Volume{tmpV},
					Containers: []corev1.Container{{
						Name:            component,
						Image:           image,
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
						SecurityContext: ContainerSecurity(),
						Resources:       resources("200m", "256Mi"),
						ReadinessProbe:  probe(port, "/healthz"),
						EnvFrom: []corev1.EnvFromSource{
							{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap}}},
							{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: Name(s, "credentials")}}},
						},
						VolumeMounts: []corev1.VolumeMount{tmpM},
					}},
				},
			},
		},
	}
}

// Service exposes a component inside the cluster.
func Service(s *v1.PlatformStack, component string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: meta(s, Name(s, component), component),
		Spec: corev1.ServiceSpec{
			Selector: Labels(s, component),
			Ports:    []corev1.ServicePort{{Name: component, Port: port, TargetPort: intstr.FromInt32(port)}},
		},
	}
}

// ConfigMap with the migrated application config of one release. Keys become environment
// variables, e.g. retention.days → RETENTION_DAYS.
func ConfigMap(s *v1.PlatformStack, version string, cfg map[string]string) *corev1.ConfigMap {
	data := map[string]string{"PLATFORM_VERSION": version}
	for k, v := range cfg {
		data[EnvKey(k)] = v
	}
	cm := &corev1.ConfigMap{ObjectMeta: meta(s, ConfigName(s, version), Core), Data: data}
	cm.Labels["platform.stacklifecycle.io/version"] = version
	return cm
}

// BackupPVC holds database dumps; sized like the database volume.
func BackupPVC(s *v1.PlatformStack) *corev1.PersistentVolumeClaim {
	c := claim(BackupClaim(s), s.Spec.Database)
	c.ObjectMeta = meta(s, BackupClaim(s), Tools)
	return &c
}

// Job runs one lifecycle action (backup, migrate, restore) to completion, once: a failed
// migration must not be retried blindly.
func Job(s *v1.PlatformStack, name, image string, args []string, withBackup bool) *batchv1.Job {
	zero := int32(0)
	ttl := int32(7 * 24 * 3600)
	tmpV, tmpM := scratch()
	vols := []corev1.Volume{tmpV}
	mounts := []corev1.VolumeMount{tmpM}
	if withBackup {
		vols = append(vols, corev1.Volume{Name: "backup", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: BackupClaim(s)}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "backup", MountPath: "/backup"})
	}
	return &batchv1.Job{
		ObjectMeta: meta(s, name, Tools),
		Spec: batchv1.JobSpec{
			BackoffLimit:            &zero,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: Labels(s, Tools)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext:              PodSecurity(),
					AutomountServiceAccountToken: ptr(false),
					Volumes:                      vols,
					Containers: []corev1.Container{{
						Name:            "job",
						Image:           image,
						Args:            args,
						SecurityContext: ContainerSecurity(),
						Resources:       resources("250m", "512Mi"),
						EnvFrom:         []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: Name(s, "credentials")}}}},
						Env:             []corev1.EnvVar{{Name: "DATABASE_HOST", Value: Name(s, Postgres)}},
						VolumeMounts:    mounts,
					}},
				},
			},
		},
	}
}

// Hash of a config map's content, stable across key order.
func Hash(cfg map[string]string) string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, cfg[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EnvKey turns retention.days into RETENTION_DAYS and tls.minVersion into TLS_MIN_VERSION.
func EnvKey(k string) string {
	var b strings.Builder
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= 'A' && c <= 'Z':
			if i > 0 && k[i-1] >= 'a' && k[i-1] <= 'z' {
				b.WriteByte('_')
			}
			b.WriteByte(c)
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func sanitize(v string) string {
	b := []byte(v)
	for i, c := range b {
		if c == '.' {
			b[i] = '-'
		}
	}
	return string(b)
}

func ptr[T any](v T) *T { return &v }
