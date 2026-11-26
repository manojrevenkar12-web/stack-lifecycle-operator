// Deep copies for the API types. Written to match what controller-gen generates.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *StorageSpec) DeepCopyInto(out *StorageSpec) {
	*out = *in
	out.Size = in.Size.DeepCopy()
	if in.StorageClassName != nil {
		s := *in.StorageClassName
		out.StorageClassName = &s
	}
}

// DeepCopyInto copies the receiver into out.
func (in *PlatformStackSpec) DeepCopyInto(out *PlatformStackSpec) {
	*out = *in
	out.Config = copyMap(in.Config)
	in.Database.DeepCopyInto(&out.Database)
	in.Messaging.DeepCopyInto(&out.Messaging)
}

// DeepCopyInto copies the receiver into out.
func (in *StepStatus) DeepCopyInto(out *StepStatus) {
	*out = *in
	if in.StartedAt != nil {
		out.StartedAt = in.StartedAt.DeepCopy()
	}
}

// DeepCopyInto copies the receiver into out.
func (in *UpgradeRecord) DeepCopyInto(out *UpgradeRecord) {
	*out = *in
	in.Started.DeepCopyInto(&out.Started)
	in.Finished.DeepCopyInto(&out.Finished)
}

// DeepCopyInto copies the receiver into out.
func (in *PlatformStackStatus) DeepCopyInto(out *PlatformStackStatus) {
	*out = *in
	if in.Steps != nil {
		out.Steps = make([]StepStatus, len(in.Steps))
		for i := range in.Steps {
			in.Steps[i].DeepCopyInto(&out.Steps[i])
		}
	}
	if in.Configs != nil {
		out.Configs = make([]VersionConfig, len(in.Configs))
		for i := range in.Configs {
			out.Configs[i] = VersionConfig{Version: in.Configs[i].Version, Config: copyMap(in.Configs[i].Config)}
		}
	}
	if in.Preflight != nil {
		out.Preflight = append([]CheckResult(nil), in.Preflight...)
	}
	if in.History != nil {
		out.History = make([]UpgradeRecord, len(in.History))
		for i := range in.History {
			in.History[i].DeepCopyInto(&out.History[i])
		}
	}
	if in.UpgradeStarted != nil {
		out.UpgradeStarted = in.UpgradeStarted.DeepCopy()
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

// DeepCopyInto copies the receiver into out.
func (in *PlatformStack) DeepCopyInto(out *PlatformStack) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy returns a copy of the receiver.
func (in *PlatformStack) DeepCopy() *PlatformStack {
	if in == nil {
		return nil
	}
	out := new(PlatformStack)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *PlatformStack) DeepCopyObject() runtime.Object { return in.DeepCopy() }

// DeepCopyInto copies the receiver into out.
func (in *PlatformStackList) DeepCopyInto(out *PlatformStackList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PlatformStack, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy returns a copy of the receiver.
func (in *PlatformStackList) DeepCopy() *PlatformStackList {
	if in == nil {
		return nil
	}
	out := new(PlatformStackList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *PlatformStackList) DeepCopyObject() runtime.Object { return in.DeepCopy() }

// DeepCopy returns a copy of the receiver.
func (in *PlatformStackStatus) DeepCopy() *PlatformStackStatus {
	if in == nil {
		return nil
	}
	out := new(PlatformStackStatus)
	in.DeepCopyInto(out)
	return out
}
