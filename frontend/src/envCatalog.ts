export type EnvType = 'string' | 'bool' | 'json' | 'select' | 'inputs' | 'number'

export interface EnvItem {
  name: string
  label: string
  type: EnvType
  help?: string
  placeholder?: string
  options?: string[]
}

export interface EnvGroup { key: string; title: string; desc?: string; logo?: string; items: EnvItem[] }

const dd = (suffix: string, label: string, type: EnvType, extra: Partial<EnvItem> = {}): EnvItem => ({ name: 'ENV_INPUT_DDTRACE_' + suffix, label, type, ...extra })
const ct = (suffix: string, label: string, type: EnvType, extra: Partial<EnvItem> = {}): EnvItem => ({ name: 'ENV_INPUT_CONTAINER_' + suffix, label, type, ...extra })
const ot = (suffix: string, label: string, type: EnvType, extra: Partial<EnvItem> = {}): EnvItem => ({ name: 'ENV_INPUT_OTEL_' + suffix, label, type, ...extra })

export const envGroups: EnvGroup[] = [
  {
    key: 'base', title: 'DataKit 基础', logo: 'dk',
    items: [
      { name: 'ENV_DATAWAY', label: 'DataWay 地址', type: 'string', placeholder: 'https://openway.guance.com?token=tkn_xxx', help: '数据上报地址，包含工作空间 token。' },
      { name: 'ENV_CLUSTER_NAME_K8S', label: '集群名称', type: 'string', help: '作为 cluster_name_k8s 标签，多集群时务必各不相同。' },
      { name: 'ENV_ENABLE_ELECTION', label: '开启选举', type: 'select', options: ['enable'], help: '任意非空值即开启。MySQL、Redis 等远程采集依赖选举避免重复采集。' },
      { name: 'ENV_NAMESPACE', label: '选举命名空间', type: 'string', help: '同一工作空间内，不同集群应使用不同的选举命名空间。' },
      { name: 'ENV_GLOBAL_HOST_TAGS', label: '全局主机标签', type: 'string', placeholder: 'host=__datakit_hostname,host_ip=__datakit_ip', help: 'key=value，逗号分隔。附加到非选举采集的数据上。' },
      { name: 'ENV_GLOBAL_ELECTION_TAGS', label: '全局选举标签', type: 'string', placeholder: 'cluster=prod', help: 'key=value，逗号分隔。附加到选举采集的数据上。' },
      { name: 'ENV_HTTP_LISTEN', label: 'HTTP 监听地址', type: 'string', placeholder: '0.0.0.0:9529', help: '需要监听 0.0.0.0，其它 Pod（APM、RUM）和本工具的状态页才能访问。' },
      { name: 'ENV_HTTP_ALLOWED_CORS_ORIGINS', label: '允许的跨域来源', type: 'string', placeholder: 'https://app.example.com,https://m.example.com', help: 'RUM 跨域上报时填写。只认精确的 origin，不支持 * 通配，多个用逗号分隔。' },
      { name: 'ENV_LOG_LEVEL', label: '日志级别', type: 'select', options: ['info', 'debug'] },
      { name: 'ENV_REQUEST_RATE_LIMIT', label: 'API 限流（次/秒）', type: 'number' },
      { name: 'ENV_IPDB', label: 'IP 库', type: 'select', options: ['iploc'], help: '需要同时挂载 IP 库数据（initContainer）。' },
      { name: 'ENV_ULIMIT', label: '最大打开文件数', type: 'number' },
    ],
  },
  {
    key: 'inputs', title: '默认开启的采集器', logo: 'container',
    desc: '这些采集器无需配置文件即可运行（主机、容器、DDTrace 等）。需要连接信息的采集器（MySQL、Redis…）请到“采集器配置”里添加。',
    items: [{ name: 'ENV_DEFAULT_ENABLED_INPUTS', label: '采集器列表', type: 'inputs' }],
  },
  {
    key: 'ddtrace', title: 'DDTrace', logo: 'ddtrace',
    desc: '需要先把 ddtrace 加入“默认开启的采集器”。以下变量留空即使用 DataKit 默认值。',
    items: [
      dd('ENDPOINTS', '接收路径', 'json', { placeholder: '["/v0.3/traces", "/v0.4/traces", "/v0.5/traces"]' }),
      dd('CUSTOMER_TAGS', '自定义标签白名单', 'json', { placeholder: '["sink_project", "custom_dd_tag"]', help: '把 span 里的这些 tag 提取为一级字段。' }),
      dd('COMPATIBLE_OTEL', '兼容 OTEL Trace', 'bool', { help: '将 otel Trace 与 DDTrace Trace 兼容（默认 false）。' }),
      dd('TRACE_128_BIT_ID', '128 位 Trace ID', 'bool', { help: '将链路 ID 转成 32 位 16 进制字符串（默认 true）。' }),
      dd('TRACE_ID_64_BIT_HEX', '64 位 16 进制 Trace ID', 'bool', { help: '将 B3/B3Multi TraceID 与 DDTrace 兼容（默认 false）。' }),
      dd('KEEP_RARE_RESOURCE', '保留稀有资源', 'bool'),
      dd('DEL_MESSAGE', '删除 message 字段', 'bool', { help: '减少存储量。' }),
      dd('OMIT_ERR_STATUS', '忽略的错误状态码', 'json', { placeholder: '["404", "403"]' }),
      dd('SAMPLER', '采样率', 'number', { placeholder: '0.3', help: '0 ~ 1。' }),
      dd('CLOSE_RESOURCE', '关闭的资源', 'json', { placeholder: '{"service1":["resource1"], "*":["/health"]}' }),
      dd('TRACING_METRIC_ENABLE', '生成链路指标', 'bool'),
      dd('MAX_SPANS', '单条链路最大 span 数', 'number', { help: '默认 100000，-1 关闭限制。' }),
      dd('MAX_BODY_MB', '单次请求最大 body（MiB）', 'number', { help: '-1 关闭限制。' }),
      dd('THREADS', '线程与缓冲', 'json', { placeholder: '{"buffer":1000, "threads":100}' }),
      dd('STORAGE', '本地缓存', 'json', { placeholder: '{"storage":"./ddtrace_storage", "capacity": 5120}' }),
      dd('TAGS', '附加标签', 'json', { placeholder: '{"k1":"v1", "k2":"v2"}' }),
    ],
  },
  {
    key: 'container', title: '容器 / Kubernetes', logo: 'container',
    items: [
      ct('ENABLE_CONTAINER_METRIC', '采集容器指标', 'bool'),
      ct('ENABLE_POD_METRIC', '采集 Pod 指标', 'bool'),
      ct('ENABLE_K8S_METRIC', '采集 K8s 资源指标', 'bool'),
      ct('ENABLE_K8S_EVENT', '采集 K8s 事件', 'bool'),
      ct('ENABLE_K8S_NODE_LOCAL', '按节点本地采集', 'bool', { help: '每个 DataKit 只采集自己所在节点的资源，分摊选举节点的压力。' }),
      ct('CONTAINER_INCLUDE_LOG', '容器日志白名单', 'string', { placeholder: 'namespace:kube-system', help: '支持 image: / namespace: 前缀，逗号分隔。' }),
      ct('CONTAINER_EXCLUDE_LOG', '容器日志黑名单', 'string', { placeholder: 'image:*' }),
      ct('LOGGING_ENABLE_MULTILINE', '日志多行合并', 'bool'),
      ct('LOGGING_EXTRA_SOURCE_MAP', '日志 source 映射', 'string', { placeholder: 'source_regex*=new_source,regex*=new_source2' }),
      ct('LOGGING_SOURCE_MULTILINE_MAP_JSON', 'source 多行规则', 'json', { placeholder: '{"source_nginx":"^\\\\d{4}"}' }),
      ct('LOGGING_SEARCH_INTERVAL', '日志发现间隔', 'string', { placeholder: '60s' }),
      ct('LOGGING_FILE_FROM_BEGINNING', '从文件开头采集日志', 'bool'),
      ct('EXTRACT_K8S_LABEL_AS_TAGS_V2', 'Label 转对象/日志标签', 'json', { placeholder: '["app", "version"]' }),
      ct('EXTRACT_K8S_LABEL_AS_TAGS_V2_FOR_METRIC', 'Label 转指标标签', 'json', { placeholder: '["app"]' }),
      ct('TAGS', '附加标签', 'string', { placeholder: 'k1=v1,k2=v2' }),
    ],
  },
  {
    key: 'otel', title: 'OpenTelemetry', logo: 'opentelemetry',
    desc: '需要先把 opentelemetry 加入“默认开启的采集器”。',
    items: [
      ot('HTTP', 'HTTP 接收配置', 'json', { placeholder: '{"enable":true, "http_status_ok": 200, "trace_api": "/otel/v1/traces", "metric_api": "/otel/v1/metrics"}' }),
      ot('GRPC', 'gRPC 接收配置', 'json', { placeholder: '{"addr": "0.0.0.0:4317"}' }),
      ot('CUSTOMER_TAGS', '自定义标签白名单', 'json', { placeholder: '["sink_project", "custom.otel.tag"]' }),
      ot('COMPATIBLE_DDTRACE', '兼容 DDTrace Trace ID', 'bool'),
      ot('SPLIT_SERVICE_NAME', '拆分服务名', 'bool'),
      ot('KEEP_RARE_RESOURCE', '保留稀有资源', 'bool'),
      ot('DEL_MESSAGE', '删除 message 字段', 'bool'),
      ot('SAMPLER', '采样率', 'number', { placeholder: '0.3' }),
    ],
  },
  {
    key: 'rum', title: 'RUM', logo: 'rum',
    desc: '需要先把 rum 加入“默认开启的采集器”。',
    items: [
      { name: 'ENV_RUM_ORIGIN_IP_HEADER', label: '真实 IP 请求头', type: 'string', placeholder: 'X-Forwarded-For' },
      { name: 'ENV_RUM_APP_ID_WHITE_LIST', label: 'App ID 白名单', type: 'string', placeholder: 'appid-1,appid-2' },
    ],
  },
]

export const knownEnv = new Set(envGroups.flatMap((g) => g.items.map((i) => i.name)))

/** Collectors that can run with no config file, offered in ENV_DEFAULT_ENABLED_INPUTS. */
export const defaultableInputs = [
  'cpu', 'disk', 'diskio', 'mem', 'swap', 'system', 'net', 'hostobject', 'host_processes', 'container', 'kubernetesprometheus', 'dk',
  'statsd', 'ddtrace', 'opentelemetry', 'rum', 'profile', 'pyroscope', 'skywalking', 'jaeger', 'zipkin', 'pinpoint', 'ebpf',
  'logfwdserver', 'logstreaming', 'prom_remote_write', 'netstat', 'hostdir', 'dialtesting',
]
