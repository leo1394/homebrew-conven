package model

type Manifest struct {
	Version      int                             `yaml:"version"`
	Workspace    Workspace                       `yaml:"workspace"`
	Environments map[string]Environment          `yaml:"environments"`
	Policies     map[string]Policy               `yaml:"policies"`
	Services     map[string]Service              `yaml:"services"`
}

type Workspace struct {
	Name             string   `yaml:"name"`
	Policy           string   `yaml:"policy"`
	DisabledBindings []string `yaml:"disabledBindings"`
}

type Policy struct {
	Drivers PolicyDrivers `yaml:"drivers"`
	Config  PolicyConfig  `yaml:"config"`
	Process PolicyProcess `yaml:"process"`
	Routing PolicyRouting `yaml:"routing"`
}

type PolicyDrivers struct {
	Runtime      string `yaml:"runtime"`
	Framework    string `yaml:"framework"`
	ConfigSource string `yaml:"configSource"`
	Discovery    string `yaml:"discovery"`
	Materializer string `yaml:"materializer"`
}

type PolicyConfig struct {
	SourceDir        string        `yaml:"sourceDir"`
	Application      string        `yaml:"application"`
	Bootstrap        string        `yaml:"bootstrap"`
	RuntimeBootstrap string        `yaml:"runtimeBootstrap"`
	Apollo           ApolloSource  `yaml:"apollo"`
	Patches          []ConfigPatch `yaml:"patches"`
}

type ApolloSource struct {
	Attempts   int    `yaml:"attempts"`
	RetryDelay string `yaml:"retryDelay"`
	Timeout    string `yaml:"timeout"`
}

type PolicyProcess struct {
	Env  map[string]string `yaml:"env"`
	Args []string          `yaml:"args"`
}

type PolicyRouting struct {
	Servers          map[string]ServerRoute `yaml:"servers"`
	LocalDependency  RouteRule              `yaml:"localDependency"`
	RemoteDependency RouteRule              `yaml:"remoteDependency"`
}

type ServerRoute struct {
	Port      string          `yaml:"port"`
	Patches   []ConfigPatch   `yaml:"patches"`
	Args      []string        `yaml:"args"`
	Env       map[string]string `yaml:"env"`
	Isolation ServerIsolation `yaml:"isolation"`
}

type ServerIsolation struct {
	Registration RegistrationGuard `yaml:"registration"`
	Listener     ListenerGuard     `yaml:"listener"`
}

type RegistrationGuard struct {
	Mode          string      `yaml:"mode"`
	File          string      `yaml:"file"`
	Path          string      `yaml:"path"`
	DisabledValue interface{} `yaml:"disabledValue"`
}

type ListenerGuard struct {
	File  string      `yaml:"file"`
	Path  string      `yaml:"path"`
	Value interface{} `yaml:"value"`
}

type RouteRule struct {
	Mode  string      `yaml:"mode"`
	Value interface{} `yaml:"value"`
}

type ConfigPatch struct {
	File  string      `yaml:"file"`
	Path  string      `yaml:"path"`
	Mode  string      `yaml:"mode"`
	Value interface{} `yaml:"value"`
}

type Environment struct {
	Registry    string                                         `yaml:"registry"`
	Registries  map[string]Registry                            `yaml:"registries"`
	EnvFile     string                                         `yaml:"envFile"`
	Env         map[string]string                              `yaml:"env"`
	Connection  Connection                                     `yaml:"connection"`
	Endpoints   map[string]EnvironmentEndpoint                 `yaml:"endpoints"`
	Resolutions map[string]map[string]DependencyResolution     `yaml:"resolutions"`
}

type Registry struct {
	Driver      string      `yaml:"driver"`
	Address     string      `yaml:"address"`
	Namespace   string      `yaml:"namespace"`
	Group       string      `yaml:"group"`
	Datacenter  string      `yaml:"datacenter"`
	Prefix      string      `yaml:"prefix"`
	TokenEnv    string      `yaml:"tokenEnv"`
	UsernameEnv string      `yaml:"usernameEnv"`
	PasswordEnv string      `yaml:"passwordEnv"`
	ObserveFor  string      `yaml:"observeFor"`
	TLS         RegistryTLS `yaml:"tls"`
}

type RegistryTLS struct {
	CAFile             string `yaml:"caFile"`
	CertFile           string `yaml:"certFile"`
	KeyFile            string `yaml:"keyFile"`
	ServerName         string `yaml:"serverName"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

type EnvironmentEndpoint struct {
	Protocol  string `yaml:"protocol"`
	Address   string `yaml:"address"`
	Readiness Health `yaml:"readiness"`
}

type DependencyResolution struct {
	Mode   string            `yaml:"mode"`
	Target string            `yaml:"target"`
	Env    map[string]string `yaml:"env"`
}

type Connection struct {
	Driver        string     `yaml:"driver"`
	Command       string     `yaml:"command"`
	Args          []string   `yaml:"args"`
	Kubeconfig    string     `yaml:"kubeconfig"`
	KubeconfigEnv string     `yaml:"kubeconfigEnv"`
	Context       string     `yaml:"context"`
	Namespace     string     `yaml:"namespace"`
	Sudo          bool       `yaml:"sudo"`
	Timeout       string     `yaml:"timeout"`
	Readiness     []Endpoint `yaml:"readiness"`
}

type Endpoint struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
}

type Service struct {
	Path         string                `yaml:"path"`
	Policy       string                `yaml:"policy"`
	Kind         string                `yaml:"kind"`
	Kinds        []string              `yaml:"kinds"`
	Network      ServiceNetwork        `yaml:"network"`
	Isolation    ServiceIsolation      `yaml:"isolation"`
	Discovery    ServiceDiscovery      `yaml:"discovery"`
	Runner       Runner                `yaml:"runner"`
	Ports        map[string]int        `yaml:"ports"`
	Env          map[string]string     `yaml:"env"`
	LocalEnv     map[string]string     `yaml:"localEnv"`
	Config       ServiceConfig         `yaml:"config"`
	Health       Health                `yaml:"health"`
	HealthChecks []ServiceHealthCheck  `yaml:"healthChecks"`
	Dependencies map[string]Dependency `yaml:"dependencies"`
}

type ServiceIsolation struct {
	Consumers map[string]ConsumerIsolation `yaml:"consumers"`
}

type ConsumerIsolation struct {
	Mode string `yaml:"mode"`
	Env  string `yaml:"env"`
}

const (
	NetworkListenLoopback      = "loopback"
	NetworkListenAllInterfaces = "all-interfaces"
)

type ServiceNetwork struct {
	Listen string `yaml:"listen"`
}

func (network ServiceNetwork) EffectiveListen() string {
	if network.Listen == "" {
		return NetworkListenLoopback
	}
	return network.Listen
}

type ServiceDiscovery struct {
	Analyzer         string   `yaml:"analyzer"`
	Certifier        string   `yaml:"certifier"`
	Registry         string   `yaml:"registry"`
	Identity         string   `yaml:"identity"`
	ProviderAliases  []string `yaml:"providerAliases"`
	ConsumerBindings []string `yaml:"consumerBindings"`
	Consumers        []string `yaml:"consumers"`
	Bindings         []string `yaml:"bindings"`
}

type ServiceConfig struct {
	Patches []ConfigPatch `yaml:"patches"`
}

type Runner struct {
	Workdir    string   `yaml:"workdir"`
	RunWorkdir string   `yaml:"runWorkdir"`
	Artifact   string   `yaml:"artifact"`
	Prepare    []string `yaml:"prepare"`
	Build      []string `yaml:"build"`
	Run        []string `yaml:"run"`
}

type Health struct {
	Type    string   `yaml:"type"`
	Address string   `yaml:"address"`
	URL     string   `yaml:"url"`
	Command []string `yaml:"command"`
	Timeout string   `yaml:"timeout"`
}

type ServiceHealthCheck struct {
	Server  string   `yaml:"server"`
	Type    string   `yaml:"type"`
	Address string   `yaml:"address"`
	URL     string   `yaml:"url"`
	Command []string `yaml:"command"`
	Timeout string   `yaml:"timeout"`
}

func (service Service) EffectiveKinds() []string {
	if len(service.Kinds) > 0 {
		return append([]string(nil), service.Kinds...)
	}
	if service.Kind != "" {
		return []string{service.Kind}
	}
	return nil
}

func (service Service) EffectiveHealthChecks() []ServiceHealthCheck {
	if len(service.HealthChecks) > 0 {
		result := make([]ServiceHealthCheck, len(service.HealthChecks))
		copy(result, service.HealthChecks)
		return result
	}
	if service.Health.Type == "" {
		return nil
	}
	server := service.Kind
	return []ServiceHealthCheck{{
		Server:  server,
		Type:    service.Health.Type,
		Address: service.Health.Address,
		URL:     service.Health.URL,
		Command: append([]string(nil), service.Health.Command...),
		Timeout: service.Health.Timeout,
	}}
}

func (discovery ServiceDiscovery) EffectiveConsumerBindings() []string {
	if len(discovery.ConsumerBindings) > 0 {
		return append([]string(nil), discovery.ConsumerBindings...)
	}
	return append([]string(nil), discovery.Bindings...)
}

func ProviderService(manifest *Manifest, alias string, binding string) string {
	if manifest == nil {
		return ""
	}
	for _, reference := range []string{alias, binding} {
		if reference == "" {
			continue
		}
		if _, found := manifest.Services[reference]; found {
			return reference
		}
		for name, service := range manifest.Services {
			for _, providerAlias := range service.Discovery.ProviderAliases {
				if providerAlias == reference {
					return name
				}
			}
		}
	}
	return ""
}

type Dependency struct {
	LocalService string            `yaml:"localService"`
	Binding      string            `yaml:"binding"`
	Port         string            `yaml:"port"`
	Env          map[string]string `yaml:"env"`
	LocalEnv     map[string]string `yaml:"localEnv"`
	RemoteEnv    map[string]string `yaml:"remoteEnv"`
	Required     *bool             `yaml:"required"`
}
