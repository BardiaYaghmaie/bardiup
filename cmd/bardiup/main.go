// Command bardiup runs the Bardiup controller manager, or the data mover agent inside backup/restore Jobs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // timeZone support without relying on the image's zoneinfo

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/agent"
	"github.com/BardiaYaghmaie/bardiup/internal/controller"
	"github.com/BardiaYaghmaie/bardiup/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "agent":
			os.Exit(runAgent(os.Args[2:]))
		case "version":
			fmt.Println(version.Version)
			return
		}
	}
	if err := runManager(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAgent(args []string) int {
	if len(args) != 1 || (args[0] != "backup" && args[0] != "restore") {
		fmt.Fprintln(os.Stderr, "usage: bardiup agent backup|restore")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := agent.LoadConfig()
	var res *agent.Result
	if err == nil {
		started := time.Now()
		if args[0] == "backup" {
			res, err = agent.Backup(ctx, cfg, agent.DataDir)
		} else {
			res, err = agent.Restore(ctx, cfg, agent.DataDir)
		}
		if err == nil {
			fmt.Fprintf(os.Stderr, "%s of %s finished in %s\n", args[0], cfg.ObjectKey, time.Since(started).Round(time.Second))
		}
	}
	if err != nil {
		res = &agent.Result{Error: err.Error()}
	}
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	if werr := agent.WriteTerminationMessage("/dev/termination-log", res); werr != nil {
		fmt.Fprintln(os.Stderr, "write termination message:", werr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func runManager() error {
	var (
		metricsAddr     string
		probeAddr       string
		leaderElect     bool
		agentImage      string
		agentPullPolicy string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address of the Prometheus metrics endpoint. 0 disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address of the health probe endpoint.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for high availability.")
	flag.StringVar(&agentImage, "agent-image", os.Getenv("BARDIUP_AGENT_IMAGE"), "Image used for backup and restore Jobs (usually this controller's own image).")
	flag.StringVar(&agentPullPolicy, "agent-image-pull-policy", string(corev1.PullIfNotPresent), "Pull policy of the agent image.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if agentImage == "" {
		return fmt.Errorf("--agent-image (or BARDIUP_AGENT_IMAGE) is required")
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "bardiup.bardiup.io",
		// The process exits right after the manager stops, so handing the lease over early is safe.
		LeaderElectionReleaseOnCancel: true,
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			// Only cache the Jobs Bardiup creates.
			&batchv1.Job{}: {Label: labels.SelectorFromSet(labels.Set{v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue})},
		}},
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	jobs := controller.JobSettings{Image: agentImage, ImagePullPolicy: corev1.PullPolicy(agentPullPolicy)}
	recorder := mgr.GetEventRecorder("bardiup")
	reader := mgr.GetAPIReader()

	if err := (&controller.BackupConfigReconciler{
		Client: mgr.GetClient(), APIReader: reader, Recorder: recorder, Stores: controller.DefaultStoreFactory, Now: time.Now,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.BackupReconciler{
		Client: mgr.GetClient(), APIReader: reader, Recorder: recorder, Jobs: jobs, Stores: controller.DefaultStoreFactory, Now: time.Now,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.RestoreReconciler{
		Client: mgr.GetClient(), APIReader: reader, Recorder: recorder, Jobs: jobs, Now: time.Now,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	ctrl.Log.WithName("setup").Info("starting bardiup", "version", version.Version, "agentImage", agentImage)
	return mgr.Start(ctrl.SetupSignalHandler())
}
