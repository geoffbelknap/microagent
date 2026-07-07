package secret

import (
	"net/http"
	"os"
)

// DefaultRegistry builds a Registry with the built-in schemes registered:
// env, file, dotenv (plaintext), vault (external manager), and helper (an
// operator-owned credential-helper binary named by MICROAGENT_SECRET_HELPER —
// how embedding platforms plug in cloud secret managers without adding their
// SDKs here). getenv defaults to os.Getenv; warn receives plaintext warnings
// (nil drops them). Vault address and token are read from VAULT_ADDR /
// VAULT_TOKEN via getenv.
func DefaultRegistry(getenv func(string) string, warn func(string)) *Registry {
	if getenv == nil {
		getenv = os.Getenv
	}
	r := NewRegistry(warn)
	r.Register("env", &EnvProvider{Getenv: getenv})
	r.Register("file", &FileProvider{})
	r.Register("dotenv", &DotenvProvider{})
	r.Register("vault", &VaultProvider{
		Addr:   getenv("VAULT_ADDR"),
		Token:  getenv("VAULT_TOKEN"),
		Client: http.DefaultClient,
	})
	r.Register("helper", &HelperProvider{Command: getenv("MICROAGENT_SECRET_HELPER")})
	return r
}
