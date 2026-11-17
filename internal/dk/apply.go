package dk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func (c *Client) prepare(ctx context.Context, cs ChangeSet) (*plan, *workload, map[string]*corev1.ConfigMap, error) {
	w, err := c.findWorkload(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if w == nil {
		return nil, nil, nil, fmt.Errorf("命名空间 %s 中没有找到 DataKit", c.Namespace)
	}
	ci := datakitContainer(w.template())
	l := analyze(w.template(), ci, w.claimTemplates())
	cms := map[string]*corev1.ConfigMap{}
	for _, name := range cmNamesFor(cs, l.managedCM) {
		cm, err := c.getConfigMap(ctx, name)
		if err != nil {
			return nil, nil, nil, err
		}
		cms[name] = cm
	}
	// files in the persisted directory are written inside the pods: how many are running?
	running := 0
	needPods := false
	for _, u := range cs.ConfUpserts {
		needPods = needPods || u.Path != ""
	}
	for _, d := range cs.ConfDeletes {
		needPods = needPods || d.Path != ""
	}
	if needPods {
		pods, err := c.listPods(ctx, w)
		if err != nil {
			return nil, nil, nil, err
		}
		running = len(runningPods(pods, w.template().Spec.Containers[ci].Name))
	}
	p, err := buildPlan(w, cms, c.Namespace, cs, time.Now().Format(time.RFC3339), running)
	return p, w, cms, err
}

// Preview computes what Apply would do without changing anything.
func (c *Client) Preview(ctx context.Context, cs ChangeSet) (*PlanView, error) {
	p, _, _, err := c.prepare(ctx, cs)
	if err != nil {
		return nil, err
	}
	return &p.view, nil
}

// Apply writes the ConfigMap(s), patches the workload (volumes, mounts, env) and triggers a
// rolling restart. backupDir, when set, receives a YAML snapshot of the previous state.
func (c *Client) Apply(ctx context.Context, cs ChangeSet, backupDir string) (*ApplyResult, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		p, orig, cms, err := c.prepare(ctx, cs)
		if err != nil {
			return nil, err
		}
		res := &ApplyResult{Actions: p.view.Actions, Warnings: p.view.Warnings}
		if backupDir != "" && attempt == 0 {
			if f, err := writeBackup(backupDir, orig, cms); err == nil {
				res.BackupFile = f
			} else {
				res.Warnings = append(res.Warnings, "备份失败: "+err.Error())
			}
		}

		// 0. persisted directory: write / remove the files inside every running pod
		if attempt == 0 && len(p.fileWrites)+len(p.fileDeletes) > 0 {
			warns, err := c.applyFiles(ctx, orig, p)
			if err != nil {
				return nil, err
			}
			res.Warnings = append(res.Warnings, warns...)
		}

		// 1. new / changed keys first, so new pods find their subPath
		for name, cm := range p.cmBefore {
			if err := c.writeCM(ctx, cm, p.cmCreate[name]); err != nil {
				return nil, fmt.Errorf("写入 ConfigMap %s 失败: %w", name, err)
			}
			delete(p.cmCreate, name)
		}
		// 2. workload
		if err := c.updateWorkload(ctx, p.w); err != nil {
			if apierrors.IsConflict(err) {
				lastErr = err
				continue
			}
			return nil, fmt.Errorf("更新 %s/%s 失败: %w", p.w.kind(), p.w.name(), err)
		}
		// 3. removed keys last
		for name, cm := range p.cmAfter {
			cur, err := c.getConfigMap(ctx, name)
			if err != nil || cur == nil {
				continue
			}
			cur.Data = cm.Data
			if err := c.writeCM(ctx, cur, false); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("清理 ConfigMap %s 失败: %v", name, err))
			}
		}
		return res, nil
	}
	return nil, fmt.Errorf("资源被并发修改，重试后仍然冲突: %w", lastErr)
}

func (c *Client) writeCM(ctx context.Context, cm *corev1.ConfigMap, create bool) error {
	var err error
	if create {
		_, err = c.cs.CoreV1().ConfigMaps(c.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			create = false
		}
	}
	if !create {
		cur, gerr := c.cs.CoreV1().ConfigMaps(c.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		cur.Data = cm.Data
		_, err = c.cs.CoreV1().ConfigMaps(c.Namespace).Update(ctx, cur, metav1.UpdateOptions{})
	}
	return err
}

func writeBackup(dir string, w *workload, cms map[string]*corev1.ConfigMap) (string, error) {
	var parts []string
	b, err := yaml.Marshal(w.forBackup())
	if err != nil {
		return "", err
	}
	parts = append(parts, string(b))
	for _, cm := range cms {
		if cm == nil {
			continue
		}
		x := cm.DeepCopy()
		x.ManagedFields = nil
		x.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}
		b, err := yaml.Marshal(x)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(b))
	}
	f := filepath.Join(dir, time.Now().Format("20060102-150405")+".yaml")
	return f, os.WriteFile(f, []byte(strings.Join(parts, "---\n")), 0o600)
}

// Logs returns the tail of a pod's datakit container log.
func (c *Client) Logs(ctx context.Context, pod, container string, tail int64) (string, error) {
	opts := &corev1.PodLogOptions{TailLines: &tail}
	if container != "" {
		opts.Container = container
	}
	b, err := c.cs.CoreV1().Pods(c.Namespace).GetLogs(pod, opts).DoRaw(ctx)
	return string(b), err
}

// applyFiles writes and removes conf files in the persisted directory, pod by pod.
func (c *Client) applyFiles(ctx context.Context, w *workload, p *plan) ([]string, error) {
	container := w.template().Spec.Containers[datakitContainer(w.template())].Name
	all, err := c.listPods(ctx, w)
	if err != nil {
		return nil, err
	}
	pods := runningPods(all, container)
	if len(pods) == 0 {
		return nil, fmt.Errorf("没有运行中的 DataKit Pod，无法写入持久化目录")
	}
	res := onPods(ctx, pods, func(ctx context.Context, pod string) error {
		for _, f := range p.fileWrites {
			if _, err := c.execPod(ctx, pod, container, []string{"sh", "-c", writeScript, "sh", f.Path}, []byte(f.Content)); err != nil {
				return fmt.Errorf("写入 %s: %w", f.Path, err)
			}
		}
		for _, path := range p.fileDeletes {
			if _, err := c.execPod(ctx, pod, container, []string{"rm", "-f", "--", path}, nil); err != nil {
				return fmt.Errorf("删除 %s: %w", path, err)
			}
		}
		return nil
	})
	var warns []string
	failed := 0
	var first error
	for _, r := range res {
		if r.err != nil {
			failed++
			if first == nil {
				first = r.err
			}
			warns = append(warns, fmt.Sprintf("节点 %s（%s）写入失败：%v", r.node, r.pod, r.err))
		}
	}
	if failed == len(res) {
		return nil, fmt.Errorf("所有 Pod 都写入失败（需要 pods/exec 权限，且目录可写）：%w", first)
	}
	if len(all) > len(pods) {
		warns = append(warns, fmt.Sprintf("有 %d 个 Pod 未在运行，未写入；它们恢复后如使用各自的目录，需要重新应用", len(all)-len(pods)))
	}
	return warns, nil
}
