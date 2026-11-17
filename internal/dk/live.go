package dk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// execFunc runs a command in a pod's container and returns its stdout.
type execFunc func(ctx context.Context, pod, container string, cmd []string, stdin []byte) (string, error)

func (c *Client) execPod(ctx context.Context, pod, container string, cmd []string, stdin []byte) (string, error) {
	if c.execFn != nil {
		return c.execFn(ctx, pod, container, cmd, stdin)
	}
	if c.cfg == nil {
		return "", errors.New("当前客户端不支持在 Pod 内执行命令")
	}
	req := c.cs.CoreV1().RESTClient().Post().Namespace(c.Namespace).Resource("pods").Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(c.cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	opts := remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}
	if stdin != nil {
		opts.Stdin = bytes.NewReader(stdin)
	}
	if err := ex.StreamWithContext(ctx, opts); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

// runningPods keeps the pods whose DataKit container is actually running.
func runningPods(pods []corev1.Pod, container string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == container && cs.State.Running != nil {
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.NodeName+out[i].Name < out[j].Spec.NodeName+out[j].Name })
	return out
}

// ListScript is listScript for other transports (SSH hosts).
const ListScript = listScript

// ParseListing parses ListScript output into absolute path -> content, for files under dir.
func ParseListing(out, marker, dir string) map[string]string {
	files := map[string]string{}
	for p, c := range parseListing(out, marker) {
		files[dir+strings.TrimPrefix(p, ConfDir)] = c
	}
	return files
}

// listScript prints every *.conf under $1 (the main datakit.conf excluded), each preceded by a marker line "<$2> <path>".
// -L follows the symlinks of directory-mounted ConfigMaps; their hidden "..data" copies are skipped.
const listScript = `cd "$1" 2>/dev/null || exit 0
find -L . -type f -name '*.conf' -not -name 'datakit.conf' -size -512k -not -path '*/..*' 2>/dev/null | sort | while IFS= read -r f; do
  printf '\n%s %s\n' "$2" "$f"
  cat "$f"
done`

// liveConfs returns the conf files DataKit actually has on disk in one pod: absolute path -> content.
func (c *Client) liveConfs(ctx context.Context, pod, container string) (map[string]string, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	marker := "@@dkm-" + hex.EncodeToString(b)
	out, err := c.execPod(ctx, pod, container, []string{"sh", "-c", listScript, "sh", ConfDir, marker}, nil)
	if err != nil {
		return nil, err
	}
	return parseListing(out, marker), nil
}

func parseListing(out, marker string) map[string]string {
	files := map[string]string{}
	parts := strings.Split(out, "\n"+marker+" ")
	for _, part := range parts[1:] {
		nl := strings.IndexByte(part, '\n')
		if nl < 0 {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimSpace(part[:nl]), "./")
		files[ConfDir+"/"+rel] = part[nl+1:]
	}
	return files
}

// writeScript writes stdin to $1 atomically, creating parent directories.
const writeScript = `set -e; mkdir -p "$(dirname "$1")"; cat > "$1.dkm-tmp"; mv -f "$1.dkm-tmp" "$1"`

type podResult struct {
	pod, node string
	err       error
}

// onPods runs fn on every pod, 8 at a time.
func onPods(ctx context.Context, pods []corev1.Pod, fn func(ctx context.Context, pod string) error) []podResult {
	res := make([]podResult, len(pods))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range pods {
		res[i] = podResult{pod: p.Name, node: p.Spec.NodeName}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			res[i].err = fn(cctx, name)
		}(i, p.Name)
	}
	wg.Wait()
	return res
}

// maxLivePods caps how many pods are inspected when files may differ per pod.
const maxLivePods = 30

// syncLive reconciles ConfFiles (so far: ConfigMap keys) with what running pods have on disk,
// adding the files found in the persisted directory and the container-only ones.
func (c *Client) syncLive(ctx context.Context, in *Inspection, l *layout, pods []corev1.Pod, container string) {
	for i := range in.ConfFiles {
		f := &in.ConfFiles[i]
		f.Source, f.Editable, f.Path = "configmap", true, f.MountPath
	}
	run := runningPods(pods, container)
	if len(run) == 0 {
		in.LiveError = "没有运行中的 DataKit Pod，无法读取容器内实际生效的配置文件。"
		return
	}
	// ConfigMap / generated files are identical on every pod; a persisted directory is not
	n := 1
	if len(l.dirMounts) > 0 {
		n = maxLivePods
	}
	if len(run) > n {
		run = run[:n]
	}
	listings := make([]map[string]string, len(run))
	var mu sync.Mutex
	res := onPods(ctx, run, func(ctx context.Context, pod string) error {
		m, err := c.liveConfs(ctx, pod, container)
		if err != nil {
			return err
		}
		mu.Lock()
		for i := range run {
			if run[i].Name == pod {
				listings[i] = m
			}
		}
		mu.Unlock()
		return nil
	})
	okPods := 0
	var firstErr error
	for i, r := range res {
		if r.err != nil {
			listings[i] = nil
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		okPods++
	}
	if okPods == 0 {
		in.LiveError = fmt.Sprintf("读取容器内配置文件失败（需要 pods/exec 权限）：%v", firstErr)
		return
	}
	in.LiveSynced, in.LivePods = true, okPods

	type seen struct {
		contents []string
	}
	all := map[string]*seen{}
	var order []string
	for _, m := range listings {
		for path, content := range m {
			s := all[path]
			if s == nil {
				s = &seen{}
				all[path] = s
				order = append(order, path)
			}
			s.contents = append(s.contents, content)
		}
	}
	sort.Strings(order)

	// ConfigMap-backed files: are they loaded, and with the saved content?
	for i := range in.ConfFiles {
		f := &in.ConfFiles[i]
		s := all[f.Path]
		if f.Path == "" || s == nil {
			continue
		}
		f.Live, f.Pods = true, len(s.contents)
		for _, content := range s.contents {
			if content != f.Content {
				f.Drift = "pending"
			}
		}
		delete(all, f.Path)
	}
	for _, path := range order {
		s := all[path]
		if s == nil {
			continue
		}
		f := ConfFile{Path: path, MountPath: path, Key: path[strings.LastIndexByte(path, '/')+1:], Content: s.contents[0],
			Live: true, Mounted: true, Pods: len(s.contents), Inputs: []string{}}
		if l.dirFor(path) != nil {
			f.Source, f.Editable = "volume", true
			if len(s.contents) < okPods {
				f.Drift = "inconsistent"
			}
			for _, content := range s.contents {
				if content != f.Content {
					f.Drift = "inconsistent"
				}
			}
		} else {
			f.Source = "container"
		}
		if ins, err := ParseInputs(f.Content); err != nil {
			f.Error = err.Error()
		} else if ins != nil {
			f.Inputs = ins
		}
		in.ConfFiles = append(in.ConfFiles, f)
	}
	rank := map[string]int{"volume": 0, "configmap": 1, "container": 2}
	sort.SliceStable(in.ConfFiles, func(i, j int) bool {
		a, b := in.ConfFiles[i], in.ConfFiles[j]
		if rank[a.Source] != rank[b.Source] {
			return rank[a.Source] < rank[b.Source]
		}
		return a.Path+a.Key < b.Path+b.Key
	})
}
