package runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type RegistryInstance struct {
	ID      string
	Address string
	Port    int
}

type RegistrySnapshot struct {
	Driver    string
	Registry  string
	Identity  string
	Instances map[string]RegistryInstance
}

type RegistrationObserverAdapter interface {
	Driver() string
	Snapshot(context.Context, *http.Client, string, model.Registry, string, map[string]string) (map[string]RegistryInstance, error)
}

var registryObserverRegistry = struct {
	sync.RWMutex
	adapters map[string]RegistrationObserverAdapter
}{adapters: make(map[string]RegistrationObserverAdapter)}

func RegisterRegistrationObserverAdapter(adapter RegistrationObserverAdapter) {
	if adapter == nil || adapter.Driver() == "" {
		panic("registration observer adapter must have a driver")
	}
	registryObserverRegistry.Lock()
	defer registryObserverRegistry.Unlock()
	if _, found := registryObserverRegistry.adapters[adapter.Driver()]; found {
		panic("duplicate registration observer adapter " + adapter.Driver())
	}
	registryObserverRegistry.adapters[adapter.Driver()] = adapter
}

func init() {
	RegisterRegistrationObserverAdapter(consulRegistryObserver{})
	RegisterRegistrationObserverAdapter(nacosRegistryObserver{})
	RegisterRegistrationObserverAdapter(eurekaRegistryObserver{})
	RegisterRegistrationObserverAdapter(etcdRegistryObserver{})
}

func builtinRegistryObserverAdapters() []RegistrationObserverAdapter {
	registryObserverRegistry.RLock()
	defer registryObserverRegistry.RUnlock()
	drivers := make([]string, 0, len(registryObserverRegistry.adapters))
	for driver := range registryObserverRegistry.adapters {
		drivers = append(drivers, driver)
	}
	sort.Strings(drivers)
	adapters := make([]RegistrationObserverAdapter, 0, len(drivers))
	for _, driver := range drivers {
		adapters = append(adapters, registryObserverRegistry.adapters[driver])
	}
	return adapters
}

func snapshotServiceRegistry(ctx context.Context, workspace string, service PlannedService) (*RegistrySnapshot, error) {
	if service.Registry == nil || service.RegistryRef == "" || service.RegistryIdentity == "" {
		return nil, nil
	}
	registry := *service.Registry
	adapter, err := registryObserverFor(registry.Driver)
	if err != nil {
		return nil, fmt.Errorf("service %s registry observation: %w", service.Name, err)
	}
	values, err := registryCredentials(registry)
	if err != nil {
		return nil, fmt.Errorf("service %s registry observation: %w", service.Name, err)
	}
	client, err := registryHTTPClient(workspace, registry)
	if err != nil {
		return nil, fmt.Errorf("service %s registry observation: %w", service.Name, err)
	}
	instances, err := adapter.Snapshot(ctx, client, registryBaseAddress(registry.Address), registry, service.RegistryIdentity, values)
	if err != nil {
		return nil, fmt.Errorf("service %s registry %s identity %q: %w", service.Name, registry.Driver, service.RegistryIdentity, err)
	}
	return &RegistrySnapshot{Driver: registry.Driver, Registry: service.RegistryRef, Identity: service.RegistryIdentity, Instances: instances}, nil
}

func verifyServiceRegistry(ctx context.Context, workspace string, service PlannedService, baseline *RegistrySnapshot) (*RegistrationEvidence, error) {
	if baseline == nil {
		return nil, nil
	}
	duration := 5 * time.Second
	if service.Registry.ObserveFor != "" {
		parsed, err := time.ParseDuration(service.Registry.ObserveFor)
		if err != nil {
			return nil, err
		}
		duration = parsed
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			current, err := snapshotServiceRegistry(ctx, workspace, service)
			if err != nil {
				return nil, err
			}
			if added := addedRegistryInstances(baseline.Instances, current.Instances); len(added) > 0 {
				return nil, fmt.Errorf("service %s appeared in %s registry as new instance(s): %s", service.Name, baseline.Driver, strings.Join(added, ", "))
			}
		case <-deadline.C:
			return &RegistrationEvidence{Registry: baseline.Registry, Driver: baseline.Driver, Identity: baseline.Identity, Status: "absent", VerifiedAt: time.Now()}, nil
		}
	}
}

func rejectLocalRegistryEntries(service PlannedService, snapshot *RegistrySnapshot) error {
	if snapshot == nil {
		return nil
	}
	ports := make(map[int]bool, len(service.Ports))
	for _, port := range service.Ports {
		ports[port] = true
	}
	local := localAddresses()
	for _, instance := range snapshot.Instances {
		address := strings.Trim(instance.Address, "[]")
		if ports[instance.Port] && (local[address] || address == "localhost") {
			return fmt.Errorf("service %s has a possible stale local %s registration %s at %s:%d; inspect the registry before retrying", service.Name, snapshot.Driver, instance.ID, instance.Address, instance.Port)
		}
	}
	return nil
}

func addedRegistryInstances(before map[string]RegistryInstance, after map[string]RegistryInstance) []string {
	result := make([]string, 0)
	for id, instance := range after {
		if _, found := before[id]; found {
			continue
		}
		result = append(result, fmt.Sprintf("%s(%s:%d)", id, instance.Address, instance.Port))
	}
	sort.Strings(result)
	return result
}

func registryObserverFor(driver string) (RegistrationObserverAdapter, error) {
	var found RegistrationObserverAdapter
	for _, adapter := range builtinRegistryObserverAdapters() {
		if adapter.Driver() != driver {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple registry observer adapters are registered for %q", driver)
		}
		found = adapter
	}
	if found == nil {
		return nil, fmt.Errorf("no registry observer adapter is registered for %q", driver)
	}
	return found, nil
}

func registryCredentials(registry model.Registry) (map[string]string, error) {
	result := make(map[string]string)
	for key, name := range map[string]string{"token": registry.TokenEnv, "username": registry.UsernameEnv, "password": registry.PasswordEnv} {
		if name == "" {
			continue
		}
		value, found := os.LookupEnv(name)
		if !found || value == "" {
			return nil, fmt.Errorf("registry credential environment variable %s is required", name)
		}
		result[key] = value
	}
	return result, nil
}

func registryHTTPClient(workspace string, registry model.Registry) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: registry.TLS.ServerName, InsecureSkipVerify: registry.TLS.InsecureSkipVerify}
	if registry.TLS.CAFile != "" {
		data, err := os.ReadFile(filepath.Join(workspace, registry.TLS.CAFile))
		if err != nil {
			return nil, fmt.Errorf("read registry CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("registry CA file contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	if registry.TLS.CertFile != "" || registry.TLS.KeyFile != "" {
		if registry.TLS.CertFile == "" || registry.TLS.KeyFile == "" {
			return nil, errors.New("registry TLS certFile and keyFile must be declared together")
		}
		certificate, err := tls.LoadX509KeyPair(filepath.Join(workspace, registry.TLS.CertFile), filepath.Join(workspace, registry.TLS.KeyFile))
		if err != nil {
			return nil, fmt.Errorf("load registry client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}, nil
}

func registryBaseAddress(address string) string {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if !strings.Contains(address, "://") {
		return "http://" + address
	}
	return address
}

func registryGET(ctx context.Context, client *http.Client, target string, headers map[string]string, result interface{}) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			return fmt.Errorf("registry request failed: %v", transport.Err)
		}
		return errors.New("registry request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode registry response: %w", err)
	}
	return nil
}

type consulRegistryObserver struct{}

func (consulRegistryObserver) Driver() string { return "consul" }

func (consulRegistryObserver) Snapshot(ctx context.Context, client *http.Client, base string, registry model.Registry, identity string, credentials map[string]string) (map[string]RegistryInstance, error) {
	query := url.Values{}
	if registry.Datacenter != "" {
		query.Set("dc", registry.Datacenter)
	}
	target := base + "/v1/health/service/" + url.PathEscape(identity)
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	headers := make(map[string]string)
	if credentials["token"] != "" {
		headers["X-Consul-Token"] = credentials["token"]
	}
	var payload []struct {
		Node struct { Address string `json:"Address"` } `json:"Node"`
		Service struct {
			ID      string `json:"ID"`
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Service"`
	}
	if err := registryGET(ctx, client, target, headers, &payload); err != nil {
		return nil, err
	}
	result := make(map[string]RegistryInstance, len(payload))
	for index, entry := range payload {
		id := entry.Service.ID
		if id == "" {
			id = strconv.Itoa(index)
		}
		address := entry.Service.Address
		if address == "" {
			address = entry.Node.Address
		}
		result[id] = RegistryInstance{ID: id, Address: address, Port: entry.Service.Port}
	}
	return result, nil
}

type nacosRegistryObserver struct{}

func (nacosRegistryObserver) Driver() string { return "nacos" }

func (nacosRegistryObserver) Snapshot(ctx context.Context, client *http.Client, base string, registry model.Registry, identity string, credentials map[string]string) (map[string]RegistryInstance, error) {
	query := url.Values{"serviceName": []string{identity}}
	if registry.Namespace != "" { query.Set("namespaceId", registry.Namespace) }
	if registry.Group != "" { query.Set("groupName", registry.Group) }
	accessToken := credentials["token"]
	if accessToken == "" && credentials["username"] != "" {
		login := url.Values{"username": []string{credentials["username"]}, "password": []string{credentials["password"]}}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/nacos/v1/auth/login", strings.NewReader(login.Encode()))
		if err != nil { return nil, err }
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			var transport *url.Error
			if errors.As(err, &transport) { return nil, fmt.Errorf("Nacos login failed: %v", transport.Err) }
			return nil, errors.New("Nacos login failed")
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 { return nil, fmt.Errorf("Nacos login returned HTTP %s", response.Status) }
		var payload struct { AccessToken string `json:"accessToken"` }
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil { return nil, fmt.Errorf("decode Nacos login response: %w", err) }
		accessToken = payload.AccessToken
		if accessToken == "" { return nil, errors.New("Nacos login returned no access token") }
	}
	if accessToken != "" { query.Set("accessToken", accessToken) }
	var payload struct {
		Hosts []struct {
			InstanceID string `json:"instanceId"`
			IP         string `json:"ip"`
			Port       int    `json:"port"`
		} `json:"hosts"`
	}
	if err := registryGET(ctx, client, base+"/nacos/v1/ns/instance/list?"+query.Encode(), nil, &payload); err != nil {
		return nil, err
	}
	result := make(map[string]RegistryInstance, len(payload.Hosts))
	for _, host := range payload.Hosts {
		id := host.InstanceID
		if id == "" { id = host.IP + ":" + strconv.Itoa(host.Port) }
		result[id] = RegistryInstance{ID: id, Address: host.IP, Port: host.Port}
	}
	return result, nil
}

type eurekaRegistryObserver struct{}

func (eurekaRegistryObserver) Driver() string { return "eureka" }

func (eurekaRegistryObserver) Snapshot(ctx context.Context, client *http.Client, base string, _ model.Registry, identity string, credentials map[string]string) (map[string]RegistryInstance, error) {
	headers := map[string]string{"Accept": "application/json"}
	if credentials["username"] != "" {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials["username"]+":"+credentials["password"]))
	}
	var payload struct {
		Application struct {
			Instances []struct {
				InstanceID string `json:"instanceId"`
				IPAddr     string `json:"ipAddr"`
				Port       struct { Value int `json:"$"` } `json:"port"`
			} `json:"instance"`
		} `json:"application"`
	}
	if err := registryGET(ctx, client, base+"/eureka/apps/"+url.PathEscape(strings.ToUpper(identity)), headers, &payload); err != nil {
		return nil, err
	}
	result := make(map[string]RegistryInstance, len(payload.Application.Instances))
	for _, instance := range payload.Application.Instances {
		result[instance.InstanceID] = RegistryInstance{ID: instance.InstanceID, Address: instance.IPAddr, Port: instance.Port.Value}
	}
	return result, nil
}

type etcdRegistryObserver struct{}

func (etcdRegistryObserver) Driver() string { return "etcd" }

func (etcdRegistryObserver) Snapshot(ctx context.Context, client *http.Client, base string, registry model.Registry, identity string, credentials map[string]string) (map[string]RegistryInstance, error) {
	prefix := strings.TrimRight(registry.Prefix, "/") + "/" + identity + "/"
	requestBody, _ := json.Marshal(map[string]string{
		"key": base64.StdEncoding.EncodeToString([]byte(prefix)),
		"range_end": base64.StdEncoding.EncodeToString(prefixRangeEnd([]byte(prefix))),
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v3/kv/range", bytes.NewReader(requestBody))
	if err != nil { return nil, err }
	request.Header.Set("Content-Type", "application/json")
	if credentials["username"] != "" {
		request.SetBasicAuth(credentials["username"], credentials["password"])
	}
	if credentials["token"] != "" { request.Header.Set("Authorization", credentials["token"]) }
	response, err := client.Do(request)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) { return nil, fmt.Errorf("etcd registry request failed: %v", transport.Err) }
		return nil, errors.New("etcd registry request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 { return nil, fmt.Errorf("HTTP %s", response.Status) }
	var payload struct { KVs []struct { Key string `json:"key"`; Value string `json:"value"` } `json:"kvs"` }
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload); err != nil { return nil, err }
	result := make(map[string]RegistryInstance, len(payload.KVs))
	for _, item := range payload.KVs {
		key, _ := base64.StdEncoding.DecodeString(item.Key)
		value, _ := base64.StdEncoding.DecodeString(item.Value)
		id := strings.TrimPrefix(string(key), prefix)
		instance := RegistryInstance{ID: id}
		var decoded struct { Address string `json:"address"`; Port int `json:"port"` }
		if json.Unmarshal(value, &decoded) == nil { instance.Address = decoded.Address; instance.Port = decoded.Port }
		result[id] = instance
	}
	return result, nil
}

func prefixRangeEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for index := len(end) - 1; index >= 0; index-- {
		if end[index] < 0xff {
			end[index]++
			return end[:index+1]
		}
	}
	return []byte{0}
}
