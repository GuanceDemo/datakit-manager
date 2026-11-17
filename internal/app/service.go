// Package app is the backend shared by the desktop app (Wails) and the web server.
// Every exported method of Service is callable from the frontend.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"datakit-manager/internal/catalog"
	"datakit-manager/internal/dk"
	"datakit-manager/internal/hostdk"
	"datakit-manager/internal/store"
)

// Service is the application backend.
type Service struct {
	store   *store.Store
	version string
	web     bool
	mu      sync.Mutex
	clients map[string]*dk.Client
}

// New creates the backend. dataDir "" means the per-user config directory. web marks the
// server deployment, where kubeconfigs are uploaded instead of picked from local disk.
func New(dataDir, version string, web bool) (*Service, error) {
	var st *store.Store
	var err error
	if dataDir == "" {
		st, err = store.New()
	} else {
		st, err = store.NewAt(dataDir)
	}
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(st.Dir(), "uploads"), 0o700); err != nil {
		return nil, err
	}
	return &Service{store: st, version: version, web: web, clients: map[string]*dk.Client{}}, nil
}

func timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (s *Service) client(id string) (*dk.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if cl, ok := s.clients[id]; ok && cl.Namespace == c.Namespace {
		return cl, nil
	}
	kubeconfig, err := s.store.Kubeconfig(id)
	if err != nil {
		return nil, err
	}
	cl, err := dk.NewClient(kubeconfig, c.Namespace)
	if err != nil {
		return nil, err
	}
	s.clients[id] = cl
	return cl, nil
}

func (s *Service) drop(id string) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

// ---------- app info

// AppInfo tells the frontend how it is deployed.
type AppInfo struct {
	Version string `json:"version"`
	Mode    string `json:"mode"` // desktop / web
	DataDir string `json:"dataDir"`
}

// Info returns version, deployment mode and the data directory.
func (s *Service) Info() AppInfo {
	mode := "desktop"
	if s.web {
		mode = "web"
	}
	return AppInfo{Version: s.version, Mode: mode, DataDir: s.store.Dir()}
}

// DataDir is where clusters, kubeconfig copies and backups are stored.
func (s *Service) DataDir() string { return s.store.Dir() }

// Samples returns the official sample conf of every collector.
func (s *Service) Samples() ([]catalog.Sample, error) { return catalog.Samples() }

// InstallDefaults returns default images / versions for the install forms.
func (s *Service) InstallDefaults() dk.InstallDefaults { return dk.Defaults() }

// ---------- clusters

// ListClusters returns the imported clusters.
func (s *Service) ListClusters() ([]store.Cluster, error) { return s.store.List() }

func (s *Service) uploadsDir() string { return filepath.Join(s.store.Dir(), "uploads") }

// uploaded returns the content of a file stored by UploadKubeconfig, given its handle.
// Uploads are kept encrypted like every other credential.
func (s *Service) uploaded(handle string) ([]byte, error) {
	clean := filepath.Clean(handle)
	if filepath.Dir(clean) != s.uploadsDir() {
		return nil, errors.New("请先上传 kubeconfig 文件")
	}
	b, err := s.store.Box().ReadFile(clean)
	if err != nil {
		return nil, errors.New("上传的 kubeconfig 已失效，请重新上传")
	}
	return b, nil
}

func (s *Service) isUpload(path string) bool {
	return filepath.Dir(filepath.Clean(path)) == s.uploadsDir()
}

// UploadKubeconfig stores kubeconfig content sent by the browser and returns a handle for
// ParseKubeconfig / ImportClusters.
func (s *Service) UploadKubeconfig(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", errors.New("kubeconfig 内容为空")
	}
	if len(content) > 4<<20 {
		return "", errors.New("kubeconfig 文件过大")
	}
	if _, err := store.ParseKubeconfigBytes([]byte(content)); err != nil {
		return "", err
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	path := filepath.Join(s.uploadsDir(), hex.EncodeToString(b)+".enc")
	return path, s.store.Box().WriteFile(path, []byte(content))
}

// ParseKubeconfig lists the contexts in a kubeconfig: an upload handle, or (desktop only)
// a file on local disk.
func (s *Service) ParseKubeconfig(path string) ([]store.ContextInfo, error) {
	if s.web || s.isUpload(path) {
		b, err := s.uploaded(path)
		if err != nil {
			return nil, err
		}
		return store.ParseKubeconfigBytes(b)
	}
	return store.ParseKubeconfig(path)
}

// ImportClusters imports the chosen contexts; namespace is DataKit's namespace.
func (s *Service) ImportClusters(path string, contexts []string, namespace string) ([]store.Cluster, error) {
	if s.web || s.isUpload(path) {
		b, err := s.uploaded(path)
		if err != nil {
			return nil, err
		}
		added, err := s.store.ImportBytes(b, contexts, namespace)
		_ = os.Remove(filepath.Clean(path)) // the store keeps its own encrypted copy
		return added, err
	}
	return s.store.Import(path, contexts, namespace)
}

// UpdateCluster renames a cluster and/or changes the DataKit namespace.
func (s *Service) UpdateCluster(id, name, namespace string) (store.Cluster, error) {
	s.drop(id)
	return s.store.Update(id, name, namespace)
}

// RemoveCluster forgets a cluster (nothing is changed in the cluster itself).
func (s *Service) RemoveCluster(id string) error {
	s.drop(id)
	return s.store.Remove(id)
}

// ---------- datakit

// Inspect reads DataKit's deployment state in the cluster.
func (s *Service) Inspect(id string) (*dk.Inspection, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(40 * time.Second)
	defer cancel()
	return c.Inspect(ctx)
}

// PreviewChanges returns the list of actions ApplyChanges would perform.
func (s *Service) PreviewChanges(id string, cs dk.ChangeSet) (*dk.PlanView, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(40 * time.Second)
	defer cancel()
	return c.Preview(ctx, cs)
}

// ApplyChanges writes the change and rolling-restarts DataKit. An empty ChangeSet only restarts.
func (s *Service) ApplyChanges(id string, cs dk.ChangeSet) (*dk.ApplyResult, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	dir, _ := s.store.BackupDir(id)
	ctx, cancel := timeout(60 * time.Second)
	defer cancel()
	return c.Apply(ctx, cs, dir)
}

// RolloutStatus reports rolling-restart progress.
func (s *Service) RolloutStatus(id string) (*dk.RolloutStatus, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(30 * time.Second)
	defer cancel()
	return c.Rollout(ctx)
}

// Metrics scrapes DataKit's own /metrics from every pod (or just one when pod != "").
func (s *Service) Metrics(id, pod string) ([]dk.PodMetrics, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(45 * time.Second)
	defer cancel()
	return c.Metrics(ctx, pod)
}

// PodLogs returns the last lines of a pod's log.
func (s *Service) PodLogs(id, pod, container string, tail int) (string, error) {
	c, err := s.client(id)
	if err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = 300
	}
	ctx, cancel := timeout(30 * time.Second)
	defer cancel()
	return c.Logs(ctx, pod, container, int64(tail))
}

// ---------- install

// RenderDataKitYAML renders the DataKit manifest for the cluster's namespace.
func (s *Service) RenderDataKitYAML(id string, o dk.DataKitInstall) (string, error) {
	c, err := s.store.Get(id)
	if err != nil {
		return "", err
	}
	o.Namespace = c.Namespace
	return dk.RenderDataKit(o)
}

// RenderOperatorYAML renders the DataKit Operator manifest.
func (s *Service) RenderOperatorYAML(id string, o dk.OperatorInstall) (string, error) {
	c, err := s.store.Get(id)
	if err != nil {
		return "", err
	}
	o.DataKitNamespace = c.Namespace
	return dk.RenderOperator(o)
}

// ApplyYAML applies a manifest to the cluster (like kubectl apply) and returns what was done.
func (s *Service) ApplyYAML(id, yaml string) ([]string, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	if dir, err := s.store.BackupDir(id); err == nil {
		_ = os.WriteFile(filepath.Join(dir, time.Now().Format("20060102-150405")+"-applied.yaml"), []byte(yaml), 0o600)
	}
	ctx, cancel := timeout(90 * time.Second)
	defer cancel()
	done, err := c.ApplyYAML(ctx, yaml)
	if err != nil && len(done) > 0 {
		return done, fmt.Errorf("%w（之前的 %d 个资源已应用）", err, len(done))
	}
	return done, err
}

// ---------- operator

// OperatorStatus reads the DataKit Operator deployment and config.
func (s *Service) OperatorStatus(id string) (*dk.OperatorStatus, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(30 * time.Second)
	defer cancel()
	return c.Operator(ctx)
}

// ApplyOperatorConfig writes the operator JSON config and restarts the operator.
func (s *Service) ApplyOperatorConfig(id, config string) (*dk.ApplyResult, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	dir, _ := s.store.BackupDir(id)
	ctx, cancel := timeout(60 * time.Second)
	defer cancel()
	return c.ApplyOperatorConfig(ctx, config, dir)
}

// OperatorRollout reports the operator restart progress.
func (s *Service) OperatorRollout(id string) (*dk.RolloutStatus, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(30 * time.Second)
	defer cancel()
	return c.OperatorRollout(ctx)
}

// ---------- versions

// Versions are the newest published versions, when the download site is reachable.
type Versions struct {
	DataKit string `json:"datakit"`
	Error   string `json:"error"`
}

// LatestVersions asks the official download site for the newest DataKit version.
func (s *Service) LatestVersions() Versions {
	ctx, cancel := timeout(12 * time.Second)
	defer cancel()
	v, err := hostdk.LatestVersion(ctx, "")
	if err != nil {
		return Versions{Error: err.Error()}
	}
	return Versions{DataKit: v}
}

// ---------- hosts (DataKit installed on Linux machines, managed over SSH)

// HostImport describes hosts to add. One private key is shared by all of them.
type HostImport struct {
	KeyPath    string   `json:"keyPath"`    // desktop only: private key file on local disk
	KeyContent string   `json:"keyContent"` // or the key itself
	Passphrase string   `json:"passphrase"`
	Hosts      []string `json:"hosts"` // "[user@]address[:port] [display name]"
}

// HostImportResult reports which hosts were added and which could not be reached.
type HostImportResult struct {
	Added  []store.Host `json:"added"`
	Errors []string     `json:"errors"`
}

func parseHostLine(line string) (store.Host, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return store.Host{}, errors.New("空行")
	}
	h := store.Host{User: "root", Port: 22}
	spec := fields[0]
	if i := strings.LastIndex(spec, "@"); i >= 0 {
		h.User, spec = spec[:i], spec[i+1:]
	}
	if i := strings.LastIndex(spec, ":"); i >= 0 && !strings.Contains(spec[:i], ":") {
		p, err := strconv.Atoi(spec[i+1:])
		if err != nil || p < 1 || p > 65535 {
			return h, fmt.Errorf("%s：端口不正确", line)
		}
		h.Port, spec = p, spec[:i]
	}
	if spec == "" || h.User == "" {
		return h, fmt.Errorf("%s：格式应为 [用户@]地址[:端口]", line)
	}
	h.Address = spec
	h.Name = strings.Join(fields[1:], " ")
	if h.Name == "" {
		h.Name = spec
	}
	return h, nil
}

// ImportHosts connects to every listed host with the given key and stores the ones that
// answer. The key is saved encrypted; the host key seen now is pinned for later connections.
func (s *Service) ImportHosts(req HostImport) (*HostImportResult, error) {
	key := req.KeyContent
	if key == "" && req.KeyPath != "" {
		if s.web {
			return nil, errors.New("Web 模式下请直接上传私钥内容")
		}
		b, err := os.ReadFile(req.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥文件失败: %w", err)
		}
		key = string(b)
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("请选择 SSH 私钥")
	}
	if _, err := hostdk.ParseKey(key, req.Passphrase); err != nil {
		return nil, err
	}
	res := &HostImportResult{Added: []store.Host{}, Errors: []string{}}
	var lines []string
	for _, l := range req.Hosts {
		for _, x := range strings.Split(l, "\n") {
			if x = strings.TrimSpace(x); x != "" && !strings.HasPrefix(x, "#") {
				lines = append(lines, x)
			}
		}
	}
	if len(lines) == 0 {
		return nil, errors.New("请至少填写一台主机")
	}
	for _, line := range lines {
		h, err := parseHostLine(line)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		ctx, cancel := timeout(25 * time.Second)
		conn, err := hostdk.Dial(ctx, hostdk.Target{Address: h.Address, Port: h.Port, User: h.User, PrivateKey: key, Passphrase: req.Passphrase})
		cancel()
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", line, err))
			continue
		}
		h.HostKey = conn.HostKey
		conn.Close()
		added, err := s.store.AddHost(h, store.HostSecret{PrivateKey: key, Passphrase: req.Passphrase})
		if err != nil {
			return res, err
		}
		res.Added = append(res.Added, added)
	}
	return res, nil
}

// ListHosts returns the imported hosts (without credentials).
func (s *Service) ListHosts() ([]store.Host, error) { return s.store.ListHosts() }

// UpdateHost renames a host.
func (s *Service) UpdateHost(id, name string) (store.Host, error) {
	return s.store.UpdateHost(id, name)
}

// RemoveHost forgets a host and deletes its stored key (nothing changes on the host).
func (s *Service) RemoveHost(id string) error { return s.store.RemoveHost(id) }

func (s *Service) dial(id string, d time.Duration) (*hostdk.Conn, store.Host, context.Context, context.CancelFunc, error) {
	h, sec, err := s.store.GetHost(id)
	if err != nil {
		return nil, h, nil, nil, err
	}
	ctx, cancel := timeout(d)
	conn, err := hostdk.Dial(ctx, hostdk.Target{Address: h.Address, Port: h.Port, User: h.User, PrivateKey: sec.PrivateKey, Passphrase: sec.Passphrase, HostKey: h.HostKey})
	if err != nil {
		cancel()
		return nil, h, nil, nil, err
	}
	return conn, h, ctx, func() { conn.Close(); cancel() }, nil
}

// HostInspect reads the host's facts, DataKit state and collector configs.
func (s *Service) HostInspect(id string) (*hostdk.Info, error) {
	conn, _, ctx, done, err := s.dial(id, 45*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.Inspect(ctx)
}

// HostPreview validates a change and lists what HostApply would do.
func (s *Service) HostPreview(id string, ch hostdk.Changes) (*dk.PlanView, error) {
	conn, _, _, done, err := s.dial(id, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	actions, warnings, err := conn.Plan(ch)
	if err != nil {
		return nil, err
	}
	return &dk.PlanView{Actions: actions, Warnings: warnings}, nil
}

// HostApply writes the config files and restarts DataKit on the host.
func (s *Service) HostApply(id string, ch hostdk.Changes) (*dk.ApplyResult, error) {
	conn, _, ctx, done, err := s.dial(id, 90*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	actions, warnings, previous, err := conn.Apply(ctx, ch)
	res := &dk.ApplyResult{Actions: actions, Warnings: warnings}
	if len(previous) > 0 {
		if dir, derr := s.store.BackupDir(id); derr == nil {
			stamp := time.Now().Format("20060102-150405")
			for p, content := range previous {
				f := filepath.Join(dir, stamp+"-"+filepath.Base(p))
				if os.WriteFile(f, []byte(content), 0o600) == nil {
					res.BackupFile = dir
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// HostStatus reports whether DataKit is back up after a restart.
func (s *Service) HostStatus(id string) (*dk.RolloutStatus, error) {
	conn, _, ctx, done, err := s.dial(id, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	info, err := conn.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	rs := &dk.RolloutStatus{Desired: 1, Pods: []dk.Pod{}}
	if info.Running {
		rs.Updated, rs.Ready, rs.Available, rs.Done = 1, 1, 1, true
		rs.Message = "datakit 服务运行中（" + info.Version + "）"
	} else {
		rs.Message = "等待 datakit 服务启动…（当前状态：" + info.Service + "）"
	}
	return rs, nil
}

// HostMetrics scrapes DataKit's /metrics on the host; same shape as the cluster call.
func (s *Service) HostMetrics(id string) ([]dk.PodMetrics, error) {
	conn, h, ctx, done, err := s.dial(id, 40*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	info, err := conn.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	name := info.Hostname
	if name == "" {
		name = h.Address
	}
	return []dk.PodMetrics{conn.Metrics(ctx, info.HTTPListen, name)}, nil
}

// HostLogs returns the tail of DataKit's log on the host.
func (s *Service) HostLogs(id string, lines int) (string, error) {
	conn, _, ctx, done, err := s.dial(id, 30*time.Second)
	if err != nil {
		return "", err
	}
	defer done()
	return conn.Logs(ctx, lines)
}

// HostInstall installs (or upgrades) DataKit on the host and returns the installer output.
func (s *Service) HostInstall(id string, o hostdk.InstallOptions) (string, error) {
	conn, _, ctx, done, err := s.dial(id, 20*time.Minute)
	if err != nil {
		return "", err
	}
	defer done()
	return conn.Install(ctx, o, filepath.Join(s.store.Dir(), "cache", "datakit-v2"))
}
