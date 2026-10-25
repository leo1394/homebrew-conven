package runtime

import (
	"fmt"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func selectApolloConnectionReadiness(plan *Plan, context config.ExpandContext, required map[string]bool) error {
	for name, service := range plan.Services {
		if service.Config == nil || service.Config.Plan.SourceDriver != materialize.SourceApollo { continue }
		address, err := materialize.ApolloReadinessAddress(service.Config.Plan)
		if err != nil { return fmt.Errorf("resolve service %s Apollo readiness: %w", name, err) }
		matched := false
		for _, endpoint := range plan.Environment.Connection.Readiness {
			candidate, err := config.Expand(endpoint.Address, context)
			if err != nil { return fmt.Errorf("expand connection readiness %q: %w", endpoint.Name, err) }
			if strings.EqualFold(strings.TrimSpace(candidate), address) {
				required["endpoint:"+endpoint.Name] = true
				matched = true
			}
		}
		if !matched { return fmt.Errorf("service %s Apollo endpoint %s has no matching connection readiness check", name, address) }
		delete(required, "apollo")
	}
	return nil
}
