package model

type Manifest struct {
	Version      int                    `yaml:"version"`
	Workspace    Workspace              `yaml:"workspace"`
	Environments map[string]Environment `yaml:"environments"`
	Policies     map[string]Policy      `yaml:"policies"`
	Services     map[string]Service     `yaml:"services"`
}

type Workspace struct {
	Name   string `yaml:"name"`
	Policy string `yaml:"policy"`
}

type Policy struct {
	Drivers PolicyDrivers `yaml:"drivers"`
	Config  PolicyConfig  `yaml:"config"`
	Process PolicyProcess `yaml:"process"`
	Routing PolicyRouting `yaml:"routing"`
}

type PolicyDrivers struct {
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
	Port    string        `yaml:"port"`
	Patches []ConfigPatch `yaml:"patches"`
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
	Registry   string            `yaml:"registry"`
	Env        map[string]string `yaml:"env"`
	Connection Connection        `yaml:"connection"`
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
	Discovery    ServiceDiscovery      `yaml:"discovery"`
	Runner       Runner                `yaml:"runner"`
	Ports        map[string]int        `yaml:"ports"`
	Env          map[string]string     `yaml:"env"`
	LocalEnv     map[string]string     `yaml:"localEnv"`
	Config       ServiceConfig         `yaml:"config"`
	Health       Health                `yaml:"health"`
	Dependencies map[string]Dependency `yaml:"dependencies"`
}

type ServiceDiscovery struct {
	Analyzer string   `yaml:"analyzer"`
	Bindings []string `yaml:"bindings"`
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

type Dependency struct {
	Binding   string            `yaml:"binding"`
	Port      string            `yaml:"port"`
	LocalEnv  map[string]string `yaml:"localEnv"`
	RemoteEnv map[string]string `yaml:"remoteEnv"`
}
