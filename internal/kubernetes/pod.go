package kubernetes

import (
	"context"
	"fmt"
	"github.com/liqoTech/liqo/internal/errdefs"
	"github.com/liqoTech/liqo/internal/node/api"
	"github.com/liqoTech/liqo/internal/trace"
	"github.com/pkg/errors"
	"io"
	v1 "k8s.io/api/core/v1"
	kerror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog"
	stats "k8s.io/kubernetes/pkg/kubelet/apis/stats/v1alpha1"
	"math/rand"
	"time"
)

// CreatePod accepts a Pod definition and stores it in memory.
func (p *KubernetesProvider) CreatePod(ctx context.Context, pod *v1.Pod) error {
	// Add the pod's coordinates to the current span.
	if pod == nil {
		return errors.New("pod cannot be nil")
	}

	klog.Infof("receive CreatePod %q", pod.Name)

	if pod.OwnerReferences != nil && len(pod.OwnerReferences) != 0 && pod.OwnerReferences[0].Kind == "DaemonSet" {
		msg := fmt.Sprintf("Skip to create DaemonSet pod %q", pod.Name)
		klog.Info(msg)
		return nil
	}

	nattedNS, err := p.NatNamespace(pod.Namespace, true)
	if err != nil {
		return err
	}

	podTranslated := H2FTranslate(pod, nattedNS)

	podServer, err := p.foreignClient.Client().CoreV1().Pods(podTranslated.Namespace).Create(podTranslated)
	if err != nil {
		return err
	}
	klog.Info("Pod", podServer.Name, "successfully created on remote cluster")
	// Here we have to change the view of the remote POD to show it as a local one
	p.notifier(pod)

	return nil
}

// UpdatePod accepts a Pod definition and updates its reference.
func (p *KubernetesProvider) UpdatePod(ctx context.Context, pod *v1.Pod) error {
	if pod == nil {
		return errors.New("pod cannot be nil")
	}
	// Add the pod's coordinates to the current span.
	nattedNS, err := p.NatNamespace(pod.Namespace, false)
	if err != nil {
		return err
	}

	podTranslated := H2FTranslate(pod, nattedNS)

	poUpdated, err := p.foreignClient.Client().CoreV1().Pods(nattedNS).Get(podTranslated.Name, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Cannot update pod \"%s\" in provider namespace \"%s\"", podTranslated.Name, podTranslated.Namespace)
		return err
	}
	klog.V(3).Infof("Updated pod \"%s\" in provider namespace \"%s\"", podTranslated.Name, podTranslated.Namespace)

	podInverse := F2HTranslate(poUpdated, p.RemappedPodCidr, pod.Namespace)
	p.notifier(podInverse)

	return nil
}

// DeletePod deletes the specified pod out of memory.
func (p *KubernetesProvider) DeletePod(ctx context.Context, pod *v1.Pod) (err error) {
	klog.Infof("receive DeletePod %q", pod.Name)
	opts := &metav1.DeleteOptions{}

	nattedNS, err := p.NatNamespace(pod.Namespace, false)
	if err != nil {
		return err
	}

	err = p.foreignClient.Client().CoreV1().Pods(nattedNS).Delete(pod.Name, opts)
	if err != nil {
		return errors.Wrap(err, "Unable to delete pod")
	}

	now := metav1.Now()
	pod.Status.Phase = v1.PodSucceeded
	pod.Status.Reason = "KubernetesProviderPodDeleted"
	//
	for idx := range pod.Status.ContainerStatuses {
		pod.Status.ContainerStatuses[idx].Ready = false
		// We fix to now the starting container when reconciliating a container which is
		if pod.Status.ContainerStatuses[idx].State.Running == nil {
			pod.Status.ContainerStatuses[idx].State.Running = &v1.ContainerStateRunning{StartedAt: now}
		}
		pod.Status.ContainerStatuses[idx].State = v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				Message:    "Kubernetes provider terminated container upon deletion",
				FinishedAt: now,
				Reason:     "KubernetesProviderPodContainerDeleted",
				StartedAt:  pod.Status.ContainerStatuses[idx].State.Running.StartedAt,
			},
		}
	}

	p.notifier(pod)

	return nil
}

// GetPod returns a pod by name that is stored in memory.
func (p *KubernetesProvider) GetPod(ctx context.Context, namespace, name string) (pod *v1.Pod, err error) {
	klog.Infof("receive GetPod %q", name)
	opts := metav1.GetOptions{}

	nattedNS, err := p.NatNamespace(namespace, false)
	if err != nil {
		return nil, err
	}

	podServer, err := p.foreignClient.Client().CoreV1().Pods(nattedNS).Get(name, opts)
	if err != nil {
		if kerror.IsNotFound(err) {
			return nil, errdefs.NotFoundf("pod \"%s/%s\" is not known to the provider", namespace, name)
		}
		return nil, errors.Wrap(err, "Unable to get pod")
	}

	podInverted := F2HTranslate(podServer, p.RemappedPodCidr, namespace)
	return podInverted, nil
}

// GetPodStatus returns the status of a pod by name that is "running".
// returns nil if a pod by that name is not found.
func (p *KubernetesProvider) GetPodStatus(ctx context.Context, namespace, name string) (*v1.PodStatus, error) {
	nattedNS, err := p.NatNamespace(namespace, false)

	if err != nil {
		return nil, nil
	}

	podForeignIn, err := p.foreignClient.Client().CoreV1().Pods(nattedNS).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "error getting status")
	}
	podOutput := F2HTranslate(podForeignIn, p.RemappedPodCidr, namespace)
	klog.Infof("receive GetPodStatus %q", name)
	return &podOutput.Status, nil
}

// RunInContainer executes a command in a container in the pod, copying data
// between in/out/err and the container's stdin/stdout/stderr.
func (p *KubernetesProvider) RunInContainer(ctx context.Context, namespace string, podName string, containerName string, cmd []string, attach api.AttachIO) error {

	nattedNS, err := p.NatNamespace(namespace, false)
	if err != nil {
		return err
	}

	req := p.foreignClient.Client().CoreV1().RESTClient().
		Post().
		Namespace(nattedNS).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(p.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("could not make remote command: %v", err)
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  attach.Stdin(),
		Stdout: attach.Stdout(),
		Stderr: attach.Stderr(),
		Tty:    attach.TTY(),
	})
	if err != nil {
		return fmt.Errorf("streaming error: %v", err)
	}

	return nil
}

// GetContainerLogs retrieves the logs of a container by name from the provider.
func (p *KubernetesProvider) GetContainerLogs(ctx context.Context, namespace string, podName string, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	nattedNS, err := p.NatNamespace(namespace, false)
	if err != nil {
		return nil, err
	}

	options := &v1.PodLogOptions{
		Container: containerName,
	}
	logs := p.foreignClient.Client().CoreV1().Pods(nattedNS).GetLogs(podName, options)
	stream, err := logs.Stream()
	if err != nil {
		return nil, fmt.Errorf("could not get stream from logs request: %v", err)
	}
	return stream, nil
}

// GetPods returns a list of all pods known to be "running".
func (p *KubernetesProvider) GetPods(ctx context.Context) ([]*v1.Pod, error) {
	klog.Info("receive GetPods")

	if p.foreignClient == nil {
		return nil, nil
	}

	var podsHomeOut []*v1.Pod

	nt, err := p.ntCache.getNattingTable(p.foreignClusterId)
	if err != nil {
		return nil, err
	}

	if nt == nil || nt.Spec.NattingTable == nil {
		klog.Info("empty natting table")
		return podsHomeOut, nil
	}

	for k, v := range nt.Spec.NattingTable {
		podsForeignIn, err := p.foreignClient.Client().CoreV1().Pods(v).List(metav1.ListOptions{})

		if err != nil {
			if kerror.IsNotFound(err) {
				return nil, errdefs.NotFoundf("pods in \"%s\" is not known to the provider", k)
			}
			return nil, errors.Wrap(err, "Unable to get pods")
		}

		for i := range podsForeignIn.Items {
			pod := podsForeignIn.Items[i]
			podsHomeOut = append(podsHomeOut, H2FTranslate(&pod, k))
		}
	}

	return podsHomeOut, nil
}

// GetStatsSummary returns dummy stats for all pods known by this provider.
func (p *KubernetesProvider) GetStatsSummary(ctx context.Context) (*stats.Summary, error) {
	var span trace.Span
	_, span = trace.StartSpan(ctx, "GetStatsSummary") //nolint: ineffassign
	defer span.End()

	// Grab the current timestamp so we can report it as the time the stats were generated.
	t := metav1.NewTime(time.Now())

	// Create the Summary object that will later be populated with node and pod stats.
	res := &stats.Summary{}

	// Populate the Summary object with basic node stats.
	res.Node = stats.NodeStats{
		NodeName:  p.nodeName,
		StartTime: metav1.NewTime(p.startTime),
	}

	// Populate the Summary object with dummy stats for each pod known by this provider.
	pods, err := p.foreignClient.Client().CoreV1().Pods("").List(metav1.ListOptions{})
	if err != nil {
		if kerror.IsNotFound(err) {
			return nil, errdefs.NotFoundf("pods in \"%s\" is not known to the provider", "")
		}
		return nil, errors.Wrap(err, "Unable to get pods")
	}
	for _, pod := range pods.Items {
		var (
			// totalUsageNanoCores will be populated with the sum of the values of UsageNanoCores computes across all containers in the pod.
			totalUsageNanoCores uint64
			// totalUsageBytes will be populated with the sum of the values of UsageBytes computed across all containers in the pod.
			totalUsageBytes uint64
		)

		// Create a PodStats object to populate with pod stats.
		pss := stats.PodStats{
			PodRef: stats.PodReference{
				Name:      pod.Name,
				Namespace: pod.Namespace,
				UID:       string(pod.UID),
			},
			StartTime: pod.CreationTimestamp,
		}

		// Iterate over all containers in the current pod to compute dummy stats.
		for _, container := range pod.Spec.Containers {
			// Grab a dummy value to be used as the total CPU usage.
			// The value should fit a uint32 in order to avoid overflows later on when computing pod stats.
			dummyUsageNanoCores := uint64(rand.Uint32())
			totalUsageNanoCores += dummyUsageNanoCores
			// Create a dummy value to be used as the total RAM usage.
			// The value should fit a uint32 in order to avoid overflows later on when computing pod stats.
			dummyUsageBytes := uint64(rand.Uint32())
			totalUsageBytes += dummyUsageBytes
			// Append a ContainerStats object containing the dummy stats to the PodStats object.
			pss.Containers = append(pss.Containers, stats.ContainerStats{
				Name:      container.Name,
				StartTime: pod.CreationTimestamp,
				CPU: &stats.CPUStats{
					Time:           t,
					UsageNanoCores: &dummyUsageNanoCores,
				},
				Memory: &stats.MemoryStats{
					Time:       t,
					UsageBytes: &dummyUsageBytes,
				},
			})
		}

		// Populate the CPU and RAM stats for the pod and append the PodsStats object to the Summary object to be returned.
		pss.CPU = &stats.CPUStats{
			Time:           t,
			UsageNanoCores: &totalUsageNanoCores,
		}
		pss.Memory = &stats.MemoryStats{
			Time:       t,
			UsageBytes: &totalUsageBytes,
		}
		res.Pods = append(res.Pods, pss)
	}

	// Return the dummy stats.
	return res, nil
}

// NotifyPods is called to set a pod notifier callback function. This should be called before any operations are ready
// within the provider.
func (p *KubernetesProvider) NotifyPods(ctx context.Context, notifier func(*v1.Pod)) {
	p.notifier = notifier
}

func (p *KubernetesProvider) watchForeignPods(watcher watch.Interface, stop chan struct{}) {
	for {
		select {
		case <-stop:
			watcher.Stop()
			p.powg.Done()
			return
		case e := <-watcher.ResultChan():
			p2, ok := e.Object.(*v1.Pod)
			if !ok {
				klog.Error("unexpected type")
				break
			}
			denattedNS, err := p.DeNatNamespace(p2.Namespace)
			if err != nil {
				klog.Error(err, "natting error in watchForeignPods")
			}
			p.notifier(F2HTranslate(p2, p.RemappedPodCidr, denattedNS))
		}
	}
}
