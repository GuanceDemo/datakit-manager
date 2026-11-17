package dk

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

// Official deployment manifests, taken from the datakit / datakit-operator repositories.
var (
	//go:embed templates/datakit.yaml
	datakitTemplate string
	//go:embed templates/datakit-operator.yaml
	operatorTemplate string
	//go:embed templates/operator-cabundle.txt
	operatorCABundle string
)

// Defaults offered by the install forms. Versions are the ones the embedded manifests were
// taken from; the user can type a newer one.
const (
	DefaultDataKitVersion  = "2.12.0"
	DefaultDataKitRepo     = "pubrepo.guance.com/datakit"
	DefaultOperatorVersion = "v1.9.2"
	DefaultOperatorRepo    = "pubrepo.guance.com/datakit-operator"
	// OperatorNamespace is fixed: the TLS certificate baked into the operator image is issued
	// for datakit-operator.datakit.svc, so the webhook only works in this namespace.
	OperatorNamespace = "datakit"
)

// InstallDefaults is sent to the UI to prefill the install forms.
type InstallDefaults struct {
	DataKitVersion    string `json:"datakitVersion"`
	DataKitRepo       string `json:"datakitRepo"`
	OperatorVersion   string `json:"operatorVersion"`
	OperatorRepo      string `json:"operatorRepo"`
	OperatorNamespace string `json:"operatorNamespace"`
	EnabledInputs     string `json:"enabledInputs"`
}

// Defaults returns the install defaults.
func Defaults() InstallDefaults {
	return InstallDefaults{
		DataKitVersion: DefaultDataKitVersion, DataKitRepo: DefaultDataKitRepo,
		OperatorVersion: DefaultOperatorVersion, OperatorRepo: DefaultOperatorRepo, OperatorNamespace: OperatorNamespace,
		EnabledInputs: "statsd,dk,cpu,disk,diskio,mem,swap,system,hostobject,net,host_processes,container,kubernetesprometheus",
	}
}

// DataKitInstall are the knobs of the DataKit install form.
type DataKitInstall struct {
	Namespace       string `json:"namespace"`
	Image           string `json:"image"`   // full image reference
	DataWay         string `json:"dataway"` // https://openway.guance.com?token=tkn_xxx
	ClusterName     string `json:"clusterName"`
	EnabledInputs   string `json:"enabledInputs"`
	GlobalHostTags  string `json:"globalHostTags"`
	ImagePullSecret string `json:"imagePullSecret"`
}

// OperatorInstall are the knobs of the DataKit Operator install form.
type OperatorInstall struct {
	Image            string `json:"image"`            // operator image, full reference
	OperatorRepo     string `json:"operatorRepo"`     // repo prefix of dd-lib-*-init images
	DataKitRepo      string `json:"datakitRepo"`      // repo prefix of logfwd / flameshot images
	DataKitNamespace string `json:"datakitNamespace"` // where the DataKit service lives
	ImagePullSecret  string `json:"imagePullSecret"`
}

// SplitYAML decodes a multi-document YAML stream, skipping empty documents.
func SplitYAML(text string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(text), 4096)
	var out []*unstructured.Unstructured
	for i := 1; ; i++ {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("第 %d 个 YAML 文档解析失败: %w", i, err)
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		if u.GetKind() == "" || u.GetAPIVersion() == "" {
			return nil, fmt.Errorf("第 %d 个 YAML 文档缺少 apiVersion / kind", i)
		}
		out = append(out, u)
	}
}

func joinYAML(objs []*unstructured.Unstructured) (string, error) {
	var b bytes.Buffer
	for i, o := range objs {
		if i > 0 {
			b.WriteString("---\n")
		}
		y, err := yaml.Marshal(o.Object)
		if err != nil {
			return "", err
		}
		b.Write(y)
	}
	return b.String(), nil
}

var clusterScoped = map[string]bool{"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true, "MutatingWebhookConfiguration": true}

// setNamespace moves every namespaced object (and RBAC subjects) from one namespace to another.
func setNamespace(objs []*unstructured.Unstructured, from, to string) {
	for _, o := range objs {
		switch {
		case o.GetKind() == "Namespace":
			if o.GetName() == from {
				o.SetName(to)
			}
		case !clusterScoped[o.GetKind()]:
			o.SetNamespace(to)
		}
		if subs, ok, _ := unstructured.NestedSlice(o.Object, "subjects"); ok {
			for _, s := range subs {
				if sm, ok := s.(map[string]any); ok && sm["namespace"] == from {
					sm["namespace"] = to
				}
			}
			_ = unstructured.SetNestedSlice(o.Object, subs, "subjects")
		}
	}
}

// editPodSpec applies fn to the first container of a workload's pod template.
func editPodSpec(o *unstructured.Unstructured, pullSecret string, fn func(container map[string]any)) error {
	ctrs, ok, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
	if !ok || len(ctrs) == 0 {
		return fmt.Errorf("%s/%s 没有容器定义", o.GetKind(), o.GetName())
	}
	c, ok := ctrs[0].(map[string]any)
	if !ok {
		return fmt.Errorf("%s/%s 容器定义格式不正确", o.GetKind(), o.GetName())
	}
	fn(c)
	if err := unstructured.SetNestedSlice(o.Object, ctrs, "spec", "template", "spec", "containers"); err != nil {
		return err
	}
	if pullSecret != "" {
		return unstructured.SetNestedSlice(o.Object, []any{map[string]any{"name": pullSecret}}, "spec", "template", "spec", "imagePullSecrets")
	}
	return nil
}

func setEnv(container map[string]any, name, value string) {
	envs, _ := container["env"].([]any)
	for _, e := range envs {
		if em, ok := e.(map[string]any); ok && em["name"] == name {
			em["value"] = value
			delete(em, "valueFrom")
			return
		}
	}
	container["env"] = append(envs, map[string]any{"name": name, "value": value})
}

// RenderDataKit produces the DataKit manifest (official datakit.yaml) for the given options.
func RenderDataKit(o DataKitInstall) (string, error) {
	if strings.TrimSpace(o.DataWay) == "" {
		return "", errors.New("请填写 DataWay 地址（包含工作空间 token）")
	}
	if !strings.Contains(o.DataWay, "://") {
		return "", errors.New("DataWay 地址需要以 http:// 或 https:// 开头")
	}
	if o.Namespace == "" {
		o.Namespace = "datakit"
	}
	if o.Image == "" {
		o.Image = DefaultDataKitRepo + "/datakit:" + DefaultDataKitVersion
	}
	text := strings.NewReplacer(
		"{{.DockerImageRepo}}/datakit:{{.Version}}", o.Image,
		"{{.DockerImageRepo}}", DefaultDataKitRepo,
		"{{.BrandDomain}}", "guance.com",
	).Replace(datakitTemplate)
	objs, err := SplitYAML(text)
	if err != nil {
		return "", err
	}
	setNamespace(objs, "datakit", o.Namespace)
	for _, obj := range objs {
		if obj.GetKind() != "DaemonSet" {
			continue
		}
		err := editPodSpec(obj, o.ImagePullSecret, func(c map[string]any) {
			c["image"] = o.Image
			setEnv(c, "ENV_DATAWAY", strings.TrimSpace(o.DataWay))
			if o.ClusterName != "" {
				setEnv(c, "ENV_CLUSTER_NAME_K8S", o.ClusterName)
			}
			if o.EnabledInputs != "" {
				setEnv(c, "ENV_DEFAULT_ENABLED_INPUTS", strings.Join(splitList(o.EnabledInputs), ","))
			}
			if o.GlobalHostTags != "" {
				setEnv(c, "ENV_GLOBAL_HOST_TAGS", o.GlobalHostTags)
			}
		})
		if err != nil {
			return "", err
		}
	}
	return joinYAML(objs)
}

// RenderOperator produces the DataKit Operator manifest for the given options.
func RenderOperator(o OperatorInstall) (string, error) {
	if o.OperatorRepo == "" {
		o.OperatorRepo = DefaultOperatorRepo
	}
	if o.DataKitRepo == "" {
		o.DataKitRepo = DefaultDataKitRepo
	}
	if o.Image == "" {
		o.Image = DefaultOperatorRepo + "/datakit-operator:" + DefaultOperatorVersion
	}
	if o.DataKitNamespace == "" {
		o.DataKitNamespace = "datakit"
	}
	version := imageTag(o.Image)
	text := strings.NewReplacer(
		"(@REPOSITORY):(@APP_VERSION)", o.Image,
		"(@APP_VERSION)", version,
		"(@BRAND_REPO_DATAKIT_OPERATOR)", strings.TrimRight(o.OperatorRepo, "/"),
		"(@BRAND_REPO_DATAKIT)", strings.TrimRight(o.DataKitRepo, "/"),
		"(@CABUNDLE)", strings.TrimSpace(operatorCABundle),
		// injected agents report to the DataKit service, wherever DataKit lives
		"datakit-service.datakit", "datakit-service."+o.DataKitNamespace,
	).Replace(operatorTemplate)
	objs, err := SplitYAML(text)
	if err != nil {
		return "", err
	}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": OperatorNamespace}}}
	objs = append([]*unstructured.Unstructured{ns}, objs...)
	if o.ImagePullSecret != "" {
		for _, obj := range objs {
			if obj.GetKind() == "Deployment" {
				if err := editPodSpec(obj, o.ImagePullSecret, func(map[string]any) {}); err != nil {
					return "", err
				}
			}
		}
	}
	return joinYAML(objs)
}

func (c *Client) dynamicClient() (dynamic.Interface, meta.RESTMapper, error) {
	if c.cfg == nil {
		return nil, nil, errors.New("当前客户端不支持应用 YAML")
	}
	dyn, err := dynamic.NewForConfig(c.cfg)
	if err != nil {
		return nil, nil, err
	}
	cs, ok := c.cs.(*kubernetes.Clientset)
	if !ok {
		return nil, nil, errors.New("当前客户端不支持应用 YAML")
	}
	return dyn, restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cs.Discovery())), nil
}

// ApplyYAML applies every document of a manifest, like `kubectl apply`. Namespaced objects
// without a namespace go to the client's namespace. It stops at the first failure.
func (c *Client) ApplyYAML(ctx context.Context, text string) ([]string, error) {
	objs, err := SplitYAML(text)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errors.New("YAML 中没有任何资源")
	}
	dyn, mapper, err := c.dynamicClient()
	if err != nil {
		return nil, err
	}
	var done []string
	for _, o := range objs {
		gvk := o.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return done, fmt.Errorf("集群不认识资源类型 %s: %w", gvk.String(), err)
		}
		var ri dynamic.ResourceInterface
		label := o.GetKind() + "/" + o.GetName()
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if o.GetNamespace() == "" {
				o.SetNamespace(c.Namespace)
			}
			ri = dyn.Resource(mapping.Resource).Namespace(o.GetNamespace())
			label += "（" + o.GetNamespace() + "）"
		} else {
			ri = dyn.Resource(mapping.Resource)
		}
		verb, err := applyOne(ctx, ri, o)
		if err != nil {
			return done, fmt.Errorf("%s 失败: %w", label, err)
		}
		done = append(done, verb+" "+label)
	}
	return done, nil
}

// applyOne uses server-side apply, falling back to create / update on clusters without it.
func applyOne(ctx context.Context, ri dynamic.ResourceInterface, o *unstructured.Unstructured) (string, error) {
	data, err := json.Marshal(o.Object)
	if err != nil {
		return "", err
	}
	force := true
	_, err = ri.Patch(ctx, o.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "datakit-manager", Force: &force})
	if err == nil {
		return "已应用", nil
	}
	if !apierrors.IsUnsupportedMediaType(err) && !apierrors.IsMethodNotSupported(err) {
		return "", err
	}
	cur, gerr := ri.Get(ctx, o.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(gerr) {
		_, err = ri.Create(ctx, o, metav1.CreateOptions{})
		return "已创建", err
	}
	if gerr != nil {
		return "", gerr
	}
	if o.GetKind() == "Service" || o.GetKind() == "Namespace" {
		return "已存在，跳过", nil // immutable fields (clusterIP) make a blind update unsafe
	}
	o.SetResourceVersion(cur.GetResourceVersion())
	_, err = ri.Update(ctx, o, metav1.UpdateOptions{})
	return "已更新", err
}
