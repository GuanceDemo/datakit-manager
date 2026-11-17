package dk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OperatorStatus describes the DataKit Operator deployment and its JSON config.
type OperatorStatus struct {
	Installed   bool   `json:"installed"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	Version     string `json:"version"`
	Desired     int32  `json:"desired"`
	Ready       int32  `json:"ready"`
	Pods        []Pod  `json:"pods"`
	ConfigMap   string `json:"configMap"`
	ConfigKey   string `json:"configKey"`
	Config      string `json:"config"` // raw JSON text
	ConfigError string `json:"configError"`
	Webhook     bool   `json:"webhook"`
	Message     string `json:"message"`
}

const operatorName = "datakit-operator"

func (c *Client) findOperator(ctx context.Context) (*appsv1.Deployment, error) {
	seen := map[string]bool{}
	for _, ns := range []string{OperatorNamespace, c.Namespace} {
		if seen[ns] {
			continue
		}
		seen[ns] = true
		d, err := c.cs.AppsV1().Deployments(ns).Get(ctx, operatorName, metav1.GetOptions{})
		if err == nil {
			return d, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, nil
}

// operatorConfigRef finds which ConfigMap key feeds ENV_JSON_CONFIG.
func operatorConfigRef(d *appsv1.Deployment) (name, key string) {
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "ENV_JSON_CONFIG" && e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
				return e.ValueFrom.ConfigMapKeyRef.Name, e.ValueFrom.ConfigMapKeyRef.Key
			}
		}
	}
	return "datakit-operator-config", "jsonconfig"
}

// Operator reads the DataKit Operator state.
func (c *Client) Operator(ctx context.Context) (*OperatorStatus, error) {
	st := &OperatorStatus{Namespace: OperatorNamespace, Name: operatorName, Pods: []Pod{}}
	d, err := c.findOperator(ctx)
	if err != nil {
		return nil, err
	}
	if d == nil {
		st.Message = "集群中没有安装 DataKit Operator。"
		return st, nil
	}
	st.Installed, st.Namespace = true, d.Namespace
	if len(d.Spec.Template.Spec.Containers) > 0 {
		st.Image = d.Spec.Template.Spec.Containers[0].Image
		st.Version = imageTag(st.Image)
	}
	st.Desired = 1
	if d.Spec.Replicas != nil {
		st.Desired = *d.Spec.Replicas
	}
	st.Ready = d.Status.ReadyReplicas

	st.ConfigMap, st.ConfigKey = operatorConfigRef(d)
	cm, err := c.cs.CoreV1().ConfigMaps(d.Namespace).Get(ctx, st.ConfigMap, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		st.ConfigError = fmt.Sprintf("ConfigMap %s 不存在", st.ConfigMap)
	case err != nil:
		return nil, err
	default:
		st.Config = cm.Data[st.ConfigKey]
		if !json.Valid([]byte(st.Config)) {
			st.ConfigError = "当前配置不是合法的 JSON"
		}
	}

	if sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector); err == nil {
		if pl, err := c.cs.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()}); err == nil {
			name := ""
			if len(d.Spec.Template.Spec.Containers) > 0 {
				name = d.Spec.Template.Spec.Containers[0].Name
			}
			st.Pods = toPods(pl.Items, name)
		}
	}
	if _, err := c.cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "datakit-operator-mutation", metav1.GetOptions{}); err == nil {
		st.Webhook = true
	}
	return st, nil
}

// ApplyOperatorConfig writes the JSON config and restarts the operator (it only reads the
// config at start-up).
func (c *Client) ApplyOperatorConfig(ctx context.Context, config, backupDir string) (*ApplyResult, error) {
	var probe map[string]any
	if err := json.Unmarshal([]byte(config), &probe); err != nil {
		return nil, fmt.Errorf("配置不是合法的 JSON: %w", err)
	}
	d, err := c.findOperator(ctx)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("集群中没有安装 DataKit Operator")
	}
	name, key := operatorConfigRef(d)
	res := &ApplyResult{Actions: []string{}, Warnings: []string{}}

	cms := c.cs.CoreV1().ConfigMaps(d.Namespace)
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("读取 ConfigMap %s 失败: %w", name, err)
	}
	if backupDir != "" {
		f := filepath.Join(backupDir, time.Now().Format("20060102-150405")+"-operator-config.json")
		if err := os.WriteFile(f, []byte(cm.Data[key]), 0o600); err == nil {
			res.BackupFile = f
		}
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = config
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("更新 ConfigMap %s 失败: %w", name, err)
	}
	res.Actions = append(res.Actions, fmt.Sprintf("ConfigMap %s 更新 %s", name, key))

	if d.Spec.Template.Annotations == nil {
		d.Spec.Template.Annotations = map[string]string{}
	}
	d.Spec.Template.Annotations[RestartAnnotation] = time.Now().Format(time.RFC3339)
	if _, err := c.cs.AppsV1().Deployments(d.Namespace).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("重启 Deployment %s 失败: %w", d.Name, err)
	}
	res.Actions = append(res.Actions, "重启 Deployment/"+d.Name)
	return res, nil
}

// OperatorRollout reports the operator restart progress.
func (c *Client) OperatorRollout(ctx context.Context) (*RolloutStatus, error) {
	d, err := c.findOperator(ctx)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("集群中没有安装 DataKit Operator")
	}
	w := &workload{dep: d}
	desired, updated, ready, available, observed := w.counts()
	rs := &RolloutStatus{Desired: desired, Updated: updated, Ready: ready, Available: available, Pods: []Pod{}}
	rs.Done = observed && updated == desired && available == desired && d.Status.Replicas == desired
	if rs.Done {
		rs.Message = fmt.Sprintf("Operator 已就绪（%d/%d）", available, desired)
	} else {
		rs.Message = fmt.Sprintf("Operator 重启中：已更新 %d/%d，可用 %d/%d", updated, desired, available, desired)
	}
	if pods, err := c.listPods(ctx, w); err == nil {
		name := ""
		if len(d.Spec.Template.Spec.Containers) > 0 {
			name = d.Spec.Template.Spec.Containers[0].Name
		}
		rs.Pods = toPods(pods, name)
	}
	return rs, nil
}
