package config

import "github.com/leo1394/homebrew-conven/internal/model"

// completeSpringHTTPPolicy only fills absent HTTP routes. Certification still
// chooses one policy and checks repository registration evidence before publishing.
func completeSpringHTTPPolicy(manifest *model.Manifest, request RepositoryCertificationRequest) (RepositoryCertification, error) {
	candidate := *manifest
	candidate.Policies = make(map[string]model.Policy, len(manifest.Policies))
	for name, policy := range manifest.Policies {
		candidate.Policies[name] = policy
		runtimeName := policy.Drivers.Runtime
		if runtimeName == "" { runtimeName = policy.Drivers.Framework }
		if runtimeName != "spring-boot" || policy.Drivers.Discovery != request.Discovery || !springPolicyCompatible(policy) {
			continue
		}
		if _, exists := policy.Routing.Servers[RepositoryKindHTTP]; exists { continue }
		route, supported := discoveredSpringHTTPRoute(request.Discovery)
		if !supported { continue }
		servers := make(map[string]model.ServerRoute, len(policy.Routing.Servers)+1)
		for kind, server := range policy.Routing.Servers { servers[kind] = server }
		servers[RepositoryKindHTTP] = route
		policy.Routing.Servers = servers
		candidate.Policies[name] = policy
	}
	certification, _, err := CertifyRepository(&candidate, request)
	if err != nil { return RepositoryCertification{}, err }
	manifest.Policies[certification.Policy] = candidate.Policies[certification.Policy]
	return certification, nil
}

func discoveredSpringHTTPRoute(discovery string) (model.ServerRoute, bool) {
	registration := model.RegistrationGuard{Mode: "config", DisabledValue: false}
	switch discovery {
	case "consul":
		registration.Path = "service.registration.enabled"
	case "nacos":
		registration.Path = "spring.cloud.nacos.discovery.register-enabled"
	case "eureka":
		registration.Path = "eureka.client.register-with-eureka"
	case "passive", "kubernetes-dns":
		registration = model.RegistrationGuard{Mode: "not-applicable"}
	default:
		return model.ServerRoute{}, false
	}
	return model.ServerRoute{
		Port: RepositoryKindHTTP,
		Patches: []model.ConfigPatch{{Path: "server.port", Value: "${port.http}"}},
		Args: []string{"--server.address=127.0.0.1", "--server.port=${port.http}"},
		Env: map[string]string{},
		Isolation: model.ServerIsolation{
			Registration: registration,
			Listener: model.ListenerGuard{Path: "server.address", Value: "127.0.0.1"},
		},
	}, true
}
