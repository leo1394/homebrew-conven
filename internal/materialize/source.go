package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxApolloResponseBytes = int64(16 << 20)
	maxApolloRetryAfter    = 30 * time.Second
)

type SourceInput struct {
	Application []byte
	Bootstrap   []byte
	Apollo      Apollo
}

type ConfigSource interface {
	Application(context.Context, SourceInput) ([]byte, error)
}

type RepositoryAdapter struct{}

func (RepositoryAdapter) Application(ctx context.Context, input SourceInput) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]byte(nil), input.Application...), nil
}

type ApolloAdapter struct {
	Client *http.Client
}

type apolloBootstrap struct {
	AppID         string `yaml:"appId"`
	Cluster       string `yaml:"cluster"`
	IP            string `yaml:"ip"`
	NamespaceName string `yaml:"namespaceName"`
}

type apolloResponse struct {
	Configurations struct {
		Content string `json:"content"`
	} `json:"configurations"`
}

func (adapter ApolloAdapter) Application(ctx context.Context, input SourceInput) ([]byte, error) {
	bootstrap := apolloBootstrap{}
	document, err := decodeStrictSingleYAML(input.Bootstrap, "Apollo bootstrap")
	if err != nil {
		return nil, fmt.Errorf("decode Apollo bootstrap: %w", err)
	}
	if err := document.Decode(&bootstrap); err != nil {
		return nil, fmt.Errorf("decode Apollo bootstrap: %w", err)
	}
	bootstrap.AppID = strings.TrimSpace(bootstrap.AppID)
	bootstrap.Cluster = strings.TrimSpace(bootstrap.Cluster)
	bootstrap.IP = strings.TrimSpace(bootstrap.IP)
	bootstrap.NamespaceName = strings.TrimSpace(bootstrap.NamespaceName)
	if bootstrap.AppID == "" || bootstrap.Cluster == "" || bootstrap.IP == "" || bootstrap.NamespaceName == "" {
		return nil, errors.New("Apollo bootstrap requires appId, cluster, ip, and namespaceName")
	}
	if strings.Contains(bootstrap.NamespaceName, ",") {
		return nil, errors.New("Apollo source supports exactly one namespaceName")
	}
	endpoint := bootstrap.IP
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Apollo endpoint %q", bootstrap.IP)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/configs/" + url.PathEscape(bootstrap.AppID) + "/" + url.PathEscape(bootstrap.Cluster) + "/" + url.PathEscape(bootstrap.NamespaceName)
	query := parsed.Query()
	query.Set("ip", "127.0.0.1")
	parsed.RawQuery = query.Encode()
	attempts := input.Apollo.Attempts
	if attempts == 0 {
		attempts = 3
	}
	timeout := input.Apollo.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	client := adapter.Client
	if client == nil {
		client = &http.Client{}
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		retryDelay := input.Apollo.RetryDelay
		if retryDelay == 0 {
			retryDelay = time.Duration(1<<(attempt-1)) * 250 * time.Millisecond
		}
		requestContext, cancel := context.WithTimeout(ctx, timeout)
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
		if err != nil {
			cancel()
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, maxApolloResponseBytes+1))
			response.Body.Close()
			if readErr != nil {
				err = readErr
			} else if int64(len(body)) > maxApolloResponseBytes {
				err = fmt.Errorf("Apollo response exceeds %d bytes", maxApolloResponseBytes)
			} else if response.StatusCode < 200 || response.StatusCode >= 300 {
				err = fmt.Errorf("Apollo returned HTTP %d", response.StatusCode)
				if !retryableApolloStatus(response.StatusCode) {
					cancel()
					return nil, err
				}
				if delay, ok := parseRetryAfter(response.Header.Get("Retry-After"), time.Now()); ok {
					retryDelay = delay
				}
			} else {
				payload := apolloResponse{}
				if err = json.Unmarshal(body, &payload); err == nil {
					if payload.Configurations.Content == "" {
						err = errors.New("Apollo configurations.content is empty")
					} else {
						cancel()
						return []byte(payload.Configurations.Content), nil
					}
				}
			}
		} else if response != nil {
			response.Body.Close()
		}
		cancel()
		lastErr = err
		if attempt < attempts {
			if err := waitRetry(ctx, retryDelay); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("fetch Apollo application after %d attempt(s): %w", attempts, lastErr)
}

func retryableApolloStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status < 600
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		if seconds < 0 {
			return 0, false
		}
		if seconds >= int64(maxApolloRetryAfter/time.Second) {
			return maxApolloRetryAfter, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := date.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if delay > maxApolloRetryAfter {
		delay = maxApolloRetryAfter
	}
	return delay, true
}

func adapterFor(driver SourceDriver) (ConfigSource, error) {
	switch driver {
	case SourceRepository:
		return RepositoryAdapter{}, nil
	case SourceApollo:
		return ApolloAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported config source driver %q", driver)
	}
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ ConfigSource = RepositoryAdapter{}
var _ ConfigSource = ApolloAdapter{}
