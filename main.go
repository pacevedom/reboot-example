package main

import (
	"context"
	"flag"
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
	leaseDuration = flag.Uint("leaseduration", 60, "Lease duration in seconds")
	initialDelay = flag.Uint("initialdelay", 60, "Initial delay to grab the lease")
)

func main() {
	klog.InitFlags(nil)

	flag.Parse()

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
		klog.InfoS("Simulating work", "seconds", *leaseDuration)
		time.Sleep(time.Second * time.Duration(*leaseDuration))
		klog.Info("Work done")
	}

	leaderCtx, leaderCancel := context.WithCancel(context.Background())

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		klog.Info("SIGTERM, shutting down")
		leaderCancel()
	}()

	klog.InfoS("Waiting to start critical operation", "seconds", *initialDelay)
	time.Sleep(time.Duration(*initialDelay) * time.Second)
	klog.Info("Starting critical operation")

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
		},
	}

	// This simulates the acquire lease + critical operation + release workflow.
	// Should be done like this in any operator. Time related variables are just
	// placeholders.
	leaderelection.RunOrDie(leaderCtx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second,
		RenewDeadline:   15 * time.Second,
		RetryPeriod:     5 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				defer leaderCancel()
				err := wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
					node, err := client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
					if err != nil {
						klog.ErrorS(err, "failed getting the node", "node", node)
						return false, nil
					}
					for _, condition := range node.Status.Conditions {
						//TODO pending addition to k8s.io/api/core/v1
						if condition.Type == "RebootInhibited" && condition.Status == corev1.ConditionTrue {
							return true, nil
						}
					}
					return false, nil
				})
				if err != nil {
					klog.Fatal(err)
				}
				run(ctx)
				klog.Info("Done working")
			},
			OnStoppedLeading: func() {
				// executed after cancelling the context.
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

	klog.Info("Critical operation is over. Resuming normal tasks...")
	select {}
}
