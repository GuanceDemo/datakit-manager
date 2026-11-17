package dk

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
)

// PodMetrics is DataKit's self-reported state (from its /metrics endpoint) for one pod.
type PodMetrics struct {
	Pod        string  `json:"pod"`
	Node       string  `json:"node"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error"`
	Via        string  `json:"via"` // proxy / exec
	Version    string  `json:"version"`
	Hostname   string  `json:"hostname"`
	OSArch     string  `json:"osArch"`
	BuildAt    string  `json:"buildAt"`
	UptimeSec  float64 `json:"uptimeSec"`
	CPUUsage   float64 `json:"cpuUsage"` // percent
	CPUCores   float64 `json:"cpuCores"`
	MemRSS     float64 `json:"memRss"` // bytes
	Goroutines float64 `json:"goroutines"`
	OpenFiles  float64 `json:"openFiles"`

	ElectionStatus    string `json:"electionStatus"`
	ElectedID         string `json:"electedId"`
	ElectionNamespace string `json:"electionNamespace"`

	Inputs  []InputInstance `json:"inputs"`
	Feeds   []FeedStat      `json:"feeds"`
	Uploads []UploadStat    `json:"uploads"`
	Errors  []LastError     `json:"errors"`
}

// InputInstance is one enabled collector.
type InputInstance struct {
	Name      string  `json:"name"`
	Instances float64 `json:"instances"`
	Crashes   float64 `json:"crashes"`
}

// FeedStat is what one collector has fed into DataKit's IO, per data category.
type FeedStat struct {
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Feeds        float64 `json:"feeds"`
	Points       float64 `json:"points"`
	Filtered     float64 `json:"filtered"`
	Errors       float64 `json:"errors"`
	LastFeed     float64 `json:"lastFeed"` // unix seconds
	AvgCollectMs float64 `json:"avgCollectMs"`
}

// UploadStat is what was sent to Dataway per category.
type UploadStat struct {
	Category string  `json:"category"`
	Total    float64 `json:"total"`
	OK       float64 `json:"ok"`
	Failed   float64 `json:"failed"`
}

// LastError is the latest error reported by a collector or sub module.
type LastError struct {
	Source   string `json:"source"`
	Category string `json:"category"`
	Error    string `json:"error"`
}

func labels(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

func value(m *dto.Metric) float64 {
	switch {
	case m.Gauge != nil:
		return m.GetGauge().GetValue()
	case m.Counter != nil:
		return m.GetCounter().GetValue()
	case m.Untyped != nil:
		return m.GetUntyped().GetValue()
	case m.Summary != nil:
		return m.GetSummary().GetSampleSum()
	case m.Histogram != nil:
		return m.GetHistogram().GetSampleSum()
	}
	return 0
}

func sumCount(m *dto.Metric) (float64, float64) {
	if m.Summary != nil {
		return m.GetSummary().GetSampleSum(), float64(m.GetSummary().GetSampleCount())
	}
	if m.Histogram != nil {
		return m.GetHistogram().GetSampleSum(), float64(m.GetHistogram().GetSampleCount())
	}
	return value(m), 1
}

func clean(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// ParseMetrics turns DataKit's Prometheus exposition text into PodMetrics.
// Metric names changed across DataKit versions, so several aliases are tried.
func ParseMetrics(text []byte) (*PodMetrics, error) {
	var parser expfmt.TextParser
	mfs, err := parser.TextToMetricFamilies(bytes.NewReader(text))
	if err != nil && len(mfs) == 0 {
		return nil, err
	}
	pm := &PodMetrics{OK: true, Inputs: []InputInstance{}, Feeds: []FeedStat{}, Uploads: []UploadStat{}, Errors: []LastError{}}
	first := func(names ...string) *dto.MetricFamily {
		for _, n := range names {
			if mf := mfs[n]; mf != nil {
				return mf
			}
		}
		return nil
	}
	single := func(names ...string) float64 {
		if mf := first(names...); mf != nil && len(mf.Metric) > 0 {
			return clean(value(mf.Metric[0]))
		}
		return 0
	}

	if mf := first("datakit_uptime_seconds"); mf != nil && len(mf.Metric) > 0 {
		m := mf.Metric[0]
		pm.UptimeSec = clean(value(m))
		ls := labels(m)
		pm.Version, pm.Hostname, pm.OSArch, pm.BuildAt = ls["version"], ls["hostname"], ls["os_arch"], ls["build_at"]
	}
	pm.CPUUsage = single("datakit_cpu_usage")
	pm.CPUCores = single("datakit_cpu_cores")
	pm.Goroutines = single("datakit_goroutines")
	pm.OpenFiles = single("datakit_open_files")
	if mf := first("datakit_mem_stat"); mf != nil {
		for _, m := range mf.Metric {
			if labels(m)["type"] == "rss" {
				pm.MemRSS = clean(value(m))
			}
		}
	}
	if pm.MemRSS == 0 {
		pm.MemRSS = single("datakit_sys_alloc_bytes")
	}
	if mf := first("datakit_election_status"); mf != nil && len(mf.Metric) > 0 {
		ls := labels(mf.Metric[0])
		pm.ElectionStatus, pm.ElectedID, pm.ElectionNamespace = ls["status"], ls["elected_id"], ls["namespace"]
	}

	// enabled inputs
	crashes := map[string]float64{}
	if mf := first("datakit_inputs_crash_total"); mf != nil {
		for _, m := range mf.Metric {
			crashes[labels(m)["input"]] += value(m)
		}
	}
	if mf := first("datakit_inputs_instance"); mf != nil {
		for _, m := range mf.Metric {
			name := labels(m)["input"]
			pm.Inputs = append(pm.Inputs, InputInstance{Name: name, Instances: clean(value(m)), Crashes: crashes[name]})
		}
	}
	sort.Slice(pm.Inputs, func(i, j int) bool { return pm.Inputs[i].Name < pm.Inputs[j].Name })

	// feeds
	type key struct{ name, cat string }
	feeds := map[key]*FeedStat{}
	get := func(ls map[string]string, nameLabel string) *FeedStat {
		k := key{ls[nameLabel], ls["category"]}
		if k.name == "" {
			return nil
		}
		f := feeds[k]
		if f == nil {
			f = &FeedStat{Name: k.name, Category: k.cat}
			feeds[k] = f
		}
		return f
	}
	if mf := first("datakit_io_feed_total"); mf != nil {
		for _, m := range mf.Metric {
			if f := get(labels(m), "name"); f != nil {
				f.Feeds += clean(value(m))
			}
		}
	}
	if mf := first("datakit_io_feed_point", "datakit_io_feed_point_total"); mf != nil {
		for _, m := range mf.Metric {
			if f := get(labels(m), "name"); f != nil {
				f.Points += clean(value(m))
			}
		}
	}
	if mf := first("datakit_io_input_filter_point_total"); mf != nil {
		for _, m := range mf.Metric {
			if f := get(labels(m), "name"); f != nil {
				f.Filtered += clean(value(m))
			}
		}
	}
	if mf := first("datakit_io_last_feed_timestamp_seconds"); mf != nil {
		for _, m := range mf.Metric {
			if f := get(labels(m), "name"); f != nil {
				f.LastFeed = math.Max(f.LastFeed, clean(value(m)))
			}
		}
	}
	if mf := first("datakit_input_collect_latency_seconds"); mf != nil {
		for _, m := range mf.Metric {
			if f := get(labels(m), "name"); f != nil {
				if s, n := sumCount(m); n > 0 {
					f.AvgCollectMs = clean(s / n * 1000)
				}
			}
		}
	}
	if mf := first("datakit_error_total"); mf != nil {
		for _, m := range mf.Metric {
			ls := labels(m)
			k := key{ls["source"], ls["category"]}
			if f := feeds[k]; f != nil {
				f.Errors += clean(value(m))
			} else if k.name != "" {
				feeds[k] = &FeedStat{Name: k.name, Category: k.cat, Errors: clean(value(m))}
			}
		}
	}
	for _, f := range feeds {
		pm.Feeds = append(pm.Feeds, *f)
	}
	sort.Slice(pm.Feeds, func(i, j int) bool {
		if pm.Feeds[i].Name != pm.Feeds[j].Name {
			return pm.Feeds[i].Name < pm.Feeds[j].Name
		}
		return pm.Feeds[i].Category < pm.Feeds[j].Category
	})

	// uploads
	if mf := first("datakit_io_endpoint_point_total", "datakit_io_dataway_point_total"); mf != nil {
		ups := map[string]*UploadStat{}
		for _, m := range mf.Metric {
			ls := labels(m)
			u := ups[ls["category"]]
			if u == nil {
				u = &UploadStat{Category: ls["category"]}
				ups[ls["category"]] = u
			}
			switch ls["status"] {
			case "total":
				u.Total += clean(value(m))
			case "OK", "ok":
				u.OK += clean(value(m))
			default:
				u.Failed += clean(value(m))
			}
		}
		for _, u := range ups {
			if u.Total == 0 {
				u.Total = u.OK + u.Failed
			}
			pm.Uploads = append(pm.Uploads, *u)
		}
		sort.Slice(pm.Uploads, func(i, j int) bool { return pm.Uploads[i].Category < pm.Uploads[j].Category })
	}

	if mf := first("datakit_last_err"); mf != nil {
		for _, m := range mf.Metric {
			ls := labels(m)
			src := ls["source"]
			if src == "" {
				src = ls["input"]
			}
			pm.Errors = append(pm.Errors, LastError{Source: src, Category: ls["category"], Error: ls["error"]})
		}
	}
	return pm, nil
}

func (c *Client) rawMetrics(ctx context.Context, pod, container string, port int, local bool) ([]byte, string, error) {
	var proxyErr error
	if !local {
		b, err := c.cs.CoreV1().RESTClient().Get().Namespace(c.Namespace).Resource("pods").
			Name(fmt.Sprintf("%s:%d", pod, port)).SubResource("proxy").Suffix("metrics").DoRaw(ctx)
		if err == nil {
			return b, "proxy", nil
		}
		proxyErr = err
	}
	// Fallback: ask the pod itself (works when DataKit only listens on loopback).
	cmd := fmt.Sprintf("curl -s --max-time 8 http://127.0.0.1:%d/metrics || wget -qO- -T 8 http://127.0.0.1:%d/metrics", port, port)
	out, err := c.execPod(ctx, pod, container, []string{"sh", "-c", cmd}, nil)
	if err != nil || out == "" {
		msg := "无输出"
		if err != nil {
			msg = err.Error()
		}
		if proxyErr != nil {
			msg = fmt.Sprintf("API 代理访问失败（%v）；exec 访问失败（%s）", proxyErr, msg)
		}
		return nil, "", fmt.Errorf("%s", msg)
	}
	return []byte(out), "exec", nil
}

// MaxMetricPods caps how many pods one Metrics call scrapes.
const MaxMetricPods = 60

// Metrics scrapes DataKit's /metrics on every (running) pod, concurrently.
// Election collectors (MySQL, Redis...) only run on the elected pod, hence all pods.
func (c *Client) Metrics(ctx context.Context, only string) ([]PodMetrics, error) {
	w, err := c.findWorkload(ctx)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("命名空间 %s 中没有找到 DataKit", c.Namespace)
	}
	ctr := w.template().Spec.Containers[datakitContainer(w.template())]
	port, local := httpListen(ctr.Env)
	pods, err := c.listPods(ctx, w)
	if err != nil {
		return nil, err
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Spec.NodeName+pods[i].Name < pods[j].Spec.NodeName+pods[j].Name })

	var targets []corev1.Pod
	for _, p := range pods {
		if only != "" && p.Name != only {
			continue
		}
		targets = append(targets, p)
	}
	if len(targets) > MaxMetricPods {
		targets = targets[:MaxMetricPods]
	}

	out := make([]PodMetrics, len(targets))
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for i, p := range targets {
		out[i] = PodMetrics{Pod: p.Name, Node: p.Spec.NodeName, Inputs: []InputInstance{}, Feeds: []FeedStat{}, Uploads: []UploadStat{}, Errors: []LastError{}}
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			out[i].Error = "Pod 未处于 Running 状态"
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			raw, via, err := c.rawMetrics(cctx, name, ctr.Name, port, local)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			pm, err := ParseMetrics(raw)
			if err != nil {
				out[i].Error = "解析指标失败: " + err.Error()
				return
			}
			pm.Pod, pm.Node, pm.Via = out[i].Pod, out[i].Node, via
			out[i] = *pm
		}(i, p.Name)
	}
	wg.Wait()
	return out, nil
}
