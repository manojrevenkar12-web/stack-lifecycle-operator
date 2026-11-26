package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phases of a PlatformStack.
const (
	PhaseInstalling  = "Installing"  // first install in progress
	PhaseReady       = "Ready"       // installed release is healthy
	PhaseBlocked     = "Blocked"     // preflight failed; nothing was changed
	PhaseUpgrading   = "Upgrading"   // running the upgrade steps
	PhaseRollingBack = "RollingBack" // a step failed; putting the last verified release back
	PhaseFailed      = "Failed"      // needs a person: see the conditions and the last step
)

// Step states.
const (
	StepPending = "Pending"
	StepRunning = "Running"
	StepDone    = "Done"
	StepFailed  = "Failed"
	StepSkipped = "Skipped"
)

// StorageSpec sizes a persistent volume.
type StorageSpec struct {
	// Size of the volume, e.g. 50Gi.
	Size resource.Quantity `json:"size"`
	// StorageClassName, or the cluster default when empty.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// UpgradePolicy controls how careful an upgrade is. The defaults are the careful ones.
type UpgradePolicy struct {
	// SkipPreflight starts upgrades without checking config, health, backup space and queues.
	// +optional
	SkipPreflight bool `json:"skipPreflight,omitempty"`
	// SkipBackup skips the database backup before migrations that cannot be reversed.
	// +optional
	SkipBackup bool `json:"skipBackup,omitempty"`
	// DisableAutoRollback stops at a failed step instead of restoring the last verified release.
	// +optional
	DisableAutoRollback bool `json:"disableAutoRollback,omitempty"`
	// StepTimeoutSeconds fails a step that has not finished in time. Default 900.
	// +optional
	StepTimeoutSeconds int32 `json:"stepTimeoutSeconds,omitempty"`
}

// PlatformStackSpec is the desired platform.
type PlatformStackSpec struct {
	// Version is the release to run. Changing it starts an upgrade.
	Version string `json:"version"`
	// Config is the application configuration. Keys of older releases are migrated.
	// +optional
	Config map[string]string `json:"config,omitempty"`
	// Database is the PostgreSQL volume.
	Database StorageSpec `json:"database"`
	// Messaging is the RabbitMQ volume.
	Messaging StorageSpec `json:"messaging"`
	// CoreReplicas of the core service. Default 2.
	// +optional
	CoreReplicas int32 `json:"coreReplicas,omitempty"`
	// GatewayReplicas of the API gateway. Default 2.
	// +optional
	GatewayReplicas int32 `json:"gatewayReplicas,omitempty"`
	// Upgrade policy.
	// +optional
	Upgrade UpgradePolicy `json:"upgrade,omitempty"`
}

// StepStatus is one step of the current upgrade or rollback.
type StepStatus struct {
	Kind      string       `json:"kind"`
	Version   string       `json:"version"`
	Schema    int          `json:"schema,omitempty"`
	State     string       `json:"state"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	Message   string       `json:"message,omitempty"`
}

// CheckResult is one preflight check.
type CheckResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// UpgradeRecord is one finished upgrade, kept for the audit trail.
type UpgradeRecord struct {
	From     string      `json:"from"`
	To       string      `json:"to"`
	Result   string      `json:"result"`
	Message  string      `json:"message,omitempty"`
	Started  metav1.Time `json:"started"`
	Finished metav1.Time `json:"finished"`
}

// VersionConfig is the application config migrated for one release.
type VersionConfig struct {
	Version string            `json:"version"`
	Config  map[string]string `json:"config"`
}

// PlatformStackStatus is what the operator observed and did.
type PlatformStackStatus struct {
	Phase            string          `json:"phase,omitempty"`
	InstalledVersion string          `json:"installedVersion,omitempty"`
	SchemaVersion    int             `json:"schemaVersion,omitempty"`
	TargetVersion    string          `json:"targetVersion,omitempty"`
	UpgradeFrom      string          `json:"upgradeFrom,omitempty"`
	RollbackTarget   string          `json:"rollbackTarget,omitempty"`
	Steps            []StepStatus    `json:"steps,omitempty"`
	Current          int             `json:"current,omitempty"`
	Configs          []VersionConfig `json:"configs,omitempty"`
	Preflight        []CheckResult   `json:"preflight,omitempty"`
	History          []UpgradeRecord `json:"history,omitempty"`
	// +optional
	UpgradeStarted     *metav1.Time       `json:"upgradeStarted,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PlatformStack is one installation of the platform: database, broker, core service and gateway.
type PlatformStack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlatformStackSpec   `json:"spec,omitempty"`
	Status PlatformStackStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PlatformStackList is a list of PlatformStack.
type PlatformStackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlatformStack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlatformStack{}, &PlatformStackList{})
}

// ConfigFor returns the migrated config recorded for a release.
func (s *PlatformStackStatus) ConfigFor(version string) (map[string]string, bool) {
	for _, c := range s.Configs {
		if c.Version == version {
			return c.Config, true
		}
	}
	return nil, false
}
