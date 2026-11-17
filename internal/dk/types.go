// Package dk inspects and changes a DataKit deployment inside a Kubernetes cluster.
package dk

// ConfDir is where DataKit loads collector configs from inside the container.
const ConfDir = "/usr/local/datakit/conf.d"

// DefaultConfigMap is the ConfigMap (and volume) name used by the official datakit.yaml.
const DefaultConfigMap = "datakit-conf"

// RestartAnnotation forces a rolling restart when its value changes.
const RestartAnnotation = "datakit-manager/restartedAt"

// Inspection is everything the UI needs to know about DataKit in one cluster.
type Inspection struct {
	Namespace     string      `json:"namespace"`
	Found         bool        `json:"found"`
	Message       string      `json:"message"`
	ServerVersion string      `json:"serverVersion"`
	Workload      Workload    `json:"workload"`
	Pods          []Pod       `json:"pods"`
	Persistence   Persistence `json:"persistence"`
	// LiveSynced: ConfFiles was reconciled with the files actually present in running pods.
	LiveSynced    bool       `json:"liveSynced"`
	LiveError     string     `json:"liveError"`
	LivePods      int        `json:"livePods"`
	ConfFiles     []ConfFile `json:"confFiles"`
	Env           []EnvVar   `json:"env"`
	EnabledInputs []string   `json:"enabledInputs"`
}

// Workload describes the DataKit DaemonSet (or Deployment).
type Workload struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Container   string `json:"container"`
	Image       string `json:"image"`
	Version     string `json:"version"`
	Desired     int32  `json:"desired"`
	Updated     int32  `json:"updated"`
	Ready       int32  `json:"ready"`
	Available   int32  `json:"available"`
	RolloutDone bool   `json:"rolloutDone"`
	HostNetwork bool   `json:"hostNetwork"`
	HTTPPort    int    `json:"httpPort"`
	HTTPLocal   bool   `json:"httpLocal"` // listens on loopback only
	RestartedAt string `json:"restartedAt"`
}

// Pod is one DataKit pod.
type Pod struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	IP       string `json:"ip"`
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	Reason   string `json:"reason"`
	Image    string `json:"image"`
	StartAt  string `json:"startAt"`
}

// Persistence says how (and whether) collector configs survive a pod restart.
type Persistence struct {
	// Mode decides where NEW conf files are written:
	//   "volume"    conf.d (or a directory under it) is a writable hostPath / PVC: files are
	//               written straight into that directory inside the pods
	//   "configmap" existing ConfigMap mounts: ConfigMap key + subPath mount
	//   "none"      nothing yet: a ConfigMap is created on first save
	Mode            string      `json:"mode"`
	VolumeDir       string      `json:"volumeDir"`     // target directory in "volume" mode
	VolumeDesc      string      `json:"volumeDesc"`    // e.g. "hostPath /data/datakit/conf.d"
	VolumePerNode   bool        `json:"volumePerNode"` // hostPath: one copy per node
	ConfigMap       string      `json:"configMap"`
	ConfigMapExists bool        `json:"configMapExists"`
	VolumeName      string      `json:"volumeName"`
	VolumeDeclared  bool        `json:"volumeDeclared"`
	Mounts          []ConfMount `json:"mounts"`
	DirVolumes      []string    `json:"dirVolumes"`
	Summary         string      `json:"summary"`
}

// ConfMount is one volumeMount that lands under conf.d.
type ConfMount struct {
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath"`
	Volume    string `json:"volume"`
	ConfigMap string `json:"configMap"`
	Kind      string `json:"kind"` // configMap / hostPath / pvc / emptyDir / secret / other
}

// ConfFile is one collector config file: a ConfigMap key, a file in the persisted conf
// directory, or a file that only exists inside the running container.
type ConfFile struct {
	// Source: "configmap" | "volume" (persisted directory) | "container" (not persisted,
	// typically generated at start-up for the default-enabled collectors)
	Source   string `json:"source"`
	Path     string `json:"path"`     // absolute path inside the container ("" = not mounted)
	Editable bool   `json:"editable"` // container-only files are read-only
	Live     bool   `json:"live"`     // present in the running pod(s)
	// Drift: "" in sync; "pending" = saved content differs from what pods run (restart
	// pending); "inconsistent" = pods disagree with each other
	Drift string `json:"drift"`
	Pods  int    `json:"pods"` // number of inspected pods that have the file

	ConfigMap string   `json:"configMap"`
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Mounted   bool     `json:"mounted"`
	MountPath string   `json:"mountPath"`
	Inputs    []string `json:"inputs"`
	Error     string   `json:"error"`
}

// EnvVar is one env entry of the DataKit container.
type EnvVar struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	FromRef  string `json:"fromRef"`
	Editable bool   `json:"editable"`
}

// ConfChange creates or replaces one conf file. With Path set the file is written into the
// persisted conf directory of every running pod; otherwise it is a ConfigMap key.
type ConfChange struct {
	Path      string `json:"path"`
	ConfigMap string `json:"configMap"` // empty = managed ConfigMap
	Key       string `json:"key"`
	Content   string `json:"content"`
}

// ConfRef points at one conf file (Path for a file in the persisted directory).
type ConfRef struct {
	Path      string `json:"path"`
	ConfigMap string `json:"configMap"`
	Key       string `json:"key"`
}

// EnvChange sets one env var to a literal value.
type EnvChange struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ChangeSet is one user edit. Applying it always ends with a rolling restart.
type ChangeSet struct {
	ConfUpserts []ConfChange `json:"confUpserts"`
	ConfDeletes []ConfRef    `json:"confDeletes"`
	EnvSet      []EnvChange  `json:"envSet"`
	EnvDelete   []string     `json:"envDelete"`
}

// PlanView is the human readable preview of a ChangeSet.
type PlanView struct {
	Actions  []string `json:"actions"`
	Warnings []string `json:"warnings"`
}

// ApplyResult reports what was done.
type ApplyResult struct {
	Actions    []string `json:"actions"`
	Warnings   []string `json:"warnings"`
	BackupFile string   `json:"backupFile"`
}

// RolloutStatus is the progress of the rolling restart.
type RolloutStatus struct {
	Desired   int32  `json:"desired"`
	Updated   int32  `json:"updated"`
	Ready     int32  `json:"ready"`
	Available int32  `json:"available"`
	Done      bool   `json:"done"`
	Message   string `json:"message"`
	Pods      []Pod  `json:"pods"`
}
