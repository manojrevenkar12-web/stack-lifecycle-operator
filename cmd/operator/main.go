// Command operator runs the PlatformStack controller.
package main

import (
	"flag"
	corev1 "k8s.io/api/core/v1"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/manojrevenkar12-web/stack-lifecycle-operator/api/v1alpha1"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/controller"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/render"
)

func main() {
	var metricsAddr, probeAddr, registry string
	var leaderElect bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics endpoint")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health and readiness probes")
	flag.StringVar(&registry, "image-registry", envOr("STACK_IMAGE_REGISTRY", "registry.example.com/platform"), "registry of the platform images")
	flag.BoolVar(&leaderElect, "leader-elect", true, "one active replica at a time")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))

	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "stack-lifecycle-operator.platform.stacklifecycle.io",
		// Secrets are read straight from the API server, never cached: the operator may only get and
		// create them, so it cannot list every Secret in the namespace.
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}}},
	}
	// Least privilege: with WATCH_NAMESPACE set the operator only needs a namespaced Role.
	if ns := os.Getenv("WATCH_NAMESPACE"); ns != "" {
		mgrOpts.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}
		mgrOpts.LeaderElectionNamespace = ns
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		log.Error(err, "manager")
		os.Exit(1)
	}
	r := &controller.Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Catalog:  release.Default(),
		Images:   render.Images{Registry: registry},
		Recorder: mgr.GetEventRecorderFor("stack-lifecycle-operator"),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "controller")
		os.Exit(1)
	}
	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))
	log.Info("starting", "registry", registry)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager stopped")
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
