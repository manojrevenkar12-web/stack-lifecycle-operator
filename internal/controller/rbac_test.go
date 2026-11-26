package controller

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The shipped Role must grant exactly what the +kubebuilder:rbac markers ask for: no more (least
// privilege), no less (the operator would fail at a customer site).
func TestRoleMatchesMarkers(t *testing.T) {
	src, err := os.ReadFile("platformstack_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	re := regexp.MustCompile(`(?m)^// \+kubebuilder:rbac:groups=([^,]*),resources=([^,]+),verbs=(\S+)$`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		group := strings.Trim(m[1], `"`)
		verbs := strings.Split(m[3], ";")
		sort.Strings(verbs)
		for _, res := range strings.Split(m[2], ";") {
			want[group+"/"+res] = strings.Join(verbs, ",")
		}
	}
	raw, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	if err := yaml.UnmarshalStrict(raw, &role); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range role.Rules {
		verbs := slices.Clone(r.Verbs)
		sort.Strings(verbs)
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				got[g+"/"+res] = strings.Join(verbs, ",")
			}
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: role grants %q, markers ask for %q", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: granted by the role but not asked for by any marker", k)
		}
	}
	if strings.Contains(got["/secrets"], "list") || strings.Contains(got["/secrets"], "watch") {
		t.Error("the operator must not be able to list Secrets")
	}
}
