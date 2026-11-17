package dk

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client talks to one cluster / namespace.
type Client struct {
	Namespace string
	cs        kubernetes.Interface
	cfg       *rest.Config
	execFn    execFunc // nil = real exec through the API server; tests replace it
}

// NewClient builds a client from kubeconfig content.
func NewClient(kubeconfig []byte, namespace string) (*Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("加载 kubeconfig 失败: %w", err)
	}
	cfg.Timeout = 20 * time.Second
	cfg.QPS = 30
	cfg.Burst = 60
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Namespace: namespace, cs: cs, cfg: cfg}, nil
}

// NewClientFor wraps an existing clientset (tests).
func NewClientFor(cs kubernetes.Interface, namespace string) *Client {
	return &Client{Namespace: namespace, cs: cs}
}

// ServerVersion returns the Kubernetes version, proving connectivity.
func (c *Client) ServerVersion() (string, error) {
	v, err := c.cs.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// workload abstracts the three ways DataKit is deployed: DaemonSet (the usual one),
// StatefulSet and Deployment. Exactly one field is set.
type workload struct {
	ds  *appsv1.DaemonSet
	sts *appsv1.StatefulSet
	dep *appsv1.Deployment
}

func (w *workload) kind() string {
	switch {
	case w.ds != nil:
		return "DaemonSet"
	case w.sts != nil:
		return "StatefulSet"
	}
	return "Deployment"
}

func (w *workload) meta() *metav1.ObjectMeta {
	switch {
	case w.ds != nil:
		return &w.ds.ObjectMeta
	case w.sts != nil:
		return &w.sts.ObjectMeta
	}
	return &w.dep.ObjectMeta
}

func (w *workload) name() string { return w.meta().Name }

func (w *workload) template() *corev1.PodTemplateSpec {
	switch {
	case w.ds != nil:
		return &w.ds.Spec.Template
	case w.sts != nil:
		return &w.sts.Spec.Template
	}
	return &w.dep.Spec.Template
}

func (w *workload) selector() *metav1.LabelSelector {
	switch {
	case w.ds != nil:
		return w.ds.Spec.Selector
	case w.sts != nil:
		return w.sts.Spec.Selector
	}
	return w.dep.Spec.Selector
}

func (w *workload) deepCopy() *workload {
	switch {
	case w.ds != nil:
		return &workload{ds: w.ds.DeepCopy()}
	case w.sts != nil:
		return &workload{sts: w.sts.DeepCopy()}
	}
	return &workload{dep: w.dep.DeepCopy()}
}

// forBackup returns a copy ready to be written as YAML (kind filled in, noise removed).
func (w *workload) forBackup() any {
	c := w.deepCopy()
	c.meta().ManagedFields = nil
	switch {
	case c.ds != nil:
		c.ds.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}
		return c.ds
	case c.sts != nil:
		c.sts.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}
		return c.sts
	}
	c.dep.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	return c.dep
}

// claimTemplates are the StatefulSet volumeClaimTemplates: volumes that exist for the pod
// without being listed in the pod template's volumes.
func (w *workload) claimTemplates() map[string]bool {
	out := map[string]bool{}
	if w.sts != nil {
		for _, t := range w.sts.Spec.VolumeClaimTemplates {
			out[t.Name] = true
		}
	}
	return out
}

// onDelete reports whether a template change is NOT rolled out automatically: pods keep
// running the old spec until someone deletes them.
func (w *workload) onDelete() bool {
	switch {
	case w.ds != nil:
		return w.ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType
	case w.sts != nil:
		return w.sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType
	}
	return false
}

func (w *workload) counts() (desired, updated, ready, available int32, observed bool) {
	switch {
	case w.ds != nil:
		s := w.ds.Status
		return s.DesiredNumberScheduled, s.UpdatedNumberScheduled, s.NumberReady, s.NumberAvailable, s.ObservedGeneration >= w.ds.Generation
	case w.sts != nil:
		s := w.sts.Status
		d := int32(1)
		if w.sts.Spec.Replicas != nil {
			d = *w.sts.Spec.Replicas
		}
		// availableReplicas only exists on newer clusters; ready is the closest older signal
		avail := s.AvailableReplicas
		if avail == 0 && s.ReadyReplicas > 0 {
			avail = s.ReadyReplicas
		}
		// a partitioned / in-flight rollout still has pods on the old revision
		upd := s.UpdatedReplicas
		if s.UpdateRevision == "" || s.UpdateRevision == s.CurrentRevision {
			upd = s.Replicas
		}
		return d, upd, s.ReadyReplicas, avail, s.ObservedGeneration >= w.sts.Generation
	}
	s := w.dep.Status
	d := int32(1)
	if w.dep.Spec.Replicas != nil {
		d = *w.dep.Spec.Replicas
	}
	return d, s.UpdatedReplicas, s.ReadyReplicas, s.AvailableReplicas, s.ObservedGeneration >= w.dep.Generation
}

func isDatakitImage(img string) bool {
	name := imageName(img)
	return name == "datakit" || name == "datakit-elinker" || name == "truewatch"
}

func datakitContainer(t *corev1.PodTemplateSpec) int {
	for i, c := range t.Spec.Containers {
		if isDatakitImage(c.Image) {
			return i
		}
	}
	for i, c := range t.Spec.Containers {
		if c.Name == "datakit" {
			return i
		}
	}
	return -1
}

// findWorkload locates DataKit in the namespace. Order: DaemonSet, StatefulSet, Deployment;
// within a kind, the one named "datakit" wins over any other workload running the datakit image.
func (c *Client) findWorkload(ctx context.Context) (*workload, error) {
	dss, err := c.cs.AppsV1().DaemonSets(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var ds *appsv1.DaemonSet
	for i := range dss.Items {
		d := &dss.Items[i]
		if datakitContainer(&d.Spec.Template) >= 0 && (ds == nil || d.Name == "datakit") {
			ds = d
		}
	}
	if ds != nil {
		return &workload{ds: ds}, nil
	}

	stss, err := c.cs.AppsV1().StatefulSets(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var sts *appsv1.StatefulSet
	for i := range stss.Items {
		d := &stss.Items[i]
		if datakitContainer(&d.Spec.Template) >= 0 && (sts == nil || d.Name == "datakit") {
			sts = d
		}
	}
	if sts != nil {
		return &workload{sts: sts}, nil
	}

	deps, err := c.cs.AppsV1().Deployments(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var dep *appsv1.Deployment
	for i := range deps.Items {
		d := &deps.Items[i]
		if datakitContainer(&d.Spec.Template) >= 0 && (dep == nil || d.Name == "datakit") {
			dep = d
		}
	}
	if dep != nil {
		return &workload{dep: dep}, nil
	}
	return nil, nil
}

func (c *Client) updateWorkload(ctx context.Context, w *workload) error {
	var err error
	switch {
	case w.ds != nil:
		_, err = c.cs.AppsV1().DaemonSets(c.Namespace).Update(ctx, w.ds, metav1.UpdateOptions{})
	case w.sts != nil:
		_, err = c.cs.AppsV1().StatefulSets(c.Namespace).Update(ctx, w.sts, metav1.UpdateOptions{})
	default:
		_, err = c.cs.AppsV1().Deployments(c.Namespace).Update(ctx, w.dep, metav1.UpdateOptions{})
	}
	return err
}

func (c *Client) getConfigMap(ctx context.Context, name string) (*corev1.ConfigMap, error) {
	cm, err := c.cs.CoreV1().ConfigMaps(c.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return cm, err
}

func (c *Client) listPods(ctx context.Context, w *workload) ([]corev1.Pod, error) {
	sel, err := metav1.LabelSelectorAsSelector(w.selector())
	if err != nil {
		return nil, err
	}
	pl, err := c.cs.CoreV1().Pods(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	return pl.Items, nil
}
