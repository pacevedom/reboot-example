package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

var (
	leaseCancel context.CancelFunc = nil
)

func main() {
	klog.InitFlags(nil)

	holderIdentity := os.Getenv("NAME")
	namespace := os.Getenv("NAMESPACE")
	node := os.Getenv("NODE")

	klog.InfoS("Starting", "holder", holderIdentity, "namespace", namespace, "node", node)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatal(err)
	}
	client := clientset.NewForConfigOrDie(cfg)

	run := func(ctx context.Context) {
		klog.Info("Simulating work")
		select {
		case <-ctx.Done():
			break
		}
		klog.Info("Work finished")
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		klog.Info("SIGTERM, shutting down")
		if leaseCancel != nil {
			leaseCancel()
		}
	}()

	// The lease object we are using is in our own namespace and uses the
	// node's name as the name for itself. Kubelet watches on leases with
	// the same name, except for those in kube-node-lease namespace which
	// are reserved for efficient heartbeating.
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      node,
			Namespace: namespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: holderIdentity,
			//TODO event recorder
		},
	}

	// Configure a simple HTTP server where we can start/stop critical operations
	// at will.
	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		klog.Info("Starting critical operation")
		// leader election Run does not return until the lease is not held anymore
		// or the context has been cancelled.
		go func() {
			if leaseCancel != nil {
				return
			}
			leaderCtx, cancel := context.WithCancel(context.Background())
			leaseCancel = cancel

			// Acquire the lease and start the critical operation when node is ready.
			elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
				Lock: lock,
				ReleaseOnCancel: true,
				LeaseDuration: 30 * time.Second,
				RenewDeadline: 15 * time.Second,
				RetryPeriod: 5 * time.Second,
				Callbacks: leaderelection.LeaderCallbacks{
					OnStartedLeading: func(ctx context.Context) {
						defer func() {
							if leaseCancel != nil {
								leaseCancel()
								leaseCancel = nil
							}
						}()
						err := wait.PollImmediate(time.Second, 10 * time.Minute, func() (bool, error) {
							node, err := client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
							if err != nil {
								klog.ErrorS(err, "failed getting the node", "node", node)
								return false, nil
							}
							for _, condition := range node.Status.Conditions {
								//TODO pending addition to k8s.io/api/core/v1
								if condition.Type == "ShutdownInhibited" && condition.Status == corev1.ConditionTrue {
									return true, nil
								}
							}
							return false, nil
						})
						if err != nil {
							klog.ErrorS(err, "failed waiting node conditions to be ready", "node", node)
							return
						}
						run(ctx)
						klog.Info("Done working")
					},
					OnStoppedLeading: func() {
						klog.InfoS("leadership lost", "identity", holderIdentity)
					},
					OnNewLeader: func(identity string) {
						if identity == holderIdentity {
							return
						}
						klog.InfoS("new leader elected", "leader", identity)
					},
				},
			})
			if err != nil {
				klog.ErrorS(err, "unable to create leader elector")
				return
			}
			elector.Run(leaderCtx)
		}()
    })

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
        klog.Info("Stopping critical operation")
		if leaseCancel != nil {
			leaseCancel()
			leaseCancel = nil
		}
    })
	klog.Fatal(http.ListenAndServe(":8080", nil))
}
