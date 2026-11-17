package dk

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Inspect reads the DataKit workload, its pods, config persistence and env.
func (c *Client) Inspect(ctx context.Context) (*Inspection, error) {
	out := &Inspection{Namespace: c.Namespace, Pods: []Pod{}, ConfFiles: []ConfFile{}, Env: []EnvVar{}, EnabledInputs: []string{}}
	if v, err := c.ServerVersion(); err == nil {
		out.ServerVersion = v
	}

	w, err := c.findWorkload(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 命名空间失败: %w", c.Namespace, err)
	}
	if w == nil {
		out.Message = fmt.Sprintf("命名空间 %s 中没有找到 DataKit（DaemonSet / StatefulSet / Deployment）。请确认命名空间是否正确。", c.Namespace)
		return out, nil
	}
	out.Found = true

	t := w.template()
	ci := datakitContainer(t)
	ctr := t.Spec.Containers[ci]
	desired, updated, ready, available, observed := w.counts()
	port, local := httpListen(ctr.Env)
	out.Workload = Workload{
		Kind: w.kind(), Name: w.name(), Container: ctr.Name, Image: ctr.Image, Version: imageTag(ctr.Image),
		Desired: desired, Updated: updated, Ready: ready, Available: available,
		RolloutDone: observed && updated == desired && available == desired,
		HostNetwork: t.Spec.HostNetwork, HTTPPort: port, HTTPLocal: local,
		RestartedAt: t.Annotations[RestartAnnotation],
	}

	// env
	for _, e := range ctr.Env {
		ev := EnvVar{Name: e.Name, Value: e.Value, FromRef: envRef(e)}
		ev.Editable = ev.FromRef == ""
		out.Env = append(out.Env, ev)
		if e.Name == "ENV_DEFAULT_ENABLED_INPUTS" || (e.Name == "ENV_ENABLE_INPUTS" && len(out.EnabledInputs) == 0) {
			out.EnabledInputs = splitList(e.Value)
		}
	}

	// persistence
	l := analyze(t, ci, w.claimTemplates())
	p := Persistence{Mode: l.mode, ConfigMap: l.managedCM, VolumeName: l.managedVol, VolumeDeclared: l.volDecl,
		Mounts: l.mounts, DirVolumes: l.dirVolumes}
	if d := l.volumeDir(); d != nil && l.mode == "volume" {
		p.VolumeDir, p.VolumeDesc, p.VolumePerNode = d.path, d.desc, d.perNode
	}
	if p.Mounts == nil {
		p.Mounts = []ConfMount{}
	}
	if p.DirVolumes == nil {
		p.DirVolumes = []string{}
	}

	cmNames := map[string]bool{l.managedCM: true}
	for _, m := range l.mounts {
		if m.ConfigMap != "" {
			cmNames[m.ConfigMap] = true
		}
	}
	names := make([]string, 0, len(cmNames))
	for n := range cmNames {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		cm, err := c.getConfigMap(ctx, name)
		if err != nil {
			return nil, err
		}
		if cm == nil {
			continue
		}
		if name == l.managedCM {
			p.ConfigMapExists = true
		}
		keys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f := ConfFile{ConfigMap: name, Key: k, Content: cm.Data[k], Inputs: []string{}}
			f.MountPath, f.Mounted = l.mountOf(t, ci, name, k)
			if strings.HasSuffix(k, ".conf") || strings.HasSuffix(k, ".toml") {
				ins, err := ParseInputs(f.Content)
				if err != nil {
					f.Error = err.Error()
				} else if ins != nil {
					f.Inputs = ins
				}
			}
			out.ConfFiles = append(out.ConfFiles, f)
		}
	}

	switch l.mode {
	case "volume":
		p.Summary = fmt.Sprintf("conf 目录已持久化（%s → %s）。新增的采集器配置直接写入该目录，不使用 ConfigMap。", p.VolumeDesc, p.VolumeDir)
		if p.VolumePerNode {
			p.Summary += "hostPath 目录每个节点各有一份，保存时会写入所有运行中的 Pod。"
		}
	case "configmap":
		p.Summary = fmt.Sprintf("采集器配置通过 ConfigMap %s 挂载到 conf.d，重启后不会丢失。", l.managedCM)
	default:
		switch {
		case l.volDecl && p.ConfigMapExists:
			p.Summary = fmt.Sprintf("已声明 ConfigMap %s 卷，但 conf.d 下没有任何挂载。保存配置时会自动添加 subPath 挂载。", l.managedCM)
		case l.volDecl:
			p.Summary = fmt.Sprintf("已声明卷但 ConfigMap %s 不存在，conf.d 下也没有挂载。保存配置时会自动创建 ConfigMap 并挂载。", l.managedCM)
		default:
			p.Summary = fmt.Sprintf("conf.d 没有任何持久化。保存配置时会自动创建 ConfigMap %s、声明卷并以 subPath 挂载。", l.managedCM)
		}
	}
	if w.onDelete() {
		p.Summary += fmt.Sprintf(" 注意：%s 的更新策略是 OnDelete，修改后需要手动删除 Pod 才会生效。", w.kind())
	}
	out.Persistence = p

	pods, err := c.listPods(ctx, w)
	if err != nil {
		return nil, err
	}
	out.Pods = toPods(pods, ctr.Name)
	c.syncLive(ctx, out, l, pods, ctr.Name)
	return out, nil
}

func toPods(pods []corev1.Pod, container string) []Pod {
	out := make([]Pod, 0, len(pods))
	for _, p := range pods {
		x := Pod{Name: p.Name, Node: p.Spec.NodeName, IP: p.Status.PodIP, Phase: string(p.Status.Phase), Reason: p.Status.Reason}
		if p.Status.StartTime != nil {
			x.StartAt = p.Status.StartTime.Format(time.RFC3339)
		}
		if p.DeletionTimestamp != nil {
			x.Phase = "Terminating"
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != container {
				continue
			}
			x.Ready = cs.Ready
			x.Restarts = cs.RestartCount
			x.Image = cs.Image
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				x.Reason = cs.State.Waiting.Reason
				if cs.State.Waiting.Message != "" {
					x.Reason += ": " + cs.State.Waiting.Message
				}
			} else if cs.State.Terminated != nil {
				x.Reason = cs.State.Terminated.Reason
			}
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node+out[i].Name < out[j].Node+out[j].Name })
	return out
}

// Rollout reports rolling-restart progress.
func (c *Client) Rollout(ctx context.Context) (*RolloutStatus, error) {
	w, err := c.findWorkload(ctx)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("命名空间 %s 中没有找到 DataKit", c.Namespace)
	}
	desired, updated, ready, available, observed := w.counts()
	rs := &RolloutStatus{Desired: desired, Updated: updated, Ready: ready, Available: available}
	rs.Done = observed && updated == desired && available == desired
	switch {
	case w.onDelete() && !rs.Done:
		rs.Message = fmt.Sprintf("%s 的更新策略是 OnDelete：配置已写入，但 Pod 不会自动重启。请手动删除 Pod（kubectl -n %s delete pod <名称>）使其生效。已更新 %d/%d", w.kind(), c.Namespace, updated, desired)
	case !observed:
		rs.Message = "等待控制器处理变更…"
	case rs.Done:
		rs.Message = fmt.Sprintf("重启完成，%d/%d 个 Pod 已就绪", available, desired)
	default:
		rs.Message = fmt.Sprintf("滚动重启中：已更新 %d/%d，可用 %d/%d", updated, desired, available, desired)
	}
	pods, err := c.listPods(ctx, w)
	if err != nil {
		return nil, err
	}
	rs.Pods = toPods(pods, w.template().Spec.Containers[datakitContainer(w.template())].Name)
	return rs, nil
}
