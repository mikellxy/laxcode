package config

import (
	"fmt"
	"os"
	"regexp"
)

var credentialEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The on-disk key is an environment variable name. OpenShell supplies its opaque value.
// Never persist the resolved reference: it belongs to this sandbox activation.
func LoadProviderAPIKeys(providers []Provider) error {
	for i := range providers {
		p := &providers[i]
		name := p.OpenaiApiKey
		if !credentialEnvName.MatchString(name) {
			return fmt.Errorf("provider %q requires an API key environment variable name", p.ProviderName)
		}
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("provider %q: credential environment %s is empty or missing", p.ProviderName, name)
		}
		p.OpenaiApiKey = value
	}
	return nil
}
