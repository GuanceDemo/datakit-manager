// Package store persists the cluster list and kubeconfig copies on local disk.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"datakit-manager/internal/secret"
)

// DefaultNamespace is where DataKit is deployed unless the user overrides it.
const DefaultNamespace = "datakit"

// Cluster is one imported kubeconfig context.
type Cluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Context   string `json:"context"`
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
	Source    string `json:"source"` // original kubeconfig path, informational
	AddedAt   string `json:"addedAt"`
}

// ContextInfo describes a context found in a kubeconfig file.
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Server    string `json:"server"`
	User      string `json:"user"`
	Namespace string `json:"namespace"`
	Current   bool   `json:"current"`
}

// Host is one Linux machine reached over SSH.
type Host struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	// HostKey is the server key seen at import time (authorized_keys format); later
	// connections must present the same key.
	HostKey string `json:"hostKey"`
	AddedAt string `json:"addedAt"`
}

// HostSecret is the SSH credential of a host; stored encrypted, never sent to the UI.
type HostSecret struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

// Store is a file-backed registry of clusters and hosts. Credentials (kubeconfigs, SSH
// keys) are encrypted at rest with the store's secret box.
type Store struct {
	dir string
	box *secret.Box
	mu  sync.Mutex
}

// New opens (and creates if needed) the store under the user config dir.
func New() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return NewAt(filepath.Join(base, "datakit-manager"))
}

// NewAt opens a store rooted at dir.
func NewAt(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "kubeconfigs"), filepath.Join(dir, "sshkeys"), filepath.Join(dir, "backups")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	box, err := secret.Open(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, box: box}
	return s, s.migratePlaintext()
}

// migratePlaintext encrypts kubeconfig copies written by versions that stored them as-is.
func (s *Store) migratePlaintext() error {
	old, _ := filepath.Glob(filepath.Join(s.dir, "kubeconfigs", "*.yaml"))
	for _, p := range old {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		id := strings.TrimSuffix(filepath.Base(p), ".yaml")
		if err := s.box.WriteFile(s.kubeconfigFile(id), b); err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

// Box is the store's encryption box.
func (s *Store) Box() *secret.Box { return s.box }

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) indexPath() string { return filepath.Join(s.dir, "clusters.json") }

func (s *Store) kubeconfigFile(id string) string {
	return filepath.Join(s.dir, "kubeconfigs", id+".enc")
}

// Kubeconfig returns the decrypted kubeconfig of a cluster.
func (s *Store) Kubeconfig(id string) ([]byte, error) {
	b, err := s.box.ReadFile(s.kubeconfigFile(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("集群 %s 的 kubeconfig 不存在，请重新导入", id)
	}
	return b, err
}

// BackupDir returns (and creates) the backup dir of a cluster.
func (s *Store) BackupDir(id string) (string, error) {
	d := filepath.Join(s.dir, "backups", id)
	return d, os.MkdirAll(d, 0o700)
}

func (s *Store) load() ([]Cluster, error) {
	b, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return []Cluster{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cs []Cluster
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, fmt.Errorf("clusters.json 损坏: %w", err)
	}
	return cs, nil
}

func (s *Store) save(cs []Cluster) error {
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath())
}

// List returns all clusters sorted by name.
func (s *Store) List() ([]Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	return cs, nil
}

// Get returns one cluster.
func (s *Store) Get(id string) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := s.load()
	if err != nil {
		return Cluster{}, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, nil
		}
	}
	return Cluster{}, fmt.Errorf("集群不存在: %s", id)
}

func contextsOf(cfg *clientcmdapi.Config) ([]ContextInfo, error) {
	out := make([]ContextInfo, 0, len(cfg.Contexts))
	for name, c := range cfg.Contexts {
		ci := ContextInfo{Name: name, Cluster: c.Cluster, User: c.AuthInfo, Namespace: c.Namespace, Current: name == cfg.CurrentContext}
		if cl, ok := cfg.Clusters[c.Cluster]; ok {
			ci.Server = cl.Server
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) == 0 {
		return nil, errors.New("kubeconfig 中没有任何 context")
	}
	return out, nil
}

// ParseKubeconfig lists the contexts of a kubeconfig file.
func ParseKubeconfig(path string) ([]ContextInfo, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}
	return contextsOf(cfg)
}

// ParseKubeconfigBytes lists the contexts of kubeconfig content.
func ParseKubeconfigBytes(content []byte) ([]ContextInfo, error) {
	cfg, err := clientcmd.Load(content)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}
	return contextsOf(cfg)
}

// Import copies the selected contexts of a kubeconfig file into the store. Certificate
// files referenced by relative path are resolved against the file's location.
func (s *Store) Import(path string, contexts []string, namespace string) ([]Cluster, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}
	return s.importConfig(cfg, path, contexts, namespace)
}

// ImportBytes is Import for kubeconfig content (web uploads).
func (s *Store) ImportBytes(content []byte, contexts []string, namespace string) ([]Cluster, error) {
	cfg, err := clientcmd.Load(content)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}
	return s.importConfig(cfg, "上传", contexts, namespace)
}

// importConfig stores each context as a self-contained (minified, cert-inlined), encrypted kubeconfig.
func (s *Store) importConfig(cfg *clientcmdapi.Config, source string, contexts []string, namespace string) ([]Cluster, error) {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if len(contexts) == 0 {
		for name := range cfg.Contexts {
			contexts = append(contexts, name)
		}
		sort.Strings(contexts)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := s.load()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, c := range cs {
		names[c.Name] = true
	}

	var added []Cluster
	for _, ctxName := range contexts {
		ctx, ok := cfg.Contexts[ctxName]
		if !ok {
			return nil, fmt.Errorf("context 不存在: %s", ctxName)
		}
		one := cfg.DeepCopy()
		one.CurrentContext = ctxName
		if err := clientcmdapi.MinifyConfig(one); err != nil {
			return nil, fmt.Errorf("context %s: %w", ctxName, err)
		}
		// Inline certificate files so the copy does not depend on the original location.
		if err := clientcmdapi.FlattenConfig(one); err != nil {
			return nil, fmt.Errorf("context %s 证书文件读取失败: %w", ctxName, err)
		}
		raw, err := clientcmd.Write(*one)
		if err != nil {
			return nil, err
		}
		id := newID()
		if err := s.box.WriteFile(s.kubeconfigFile(id), raw); err != nil {
			return nil, err
		}

		name := ctxName
		for i := 2; names[name]; i++ {
			name = fmt.Sprintf("%s (%d)", ctxName, i)
		}
		names[name] = true

		c := Cluster{ID: id, Name: name, Context: ctxName, Namespace: namespace, Source: source, AddedAt: time.Now().Format(time.RFC3339)}
		if cl, ok := cfg.Clusters[ctx.Cluster]; ok {
			c.Server = cl.Server
		}
		cs = append(cs, c)
		added = append(added, c)
	}
	return added, s.save(cs)
}

// Update changes the display name and/or namespace of a cluster.
func (s *Store) Update(id, name, namespace string) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := s.load()
	if err != nil {
		return Cluster{}, err
	}
	for i := range cs {
		if cs[i].ID != id {
			continue
		}
		if name != "" {
			cs[i].Name = name
		}
		if namespace != "" {
			cs[i].Namespace = namespace
		}
		return cs[i], s.save(cs)
	}
	return Cluster{}, fmt.Errorf("集群不存在: %s", id)
}

// Remove deletes a cluster and its kubeconfig copy (backups are kept).
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := s.load()
	if err != nil {
		return err
	}
	out := cs[:0]
	for _, c := range cs {
		if c.ID != id {
			out = append(out, c)
		}
	}
	_ = os.Remove(s.kubeconfigFile(id))
	return s.save(out)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- hosts

func (s *Store) hostsPath() string            { return filepath.Join(s.dir, "hosts.json") }
func (s *Store) hostKeyFile(id string) string { return filepath.Join(s.dir, "sshkeys", id+".enc") }

func (s *Store) loadHosts() ([]Host, error) {
	b, err := os.ReadFile(s.hostsPath())
	if errors.Is(err, os.ErrNotExist) {
		return []Host{}, nil
	}
	if err != nil {
		return nil, err
	}
	var hs []Host
	if err := json.Unmarshal(b, &hs); err != nil {
		return nil, fmt.Errorf("hosts.json 损坏: %w", err)
	}
	return hs, nil
}

func (s *Store) saveHosts(hs []Host) error {
	b, err := json.MarshalIndent(hs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.hostsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.hostsPath())
}

// ListHosts returns all hosts sorted by name.
func (s *Store) ListHosts() ([]Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, err := s.loadHosts()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].Name < hs[j].Name })
	return hs, nil
}

// GetHost returns one host with its decrypted credential.
func (s *Store) GetHost(id string) (Host, HostSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, err := s.loadHosts()
	if err != nil {
		return Host{}, HostSecret{}, err
	}
	for _, h := range hs {
		if h.ID != id {
			continue
		}
		var sec HostSecret
		b, err := s.box.ReadFile(s.hostKeyFile(id))
		if err != nil {
			return h, sec, fmt.Errorf("读取主机 %s 的 SSH 密钥失败: %w", h.Name, err)
		}
		return h, sec, json.Unmarshal(b, &sec)
	}
	return Host{}, HostSecret{}, fmt.Errorf("主机不存在: %s", id)
}

// AddHost stores a host and its credential (encrypted). The ID and AddedAt are filled in.
func (s *Store) AddHost(h Host, sec HostSecret) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, err := s.loadHosts()
	if err != nil {
		return Host{}, err
	}
	names := map[string]bool{}
	for _, x := range hs {
		names[x.Name] = true
	}
	base := h.Name
	for i := 2; names[h.Name]; i++ {
		h.Name = fmt.Sprintf("%s (%d)", base, i)
	}
	h.ID = newID()
	h.AddedAt = time.Now().Format(time.RFC3339)
	raw, err := json.Marshal(sec)
	if err != nil {
		return Host{}, err
	}
	if err := s.box.WriteFile(s.hostKeyFile(h.ID), raw); err != nil {
		return Host{}, err
	}
	return h, s.saveHosts(append(hs, h))
}

// UpdateHost renames a host.
func (s *Store) UpdateHost(id, name string) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, err := s.loadHosts()
	if err != nil {
		return Host{}, err
	}
	for i := range hs {
		if hs[i].ID == id {
			if name != "" {
				hs[i].Name = name
			}
			return hs[i], s.saveHosts(hs)
		}
	}
	return Host{}, fmt.Errorf("主机不存在: %s", id)
}

// RemoveHost deletes a host and its stored key.
func (s *Store) RemoveHost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, err := s.loadHosts()
	if err != nil {
		return err
	}
	out := hs[:0]
	for _, h := range hs {
		if h.ID != id {
			out = append(out, h)
		}
	}
	_ = os.Remove(s.hostKeyFile(id))
	return s.saveHosts(out)
}
