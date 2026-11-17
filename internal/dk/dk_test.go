package dk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// officialDS mimics the DaemonSet of the official datakit.yaml: the datakit-conf volume is
// declared but nothing is mounted under conf.d.
func officialDS(declareVolume bool) *appsv1.DaemonSet {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "datakit", Namespace: "datakit"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "daemonset-datakit"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "daemonset-datakit"}},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					Containers: []corev1.Container{{
						Name:  "datakit",
						Image: "pubrepo.guance.com/datakit/datakit:1.80.1",
						Env: []corev1.EnvVar{
							{Name: "ENV_K8S_NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
							{Name: "ENV_DATAWAY", Value: "https://openway.guance.com?token=tkn_x"},
							{Name: "ENV_DEFAULT_ENABLED_INPUTS", Value: "cpu,disk,mem,container"},
							{Name: "ENV_HTTP_LISTEN", Value: "0.0.0.0:9529"},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "cache", MountPath: "/usr/local/datakit/cache"}},
					}},
					Volumes: []corev1.Volume{{Name: "cache", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/root/datakit_cache"}}}},
				},
			},
		},
	}
	if declareVolume {
		ds.Spec.Template.Spec.Volumes = append(ds.Spec.Template.Spec.Volumes, corev1.Volume{Name: "datakit-conf",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "datakit-conf"}}}})
	}
	return ds
}

const mysqlConf = "[[inputs.mysql]]\n  host = \"10.0.0.1\"\n  port = 3306\n  [inputs.mysql.tags]\n    env = \"prod\"\n"

func mounts(t *testing.T, c *Client) []corev1.VolumeMount {
	t.Helper()
	ds, err := c.cs.AppsV1().DaemonSets("datakit").Get(context.Background(), "datakit", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ds.Spec.Template.Spec.Containers[0].VolumeMounts
}

func TestNoPersistenceThenApply(t *testing.T) {
	ctx := context.Background()
	for _, declared := range []bool{true, false} {
		c := NewClientFor(fake.NewSimpleClientset(officialDS(declared)), "datakit")

		in, err := c.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !in.Found || in.Persistence.Mode != "none" || in.Persistence.VolumeDeclared != declared || in.Persistence.ConfigMapExists {
			t.Fatalf("declared=%v unexpected persistence %+v", declared, in.Persistence)
		}
		if in.Workload.Version != "1.80.1" || in.Workload.HTTPPort != 9529 || len(in.EnabledInputs) != 4 {
			t.Fatalf("workload %+v inputs %v", in.Workload, in.EnabledInputs)
		}

		cs := ChangeSet{ConfUpserts: []ConfChange{{Key: "mysql.conf", Content: mysqlConf}}}
		pv, err := c.Preview(ctx, cs)
		if err != nil {
			t.Fatal(err)
		}
		if len(mounts(t, c)) != 1 {
			t.Fatal("preview must not change the cluster")
		}
		joined := strings.Join(pv.Actions, "\n")
		for _, want := range []string{"创建 ConfigMap datakit-conf", "新增挂载 /usr/local/datakit/conf.d/mysql.conf", "滚动重启"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("declared=%v preview misses %q:\n%s", declared, want, joined)
			}
		}
		if strings.Contains(joined, "声明卷") == declared {
			t.Fatalf("declared=%v volume action wrong:\n%s", declared, joined)
		}

		if _, err := c.Apply(ctx, cs, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		in, _ = c.Inspect(ctx)
		if in.Persistence.Mode != "configmap" || len(in.ConfFiles) != 1 {
			t.Fatalf("after apply: %+v files=%d", in.Persistence, len(in.ConfFiles))
		}
		f := in.ConfFiles[0]
		if !f.Mounted || f.MountPath != ConfDir+"/mysql.conf" || len(f.Inputs) != 1 || f.Inputs[0] != "mysql" {
			t.Fatalf("file %+v", f)
		}
		if in.Workload.RestartedAt == "" {
			t.Fatal("restart annotation missing")
		}

		// second file reuses the volume; update of the first adds no mount
		cs = ChangeSet{ConfUpserts: []ConfChange{{Key: "redis.conf", Content: "[[inputs.redis]]\nhost='r'\n"}, {Key: "mysql.conf", Content: mysqlConf + "# x\n"}}}
		if _, err := c.Apply(ctx, cs, ""); err != nil {
			t.Fatal(err)
		}
		if n := len(mounts(t, c)); n != 3 {
			t.Fatalf("want 3 mounts, got %d", n)
		}

		// delete removes both the mount and the key
		if _, err := c.Apply(ctx, ChangeSet{ConfDeletes: []ConfRef{{Key: "mysql.conf"}}}, ""); err != nil {
			t.Fatal(err)
		}
		in, _ = c.Inspect(ctx)
		if len(in.ConfFiles) != 1 || in.ConfFiles[0].Key != "redis.conf" || len(mounts(t, c)) != 2 {
			t.Fatalf("after delete files=%+v mounts=%d", in.ConfFiles, len(mounts(t, c)))
		}
	}
}

func TestEnvAndValidation(t *testing.T) {
	ctx := context.Background()
	c := NewClientFor(fake.NewSimpleClientset(officialDS(true)), "datakit")

	if _, err := c.Preview(ctx, ChangeSet{ConfUpserts: []ConfChange{{Key: "bad.conf", Content: "[[inputs.mysql]\n"}}}); err == nil {
		t.Fatal("invalid TOML must be rejected")
	}
	if _, err := c.Preview(ctx, ChangeSet{ConfUpserts: []ConfChange{{Key: "mysql.toml", Content: mysqlConf}}}); err == nil {
		t.Fatal("non .conf name must be rejected")
	}
	if _, err := c.Preview(ctx, ChangeSet{EnvSet: []EnvChange{{Name: "ENV_K8S_NODE_NAME", Value: "x"}}}); err == nil {
		t.Fatal("valueFrom env must not be editable")
	}

	cs := ChangeSet{
		EnvSet:    []EnvChange{{Name: "ENV_DEFAULT_ENABLED_INPUTS", Value: "cpu,disk,mem,container,ddtrace"}, {Name: "ENV_INPUT_DDTRACE_COMPATIBLE_OTEL", Value: "true"}},
		EnvDelete: []string{"ENV_HTTP_LISTEN"},
	}
	if _, err := c.Apply(ctx, cs, ""); err != nil {
		t.Fatal(err)
	}
	in, _ := c.Inspect(ctx)
	got := map[string]string{}
	for _, e := range in.Env {
		got[e.Name] = e.Value
	}
	if got["ENV_INPUT_DDTRACE_COMPATIBLE_OTEL"] != "true" || !strings.HasSuffix(got["ENV_DEFAULT_ENABLED_INPUTS"], "ddtrace") {
		t.Fatalf("env %+v", got)
	}
	if _, ok := got["ENV_HTTP_LISTEN"]; ok {
		t.Fatal("env not deleted")
	}
	if len(in.EnabledInputs) != 5 {
		t.Fatalf("enabled %v", in.EnabledInputs)
	}
}

func TestExistingLayouts(t *testing.T) {
	ctx := context.Background()

	// (a) ConfigMap mounted as a whole directory: new keys need no extra mount
	ds := officialDS(true)
	ds.Spec.Template.Spec.Containers[0].VolumeMounts = append(ds.Spec.Template.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{Name: "datakit-conf", MountPath: ConfDir + "/custom"})
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "datakit-conf", Namespace: "datakit"}, Data: map[string]string{"a.conf": mysqlConf}}
	c := NewClientFor(fake.NewSimpleClientset(ds, cm), "datakit")
	in, _ := c.Inspect(ctx)
	if in.Persistence.Mode != "configmap" || !in.ConfFiles[0].Mounted || in.ConfFiles[0].MountPath != ConfDir+"/custom/a.conf" {
		t.Fatalf("%+v %+v", in.Persistence, in.ConfFiles)
	}
	if _, err := c.Apply(ctx, ChangeSet{ConfUpserts: []ConfChange{{Key: "b.conf", Content: mysqlConf}}}, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(mounts(t, c)); n != 2 {
		t.Fatalf("dir mount: want 2 mounts, got %d", n)
	}

	// (b) conf.d is a hostPath volume, and DataKit lives in another namespace
	ds = officialDS(false)
	ds.Namespace = "monitoring"
	ds.Spec.Template.Spec.Volumes = append(ds.Spec.Template.Spec.Volumes, corev1.Volume{Name: "confd", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data/dk"}}})
	ds.Spec.Template.Spec.Containers[0].VolumeMounts = append(ds.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "confd", MountPath: ConfDir})
	c = NewClientFor(fake.NewSimpleClientset(ds), "monitoring")
	in, _ = c.Inspect(ctx)
	if in.Persistence.Mode != "volume" || len(in.Persistence.DirVolumes) != 1 {
		t.Fatalf("%+v", in.Persistence)
	}
	c = NewClientFor(fake.NewSimpleClientset(ds), "datakit")
	if in, _ = c.Inspect(ctx); in.Found {
		t.Fatal("wrong namespace must report not found")
	}
}

const sampleMetrics = `# TYPE datakit_uptime_seconds gauge
datakit_uptime_seconds{auto_update="false",branch="master",build_at="2026-09-10",cgroup="-",docker="true",hostname="node-1",lite="false",os_arch="linux/amd64",version="1.80.1"} 3600
# TYPE datakit_cpu_usage gauge
datakit_cpu_usage 3.5
# TYPE datakit_mem_stat gauge
datakit_mem_stat{type="rss"} 2.5e+08
datakit_mem_stat{type="vms"} 9e+08
# TYPE datakit_goroutines gauge
datakit_goroutines 420
# TYPE datakit_election_status gauge
datakit_election_status{elected_id="node-1",id="node-1",namespace="default",status="success"} 1.7e+09
# TYPE datakit_inputs_instance gauge
datakit_inputs_instance{input="cpu"} 1
datakit_inputs_instance{input="mysql"} 2
# TYPE datakit_io_feed_total counter
datakit_io_feed_total{category="metric",name="cpu"} 360
datakit_io_feed_total{category="metric",name="mysql"} 100
datakit_io_feed_total{category="logging",name="mysql/dbm"} 7
# TYPE datakit_io_feed_point summary
datakit_io_feed_point_sum{category="metric",name="cpu"} 720
datakit_io_feed_point_count{category="metric",name="cpu"} 360
datakit_io_feed_point_sum{category="metric",name="mysql"} 5000
datakit_io_feed_point_count{category="metric",name="mysql"} 100
# TYPE datakit_io_last_feed_timestamp_seconds gauge
datakit_io_last_feed_timestamp_seconds{category="metric",name="mysql"} 1.79e+09
# TYPE datakit_input_collect_latency_seconds summary
datakit_input_collect_latency_seconds_sum{category="metric",name="mysql"} 5
datakit_input_collect_latency_seconds_count{category="metric",name="mysql"} 100
# TYPE datakit_error_total counter
datakit_error_total{category="metric",source="mysql"} 3
# TYPE datakit_last_err gauge
datakit_last_err{category="metric",error="dial tcp 10.0.0.1:3306: connect: connection refused",input="mysql",source="mysql"} 1.79e+09
# TYPE datakit_io_endpoint_point_total counter
datakit_io_endpoint_point_total{category="metric",owner="dw",status="total"} 5720
datakit_io_endpoint_point_total{category="metric",owner="dw",status="OK"} 5700
`

func TestParseMetrics(t *testing.T) {
	pm, err := ParseMetrics([]byte(sampleMetrics))
	if err != nil {
		t.Fatal(err)
	}
	if pm.Version != "1.80.1" || pm.UptimeSec != 3600 || pm.MemRSS != 2.5e8 || pm.ElectedID != "node-1" || len(pm.Inputs) != 2 {
		t.Fatalf("%+v", pm)
	}
	var my *FeedStat
	for i := range pm.Feeds {
		if pm.Feeds[i].Name == "mysql" {
			my = &pm.Feeds[i]
		}
	}
	if my == nil || my.Feeds != 100 || my.Points != 5000 || my.Errors != 3 || my.AvgCollectMs != 50 || my.LastFeed != 1.79e9 {
		t.Fatalf("mysql feed %+v", my)
	}
	if len(pm.Uploads) != 1 || pm.Uploads[0].OK != 5700 || len(pm.Errors) != 1 {
		t.Fatalf("uploads %+v errors %+v", pm.Uploads, pm.Errors)
	}
}

func TestRenderDataKit(t *testing.T) {
	if _, err := RenderDataKit(DataKitInstall{}); err == nil {
		t.Fatal("dataway is required")
	}
	y, err := RenderDataKit(DataKitInstall{Namespace: "monitoring", Image: "harbor.local/obs/datakit:2.12.0", DataWay: "https://openway.guance.com?token=tkn_abc",
		ClusterName: "prod", EnabledInputs: "cpu, mem ,ddtrace", ImagePullSecret: "regcred"})
	if err != nil {
		t.Fatal(err)
	}
	objs, err := SplitYAML(y)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, o := range objs {
		kinds[o.GetKind()]++
		if !clusterScoped[o.GetKind()] && o.GetNamespace() != "monitoring" {
			t.Fatalf("%s/%s namespace %q", o.GetKind(), o.GetName(), o.GetNamespace())
		}
	}
	for _, k := range []string{"Namespace", "ClusterRole", "ClusterRoleBinding", "ServiceAccount", "Service", "DaemonSet", "ConfigMap"} {
		if kinds[k] != 1 {
			t.Fatalf("want exactly one %s, got %v", k, kinds)
		}
	}
	for _, want := range []string{"image: harbor.local/obs/datakit:2.12.0", "value: https://openway.guance.com?token=tkn_abc", "value: cpu,mem,ddtrace", "name: regcred", "name: monitoring", "value: prod"} {
		if !strings.Contains(y, want) {
			t.Fatalf("rendered yaml misses %q", want)
		}
	}
	if strings.Contains(y, "{{") || strings.Contains(y, "YOUR-WORKSPACE-TOKEN") {
		t.Fatal("placeholders left in yaml")
	}
	// the rendered DaemonSet must be recognised by Inspect's own detection
	for _, o := range objs {
		if o.GetKind() != "DaemonSet" {
			continue
		}
		var ds appsv1.DaemonSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &ds); err != nil {
			t.Fatal(err)
		}
		c := NewClientFor(fake.NewSimpleClientset(&ds), "monitoring")
		in, err := c.Inspect(context.Background())
		if err != nil || !in.Found || in.Workload.Version != "2.12.0" || in.Persistence.Mode != "none" || !in.Persistence.VolumeDeclared {
			t.Fatalf("%v %+v", err, in)
		}
	}
}

func TestRenderOperatorAndConfig(t *testing.T) {
	y, err := RenderOperator(OperatorInstall{Image: "harbor.local/obs/datakit-operator:v1.9.2", OperatorRepo: "harbor.local/obs", DataKitRepo: "harbor.local/obs", DataKitNamespace: "monitoring"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(y, "(@") {
		t.Fatal("placeholders left in yaml")
	}
	for _, want := range []string{"image: harbor.local/obs/datakit-operator:v1.9.2", "harbor.local/obs/dd-lib-java-init:", "harbor.local/obs/logfwd:", "datakit-service.monitoring.svc.cluster.local", "caBundle: LS0t"} {
		if !strings.Contains(y, want) {
			t.Fatalf("rendered yaml misses %q", want)
		}
	}
	objs, _ := SplitYAML(y)
	var cfg string
	for _, o := range objs {
		// the operator itself always lives in the fixed namespace
		if !clusterScoped[o.GetKind()] && o.GetNamespace() != OperatorNamespace {
			t.Fatalf("%s in %q", o.GetKind(), o.GetNamespace())
		}
		if o.GetKind() == "ConfigMap" {
			cfg, _, _ = unstructuredString(o.Object, "data", "jsonconfig")
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil || parsed["admission_inject_v2"] == nil {
		t.Fatalf("operator json config invalid: %v", err)
	}

	// status + config update against a fake cluster
	ctx := context.Background()
	one := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "datakit-operator", Namespace: "datakit"},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "datakit-operator"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "pubrepo.guance.com/datakit-operator/datakit-operator:v1.9.2",
				Env: []corev1.EnvVar{{Name: "ENV_JSON_CONFIG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "datakit-operator-config"}, Key: "jsonconfig"}}}}}}}}}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "datakit-operator-config", Namespace: "datakit"}, Data: map[string]string{"jsonconfig": cfg}}
	c := NewClientFor(fake.NewSimpleClientset(dep, cm), "monitoring")
	st, err := c.Operator(ctx)
	if err != nil || !st.Installed || st.Version != "v1.9.2" || st.Config != cfg || st.ConfigError != "" {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := c.ApplyOperatorConfig(ctx, "{not json", ""); err == nil {
		t.Fatal("invalid json must be rejected")
	}
	if _, err := c.ApplyOperatorConfig(ctx, `{"log_level":"debug"}`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	st, _ = c.Operator(ctx)
	d, _ := c.cs.AppsV1().Deployments("datakit").Get(ctx, "datakit-operator", metav1.GetOptions{})
	if st.Config != `{"log_level":"debug"}` || d.Spec.Template.Annotations[RestartAnnotation] == "" {
		t.Fatalf("config not applied: %q", st.Config)
	}
	empty := NewClientFor(fake.NewSimpleClientset(), "datakit")
	if st, _ := empty.Operator(ctx); st.Installed {
		t.Fatal("operator must be reported missing")
	}
}

func unstructuredString(obj map[string]any, fields ...string) (string, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur = m[f]
	}
	s, ok := cur.(string)
	return s, ok, nil
}

func TestStatefulSet(t *testing.T) {
	ctx := context.Background()
	two := int32(2)
	tpl := officialDS(false).Spec.Template
	tpl.Spec.Containers[0].VolumeMounts = append(tpl.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "confd", MountPath: ConfDir})
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "datakit", Namespace: "datakit"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &two, ServiceName: "datakit",
			Selector:             &metav1.LabelSelector{MatchLabels: map[string]string{"app": "daemonset-datakit"}},
			Template:             tpl,
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "confd"}}},
			UpdateStrategy:       appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2, CurrentRevision: "r1", UpdateRevision: "r1"},
	}
	c := NewClientFor(fake.NewSimpleClientset(sts), "datakit")

	in, err := c.Inspect(ctx)
	if err != nil || !in.Found || in.Workload.Kind != "StatefulSet" || in.Workload.Desired != 2 || in.Workload.Ready != 2 || !in.Workload.RolloutDone {
		t.Fatalf("%v %+v", err, in.Workload)
	}
	// conf.d on a volumeClaimTemplate is recognised as persistent storage
	if in.Persistence.Mode != "volume" || len(in.Persistence.Mounts) != 1 || in.Persistence.Mounts[0].Kind != "pvc" || !strings.Contains(in.Persistence.Summary, "OnDelete") {
		t.Fatalf("%+v", in.Persistence)
	}

	cs := ChangeSet{ConfUpserts: []ConfChange{{Key: "mysql.conf", Content: mysqlConf}}, EnvSet: []EnvChange{{Name: "ENV_LOG_LEVEL", Value: "debug"}}}
	pv, err := c.Preview(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(pv.Actions, "\n"); !strings.Contains(all, "StatefulSet/datakit 新增挂载") || strings.Contains(all, "滚动重启") {
		t.Fatalf("actions:\n%s", all)
	}
	if len(pv.Warnings) == 0 || !strings.Contains(strings.Join(pv.Warnings, ""), "OnDelete") {
		t.Fatalf("OnDelete warning missing: %v", pv.Warnings)
	}
	res, err := c.Apply(ctx, cs, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(res.BackupFile); !strings.Contains(string(b), "kind: StatefulSet") {
		t.Fatal("backup must contain the StatefulSet")
	}
	got, _ := c.cs.AppsV1().StatefulSets("datakit").Get(ctx, "datakit", metav1.GetOptions{})
	ctr := got.Spec.Template.Spec.Containers[0]
	if n := len(ctr.VolumeMounts); n != 3 || ctr.VolumeMounts[2].SubPath != "mysql.conf" || got.Spec.Template.Annotations[RestartAnnotation] == "" {
		t.Fatalf("mounts %+v", ctr.VolumeMounts)
	}
	in, _ = c.Inspect(ctx)
	// an explicit ConfigMap key still works, but the persisted directory stays the default target
	if in.Persistence.Mode != "volume" || len(in.ConfFiles) != 1 || !in.ConfFiles[0].Mounted || in.ConfFiles[0].Source != "configmap" {
		t.Fatalf("%+v %+v", in.Persistence, in.ConfFiles)
	}
	rs, err := c.Rollout(ctx)
	if err != nil || rs.Desired != 2 {
		t.Fatalf("%v %+v", err, rs)
	}

	// a DaemonSet in the same namespace still wins
	both := NewClientFor(fake.NewSimpleClientset(sts, officialDS(true)), "datakit")
	if in, _ := both.Inspect(ctx); in.Workload.Kind != "DaemonSet" {
		t.Fatalf("want DaemonSet, got %s", in.Workload.Kind)
	}
}

// fakePods is an in-memory stand-in for `kubectl exec`: one tiny filesystem per pod that
// understands exactly the three commands the client sends.
type fakePods struct {
	mu    sync.Mutex
	files map[string]map[string]string // pod -> absolute path -> content
}

func (f *fakePods) exec(_ context.Context, pod, _ string, cmd []string, stdin []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fs := f.files[pod]
	if fs == nil {
		return "", fmt.Errorf("pod %s not found", pod)
	}
	switch {
	case len(cmd) == 6 && cmd[2] == listScript:
		paths := make([]string, 0, len(fs))
		for p := range fs {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var b strings.Builder
		for _, p := range paths {
			if strings.HasPrefix(p, cmd[4]+"/") && strings.HasSuffix(p, ".conf") {
				fmt.Fprintf(&b, "\n%s ./%s\n%s", cmd[5], strings.TrimPrefix(p, cmd[4]+"/"), fs[p])
			}
		}
		return b.String(), nil
	case len(cmd) == 5 && cmd[2] == writeScript:
		fs[cmd[4]] = string(stdin)
		return "", nil
	case len(cmd) == 4 && cmd[0] == "rm":
		delete(fs, cmd[3])
		return "", nil
	}
	return "", fmt.Errorf("unexpected command %v", cmd)
}

func runningPod(name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "datakit", Labels: map[string]string{"app": "daemonset-datakit"}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "datakit", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
}

// conf.d persisted on a hostPath: live files are synced from the pods, and new / edited
// configs are written into that directory (no ConfigMap).
func TestPersistedDirectory(t *testing.T) {
	ctx := context.Background()
	ds := officialDS(true)
	ds.Spec.Template.Spec.Volumes = append(ds.Spec.Template.Spec.Volumes, corev1.Volume{Name: "confd", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data/dk-conf"}}})
	ds.Spec.Template.Spec.Containers[0].VolumeMounts = append(ds.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "confd", MountPath: ConfDir})

	redis := "[[inputs.redis]]\n  host = \"r\"\n"
	pods := &fakePods{files: map[string]map[string]string{
		"dk-a": {ConfDir + "/db/redis.conf": redis, ConfDir + "/host/cpu.conf": "[[inputs.cpu]]\n", "/usr/local/datakit/other.conf": "x"},
		"dk-b": {ConfDir + "/db/redis.conf": redis, ConfDir + "/host/cpu.conf": "[[inputs.cpu]]\n"},
	}}
	c := NewClientFor(fake.NewSimpleClientset(ds, runningPod("dk-a", "node-a"), runningPod("dk-b", "node-b")), "datakit")
	c.execFn = pods.exec

	in, err := c.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := in.Persistence
	if p.Mode != "volume" || p.VolumeDir != ConfDir || !p.VolumePerNode || !in.LiveSynced || in.LivePods != 2 {
		t.Fatalf("%+v synced=%v pods=%d err=%s", p, in.LiveSynced, in.LivePods, in.LiveError)
	}
	// 1. the configs actually in effect are listed, straight from the pods
	byPath := map[string]ConfFile{}
	for _, f := range in.ConfFiles {
		byPath[f.Path] = f
	}
	r := byPath[ConfDir+"/db/redis.conf"]
	if len(in.ConfFiles) != 2 || r.Source != "volume" || !r.Editable || !r.Live || r.Pods != 2 || r.Content != redis || len(r.Inputs) != 1 || r.Inputs[0] != "redis" {
		t.Fatalf("%+v", in.ConfFiles)
	}

	// 2. a new config goes into the persisted directory on every running pod, not into a ConfigMap
	target := p.VolumeDir + "/mysql.conf"
	cs := ChangeSet{ConfUpserts: []ConfChange{{Path: target, Content: mysqlConf}}}
	pv, err := c.Preview(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(pv.Actions, "\n")
	if !strings.Contains(all, "写入持久化目录 "+target) || !strings.Contains(all, "2 个运行中的 Pod") || strings.Contains(all, "ConfigMap") || !strings.Contains(all, "滚动重启") {
		t.Fatalf("actions:\n%s", all)
	}
	if _, ok := pods.files["dk-a"][target]; ok {
		t.Fatal("preview must not write")
	}
	if _, err := c.Apply(ctx, cs, ""); err != nil {
		t.Fatal(err)
	}
	if pods.files["dk-a"][target] != mysqlConf || pods.files["dk-b"][target] != mysqlConf {
		t.Fatalf("file not written to both pods: %v", pods.files)
	}
	if cm, _ := c.getConfigMap(ctx, "datakit-conf"); cm != nil {
		t.Fatal("no ConfigMap may be created when conf.d is persisted")
	}
	if got := mounts(t, c); len(got) != 2 {
		t.Fatalf("no subPath mount may be added: %+v", got)
	}
	in, _ = c.Inspect(ctx)
	if in.Workload.RestartedAt == "" || len(in.ConfFiles) != 3 {
		t.Fatalf("restart=%q files=%d", in.Workload.RestartedAt, len(in.ConfFiles))
	}

	// editing in place, then a pod that missed the write shows up as inconsistent
	edited := redis + "  # tuned\n"
	if _, err := c.Apply(ctx, ChangeSet{ConfUpserts: []ConfChange{{Path: ConfDir + "/db/redis.conf", Content: edited}}}, ""); err != nil {
		t.Fatal(err)
	}
	pods.files["dk-b"][ConfDir+"/db/redis.conf"] = redis
	in, _ = c.Inspect(ctx)
	for _, f := range in.ConfFiles {
		if f.Path == ConfDir+"/db/redis.conf" && f.Drift != "inconsistent" {
			t.Fatalf("drift not detected: %+v", f)
		}
	}

	// delete; paths outside the persisted directory are refused
	if _, err := c.Apply(ctx, ChangeSet{ConfDeletes: []ConfRef{{Path: target}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := pods.files["dk-a"][target]; ok {
		t.Fatal("file not deleted")
	}
	for _, bad := range []string{"/etc/passwd.conf", ConfDir + "/../evil.conf", ConfDir + "/a b.conf", ConfDir + "/x.toml"} {
		if _, err := c.Preview(ctx, ChangeSet{ConfUpserts: []ConfChange{{Path: bad, Content: mysqlConf}}}); err == nil {
			t.Fatalf("path %q must be refused", bad)
		}
	}
}

// ConfigMap-managed configs are matched with what the pod really runs.
func TestLiveSyncConfigMap(t *testing.T) {
	ctx := context.Background()
	c := NewClientFor(fake.NewSimpleClientset(officialDS(true), runningPod("dk-a", "node-a")), "datakit")
	pods := &fakePods{files: map[string]map[string]string{"dk-a": {ConfDir + "/host/cpu.conf": "[[inputs.cpu]]\n"}}}
	c.execFn = pods.exec
	if _, err := c.Apply(ctx, ChangeSet{ConfUpserts: []ConfChange{{Key: "mysql.conf", Content: mysqlConf}}}, ""); err != nil {
		t.Fatal(err)
	}
	// the pod has not restarted yet: saved but not in effect
	in, _ := c.Inspect(ctx)
	var my, cpu ConfFile
	for _, f := range in.ConfFiles {
		if f.Key == "mysql.conf" {
			my = f
		}
		if f.Key == "cpu.conf" {
			cpu = f
		}
	}
	if in.Persistence.Mode != "configmap" || my.Source != "configmap" || my.Live || !my.Editable {
		t.Fatalf("%+v", my)
	}
	// generated at start-up for a default-enabled collector: visible, read-only
	if cpu.Source != "container" || cpu.Editable || !cpu.Live {
		t.Fatalf("%+v", cpu)
	}
	// after the restart the pod has it; a later edit shows as pending until the next restart
	pods.files["dk-a"][ConfDir+"/mysql.conf"] = mysqlConf
	in, _ = c.Inspect(ctx)
	for _, f := range in.ConfFiles {
		if f.Key == "mysql.conf" && (!f.Live || f.Drift != "") {
			t.Fatalf("%+v", f)
		}
	}
	pods.files["dk-a"][ConfDir+"/mysql.conf"] = "[[inputs.mysql]]\n"
	in, _ = c.Inspect(ctx)
	for _, f := range in.ConfFiles {
		if f.Key == "mysql.conf" && f.Drift != "pending" {
			t.Fatalf("%+v", f)
		}
	}
	// exec not possible: the ConfigMap view still works, with the reason reported
	c.execFn = func(context.Context, string, string, []string, []byte) (string, error) {
		return "", fmt.Errorf("forbidden")
	}
	in, err := c.Inspect(ctx)
	if err != nil || in.LiveSynced || in.LiveError == "" || len(in.ConfFiles) != 1 {
		t.Fatalf("%v %+v", err, in)
	}
}
