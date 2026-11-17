package dk

import (
	"fmt"
	pathpkg "path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// plan is the computed outcome of a ChangeSet against the current cluster state.
type plan struct {
	view     PlanView
	w        *workload                    // mutated copy, ready to Update
	cmBefore map[string]*corev1.ConfigMap // write before the workload (new/changed keys; deleted keys kept)
	cmAfter  map[string]*corev1.ConfigMap // write after the workload (deleted keys removed)
	cmCreate map[string]bool              // ConfigMaps that do not exist yet
	// files in the persisted conf directory, written / removed inside every running pod
	fileWrites  []ConfChange
	fileDeletes []string
	perNode     bool // any touched directory is a hostPath (one copy per node)
}

func (p *plan) act(format string, a ...any) {
	p.view.Actions = append(p.view.Actions, fmt.Sprintf(format, a...))
}
func (p *plan) warn(format string, a ...any) {
	p.view.Warnings = append(p.view.Warnings, fmt.Sprintf(format, a...))
}

// cmNamesFor lists every ConfigMap a ChangeSet touches, given the managed one.
func cmNamesFor(cs ChangeSet, managed string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n == "" {
			n = managed
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, u := range cs.ConfUpserts {
		if u.Path == "" {
			add(u.ConfigMap)
		}
	}
	for _, d := range cs.ConfDeletes {
		if d.Path == "" {
			add(d.ConfigMap)
		}
	}
	return out
}

// buildPlan is pure: it never talks to the cluster. cms maps name -> existing ConfigMap (nil if absent).
func buildPlan(orig *workload, cms map[string]*corev1.ConfigMap, namespace string, cs ChangeSet, stamp string, running int) (*plan, error) {
	p := &plan{w: orig.deepCopy(), cmBefore: map[string]*corev1.ConfigMap{}, cmAfter: map[string]*corev1.ConfigMap{}, cmCreate: map[string]bool{}}
	p.view.Actions, p.view.Warnings = []string{}, []string{}
	t := p.w.template()
	ci := datakitContainer(t)
	if ci < 0 {
		return nil, fmt.Errorf("%s/%s 中没有找到 datakit 容器", p.w.kind(), p.w.name())
	}
	l := analyze(t, ci, p.w.claimTemplates())
	ctr := &t.Spec.Containers[ci]
	wl := p.w.kind() + "/" + p.w.name()

	working := map[string]*corev1.ConfigMap{}
	getCM := func(name string) *corev1.ConfigMap {
		if cm, ok := working[name]; ok {
			return cm
		}
		var cm *corev1.ConfigMap
		if ex := cms[name]; ex != nil {
			cm = ex.DeepCopy()
		} else {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "datakit-manager"}}}
			p.cmCreate[name] = true
			p.act("创建 ConfigMap %s", name)
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		working[name] = cm
		return cm
	}

	changed := false

	// --- files in the persisted conf directory
	podsText := fmt.Sprintf("%d 个运行中的 Pod", running)
	checkPath := func(path string) (*dirMount, error) {
		clean := pathClean(path)
		d := l.dirFor(clean)
		if clean != path || d == nil {
			return nil, fmt.Errorf("%s 不在可写的持久化 conf 目录内", path)
		}
		if base := path[strings.LastIndexByte(path, '/')+1:]; !keyRe.MatchString(base) || !strings.HasSuffix(base, ".conf") {
			return nil, fmt.Errorf("配置文件名 %q 不合法：只能包含字母、数字、- _ .，并以 .conf 结尾", base)
		}
		return d, nil
	}
	for _, u := range cs.ConfUpserts {
		if u.Path == "" {
			continue
		}
		d, err := checkPath(u.Path)
		if err != nil {
			return nil, err
		}
		ins, err := ParseInputs(u.Content)
		if err != nil {
			return nil, fmt.Errorf("%s 不是合法的 TOML: %w", u.Path, err)
		}
		if len(ins) == 0 {
			p.warn("%s 中没有 [[inputs.xxx]] 配置段，DataKit 加载后不会启动任何采集器", u.Path)
		}
		p.fileWrites = append(p.fileWrites, u)
		p.perNode = p.perNode || d.perNode
		p.act("写入持久化目录 %s（%s，%s）", u.Path, d.desc, podsText)
		changed = true
	}
	for _, d := range cs.ConfDeletes {
		if d.Path == "" {
			continue
		}
		dm, err := checkPath(d.Path)
		if err != nil {
			return nil, err
		}
		p.fileDeletes = append(p.fileDeletes, d.Path)
		p.perNode = p.perNode || dm.perNode
		p.act("从持久化目录删除 %s（%s）", d.Path, podsText)
		changed = true
	}
	if len(p.fileWrites)+len(p.fileDeletes) > 0 {
		if running == 0 {
			return nil, fmt.Errorf("没有运行中的 DataKit Pod，无法写入持久化目录")
		}
		if p.perNode {
			p.warn("hostPath 目录每个节点各有一份：本次只会写入当前 %s 所在的节点，之后新加入的节点需要重新应用一次", podsText)
		}
	}

	// --- conf upserts
	for _, u := range cs.ConfUpserts {
		if u.Path != "" {
			continue
		}
		name := u.ConfigMap
		if name == "" {
			name = l.managedCM
		}
		if !keyRe.MatchString(u.Key) {
			return nil, fmt.Errorf("配置文件名 %q 不合法，只能包含字母、数字、- _ .", u.Key)
		}
		if !strings.HasSuffix(u.Key, ".conf") {
			return nil, fmt.Errorf("配置文件名 %q 必须以 .conf 结尾，否则 DataKit 不会加载", u.Key)
		}
		ins, err := ParseInputs(u.Content)
		if err != nil {
			return nil, fmt.Errorf("%s 不是合法的 TOML: %w", u.Key, err)
		}
		if len(ins) == 0 {
			p.warn("%s 中没有 [[inputs.xxx]] 配置段，DataKit 加载后不会启动任何采集器", u.Key)
		}

		cm := getCM(name)
		old, exists := cm.Data[u.Key]
		switch {
		case !exists:
			p.act("ConfigMap %s 新增 %s（采集器：%s）", name, u.Key, strings.Join(ins, ", "))
			changed = true
		case old != u.Content:
			p.act("ConfigMap %s 更新 %s", name, u.Key)
			changed = true
		}
		cm.Data[u.Key] = u.Content
		p.cmBefore[name] = cm

		// make sure the key is mounted
		if _, ok := l.cmDirMount[name]; ok {
			continue // whole ConfigMap is mounted as a directory
		}
		vol, declared := l.volumeFor(name)
		if !declared {
			vol = uniqueVolumeName(t, name)
			t.Spec.Volumes = append(t.Spec.Volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}})
			l.cmVolumes[vol] = name
			p.act("%s 声明卷 %s（ConfigMap %s）", wl, vol, name)
			changed = true
		}
		if _, mounted := l.mountOf(t, ci, name, u.Key); mounted {
			continue
		}
		target := ConfDir + "/" + u.Key
		for _, m := range ctr.VolumeMounts {
			if strings.TrimRight(m.MountPath, "/") == target {
				return nil, fmt.Errorf("挂载路径 %s 已被卷 %s 占用，请换一个文件名", target, m.Name)
			}
		}
		ctr.VolumeMounts = append(ctr.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: target, SubPath: u.Key, ReadOnly: true})
		p.act("%s 新增挂载 %s（subPath: %s）", wl, target, u.Key)
		changed = true
	}

	// --- conf deletes
	for _, d := range cs.ConfDeletes {
		if d.Path != "" {
			continue
		}
		name := d.ConfigMap
		if name == "" {
			name = l.managedCM
		}
		if cms[name] == nil && working[name] == nil {
			p.warn("ConfigMap %s 不存在，忽略删除 %s", name, d.Key)
			continue
		}
		cm := getCM(name)
		if _, ok := cm.Data[d.Key]; !ok {
			p.warn("ConfigMap %s 中没有 %s，忽略删除", name, d.Key)
		}
		// drop the subPath mounts first; the key itself is removed after the workload update,
		// otherwise new pods would fail to start on a missing subPath.
		kept := ctr.VolumeMounts[:0:0]
		for _, m := range ctr.VolumeMounts {
			if l.cmVolumes[m.Name] == name && m.SubPath == d.Key {
				p.act("%s 移除挂载 %s", wl, m.MountPath)
				changed = true
				continue
			}
			kept = append(kept, m)
		}
		ctr.VolumeMounts = kept

		after := p.cmAfter[name]
		if after == nil {
			after = cm.DeepCopy()
		}
		if _, ok := after.Data[d.Key]; ok {
			delete(after.Data, d.Key)
			p.act("ConfigMap %s 删除 %s", name, d.Key)
			changed = true
		}
		p.cmAfter[name] = after
	}
	// keep cmAfter in sync with later upserts into the same ConfigMap
	for name, after := range p.cmAfter {
		if before := p.cmBefore[name]; before != nil {
			for _, u := range cs.ConfUpserts {
				if u.Path != "" {
					continue
				}
				n := u.ConfigMap
				if n == "" {
					n = l.managedCM
				}
				if n == name {
					after.Data[u.Key] = u.Content
				}
			}
		}
	}

	// --- env
	for _, e := range cs.EnvSet {
		if !envRe.MatchString(e.Name) {
			return nil, fmt.Errorf("环境变量名 %q 不合法", e.Name)
		}
		found := false
		for i := range ctr.Env {
			if ctr.Env[i].Name != e.Name {
				continue
			}
			found = true
			if ctr.Env[i].ValueFrom != nil {
				return nil, fmt.Errorf("环境变量 %s 来自 %s，不能在这里修改", e.Name, envRef(ctr.Env[i]))
			}
			if ctr.Env[i].Value != e.Value {
				p.act("环境变量 %s：%q → %q", e.Name, ctr.Env[i].Value, e.Value)
				ctr.Env[i].Value = e.Value
				changed = true
			}
		}
		if !found {
			ctr.Env = append(ctr.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
			p.act("新增环境变量 %s = %q", e.Name, e.Value)
			changed = true
		}
	}
	for _, name := range cs.EnvDelete {
		kept := ctr.Env[:0:0]
		for _, e := range ctr.Env {
			if e.Name == name {
				p.act("删除环境变量 %s", name)
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		ctr.Env = kept
	}

	if !changed {
		p.warn("配置内容没有变化，仅执行重启")
	}
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[RestartAnnotation] = stamp
	if p.w.onDelete() {
		p.act("更新 %s 的 Pod 模板", wl)
		p.warn("%s 的更新策略是 OnDelete：Pod 不会自动重启，应用后需要手动删除 Pod 才会生效", wl)
	} else {
		p.act("滚动重启 %s", wl)
	}
	return p, nil
}

func uniqueVolumeName(t *corev1.PodTemplateSpec, cm string) string {
	used := map[string]bool{}
	for _, v := range t.Spec.Volumes {
		used[v.Name] = true
	}
	base := cm
	if len(base) > 55 {
		base = base[:55]
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// pathClean is path.Clean without importing it under a name that shadows locals.
func pathClean(p string) string { return pathpkg.Clean(p) }
