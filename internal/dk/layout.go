package dk

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	corev1 "k8s.io/api/core/v1"
)

func imageName(img string) string {
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	if i := strings.Index(img, ":"); i >= 0 {
		img = img[:i]
	}
	return img
}

func imageTag(img string) string {
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	last := img
	if i := strings.LastIndex(img, "/"); i >= 0 {
		last = img[i+1:]
	}
	if i := strings.Index(last, ":"); i >= 0 {
		return last[i+1:]
	}
	return ""
}

func underConfDir(p string) bool {
	p = strings.TrimRight(p, "/")
	return p == ConfDir || strings.HasPrefix(p, ConfDir+"/")
}

// layout is how conf.d is wired in the pod template.
type layout struct {
	mounts     []ConfMount
	cmVolumes  map[string]string // volume name -> ConfigMap name
	cmDirMount map[string]string // ConfigMap name -> directory mountPath (mounted without subPath)
	dirVolumes []string
	dirMounts  []dirMount // persistent directories at / under conf.d, shortest path first
	managedCM  string
	managedVol string
	volDecl    bool
	mode       string
}

func volumeKind(v corev1.Volume) (kind, detail string) {
	switch {
	case v.ConfigMap != nil:
		return "configMap", v.ConfigMap.Name
	case v.HostPath != nil:
		return "hostPath", v.HostPath.Path
	case v.PersistentVolumeClaim != nil:
		return "pvc", v.PersistentVolumeClaim.ClaimName
	case v.EmptyDir != nil:
		return "emptyDir", ""
	case v.Secret != nil:
		return "secret", v.Secret.SecretName
	default:
		return "other", ""
	}
}

// dirMount is a persistent (hostPath / PVC / network) directory mounted at or under conf.d.
type dirMount struct {
	path     string
	readOnly bool
	desc     string
	perNode  bool // hostPath: every node has its own copy
}

// volumeDir is where new conf files go when conf.d itself is persisted: the writable
// persistent directory closest to conf.d. "" when there is none.
func (l *layout) volumeDir() *dirMount {
	for i := range l.dirMounts {
		if !l.dirMounts[i].readOnly {
			return &l.dirMounts[i]
		}
	}
	return nil
}

// dirFor returns the writable persistent directory that contains path, if any.
func (l *layout) dirFor(path string) *dirMount {
	var best *dirMount
	for i := range l.dirMounts {
		d := &l.dirMounts[i]
		if !d.readOnly && strings.HasPrefix(path, d.path+"/") && (best == nil || len(d.path) > len(best.path)) {
			best = d
		}
	}
	return best
}

// analyze reads how conf.d is wired. claims are StatefulSet volumeClaimTemplate names.
func analyze(t *corev1.PodTemplateSpec, ci int, claims map[string]bool) *layout {
	l := &layout{cmVolumes: map[string]string{}, cmDirMount: map[string]string{}, mode: "none"}
	vols := map[string]corev1.Volume{}
	for _, v := range t.Spec.Volumes {
		vols[v.Name] = v
		if v.ConfigMap != nil {
			l.cmVolumes[v.Name] = v.ConfigMap.Name
		}
	}

	cmUse := map[string]int{}
	hasCM, hasDir := false, false
	for _, m := range t.Spec.Containers[ci].VolumeMounts {
		if !underConfDir(m.MountPath) {
			continue
		}
		kind, detail := volumeKind(vols[m.Name])
		if _, declared := vols[m.Name]; !declared && claims[m.Name] {
			kind, detail = "pvc", "volumeClaimTemplate "+m.Name
		}
		cmnt := ConfMount{MountPath: m.MountPath, SubPath: m.SubPath, Volume: m.Name, Kind: kind}
		if kind == "configMap" {
			cmnt.ConfigMap = detail
			cmUse[detail]++
			hasCM = true
			if m.SubPath == "" {
				l.cmDirMount[detail] = strings.TrimRight(m.MountPath, "/")
			}
		} else {
			desc := kind
			if detail != "" {
				desc += " " + detail
			}
			l.dirVolumes = append(l.dirVolumes, fmt.Sprintf("%s → %s", desc, m.MountPath))
			// emptyDir / secret are not persistence; a mount ending in .conf is a single file
			if kind != "emptyDir" && kind != "secret" && !strings.HasSuffix(m.MountPath, ".conf") {
				hasDir = true
				l.dirMounts = append(l.dirMounts, dirMount{path: strings.TrimRight(m.MountPath, "/"), readOnly: m.ReadOnly, desc: desc, perNode: kind == "hostPath"})
			}
		}
		l.mounts = append(l.mounts, cmnt)
	}
	sort.SliceStable(l.dirMounts, func(i, j int) bool { return len(l.dirMounts[i].path) < len(l.dirMounts[j].path) })
	// A persisted conf directory wins: new configs keep going there. ConfigMap is used when
	// there is no such directory (existing ConfigMap mounts, or nothing at all).
	switch {
	case hasDir && l.volumeDir() != nil:
		l.mode = "volume"
	case hasCM:
		l.mode = "configmap"
	}

	// Managed ConfigMap: the one most used under conf.d, else the conventional datakit-conf.
	best := ""
	for name, n := range cmUse {
		if best == "" || n > cmUse[best] || (n == cmUse[best] && name == DefaultConfigMap) {
			best = name
		}
	}
	if best == "" {
		for vol, cm := range l.cmVolumes {
			if cm == DefaultConfigMap || vol == DefaultConfigMap {
				best = cm
				break
			}
		}
	}
	if best == "" {
		best = DefaultConfigMap
	}
	l.managedCM = best
	l.managedVol, l.volDecl = l.volumeFor(best)
	return l
}

// volumeFor returns the volume name projecting ConfigMap cm, and whether it is declared.
func (l *layout) volumeFor(cm string) (string, bool) {
	names := make([]string, 0, len(l.cmVolumes))
	for vol, c := range l.cmVolumes {
		if c == cm {
			names = append(names, vol)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return names[0], true
	}
	return "", false
}

// mountOf reports where key of ConfigMap cm lands in the container.
func (l *layout) mountOf(t *corev1.PodTemplateSpec, ci int, cm, key string) (string, bool) {
	for _, m := range t.Spec.Containers[ci].VolumeMounts {
		if l.cmVolumes[m.Name] != cm {
			continue
		}
		if m.SubPath == key {
			return m.MountPath, true
		}
		if m.SubPath == "" {
			return strings.TrimRight(m.MountPath, "/") + "/" + key, true
		}
	}
	return "", false
}

var (
	keyRe = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
	envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseInputs validates TOML and returns the collector names found under [inputs].
func ParseInputs(content string) ([]string, error) {
	var m map[string]any
	if _, err := toml.Decode(content, &m); err != nil {
		return nil, err
	}
	in, ok := m["inputs"].(map[string]any)
	if !ok {
		return nil, nil
	}
	names := make([]string, 0, len(in))
	for k := range in {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

func httpListen(env []corev1.EnvVar) (port int, local bool) {
	port = 9529
	for _, e := range env {
		if e.Name != "ENV_HTTP_LISTEN" || e.Value == "" {
			continue
		}
		host := e.Value
		if i := strings.LastIndex(e.Value, ":"); i >= 0 {
			host = e.Value[:i]
			if p, err := strconv.Atoi(e.Value[i+1:]); err == nil {
				port = p
			}
		}
		local = host == "localhost" || host == "127.0.0.1" || host == "[::1]"
	}
	return
}

func envRef(e corev1.EnvVar) string {
	f := e.ValueFrom
	switch {
	case f == nil:
		return ""
	case f.FieldRef != nil:
		return "fieldRef: " + f.FieldRef.FieldPath
	case f.SecretKeyRef != nil:
		return fmt.Sprintf("secret: %s/%s", f.SecretKeyRef.Name, f.SecretKeyRef.Key)
	case f.ConfigMapKeyRef != nil:
		return fmt.Sprintf("configMap: %s/%s", f.ConfigMapKeyRef.Name, f.ConfigMapKeyRef.Key)
	case f.ResourceFieldRef != nil:
		return "resourceFieldRef: " + f.ResourceFieldRef.Resource
	}
	return "valueFrom"
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
