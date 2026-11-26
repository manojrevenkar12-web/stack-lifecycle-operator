// Command render prints the objects the operator would create for a PlatformStack, as YAML, so
// they can be linted (tools/stackctl openshift-lint) and reviewed without a cluster.
//
//	go run ./cmd/render -f config/samples/platformstack.yaml > rendered.yaml
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/controller"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/plan"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/render"
)

func main() {
	file := flag.String("f", "config/samples/platformstack.yaml", "PlatformStack manifest")
	version := flag.String("version", "", "render this release instead of spec.version")
	registry := flag.String("registry", "registry.example.com/platform", "image registry")
	flag.Parse()
	if err := run(*file, *version, *registry); err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
}

func run(file, version, registry string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var s v1.PlatformStack
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return err
	}
	cat := release.Default()
	if version != "" && version != s.Spec.Version {
		// carry the config along the upgrade path, as the operator does during an upgrade
		path, err := cat.Path(s.Spec.Version, version)
		if err != nil {
			return err
		}
		s.Spec.Config, _ = plan.MigrateConfig(cat, s.Spec.Config, path)
		s.Spec.Version = version
	}
	if s.Namespace == "" {
		s.Namespace = "default"
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	r := &controller.Reconciler{Scheme: scheme, Catalog: cat, Images: render.Images{Registry: registry}}
	objs, err := r.Manifests(&s)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	for _, o := range objs {
		gvk, err := apiutil.GVKForObject(o, scheme)
		if err != nil {
			return err
		}
		o.GetObjectKind().SetGroupVersionKind(gvk)
		b, err := yaml.Marshal(o)
		if err != nil {
			return err
		}
		out.WriteString("---\n")
		out.Write(b)
	}
	_, err = os.Stdout.Write(out.Bytes())
	return err
}
