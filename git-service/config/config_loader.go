package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type configReader struct {
	errors []error
}

// Load reads configuration from environment variables.
// If ENV_FILE_PATH is set, variables are loaded from that file first.
func Load() (Config, error) {
	if envFile := os.Getenv("ENV_FILE_PATH"); envFile != "" {
		if err := loadEnvFile(envFile); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to load env file %s: %v\n", envFile, err)
		}
	}

	r := &configReader{}
	cfg := Config{
		ServerHost:           r.readOptionalString("SERVER_HOST", "0.0.0.0"),
		ServerPort:           r.readOptionalInt("SERVER_PORT", 3300),
		LogLevel:             r.readOptionalString("LOG_LEVEL", "info"),
		DatabaseURL:          r.databaseURL(),
		RepoBasePath:         r.readRequiredString("REPO_BASE_PATH"),
		GitHubRepoVisibility: r.readOptionalString("GITHUB_REPO_VISIBILITY", "private"),
		GitHubCommitterName:  r.readOptionalString("GIT_COMMITTER_NAME", "ASDLC Bot"),
		GitHubCommitterEmail: r.readOptionalString("GIT_COMMITTER_EMAIL", "bot@asdlc.dev"),
		GitHubPlatformPAT:         r.readOptionalString("GITHUB_PLATFORM_PAT", ""),
		GitHubRepoOwner:           r.readOptionalString("GITHUB_REPO_OWNER", ""),
		GitHubPlatformPATSeedOrgs: r.readOptionalString("GITHUB_PLATFORM_PAT_SEED_ORGS", "default"),
		WebhookDeliveryURL:   r.readOptionalString("GITHUB_WEBHOOK_DELIVERY_URL", ""),
		WebhookHMACSecret:    r.readOptionalString("GITHUB_WEBHOOK_SECRET", ""),
		TestMode:             r.readOptionalBool("TEST_MODE", false),
		DeploymentTier:          r.readOptionalString("DEPLOYMENT_TIER", "dev"),
		// TODO: replace this default with a real key in Vault before deploying to any
		// shared environment. Generate with: openssl rand -base64 32
		// The default (32 zero bytes) means any pod without the env var set can decrypt
		// data written by another pod without the env var set — fine for a single-dev
		// local setup, not acceptable for shared or production environments.
		CredentialEncryptionKey: r.readOptionalString("CREDENTIAL_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="),
		OpenBaoAddr:             r.readOptionalString("OPENBAO_ADDR", ""),
		OpenBaoToken:            r.readOptionalString("OPENBAO_TOKEN", ""),
		GitHubAppID:             r.readOptionalString("GITHUB_APP_ID", ""),
		GitHubAppClientID:       r.readOptionalString("GITHUB_CLIENT_ID", ""),
		GitHubAppClientSecret:   r.readOptionalString("GITHUB_CLIENT_SECRET", ""),
		GitHubAppSlug:           r.readOptionalString("GITHUB_APP_SLUG", "asdlc-platform"),
		GitHubAppPrivateKeyPath: r.readOptionalString("GITHUB_APP_PRIVATE_KEY_PATH", ""),
		// Default 24h per phase2.md §6.10. Tests force fast ticks via env.
		CredentialValidatorInterval: r.readOptionalDuration("CREDENTIAL_VALIDATOR_INTERVAL", 24*time.Hour),
		JWKSURL:                     r.readOptionalString("JWKS_URL", ""),
		BFFJWKSURL:                  r.readOptionalString("BFF_JWKS_URL", ""),
		JWTAllowedIssuer:            r.readOptionalString("JWT_ISSUER", ""),
		JWTAllowedAudience:          r.readOptionalString("JWT_AUDIENCE", "git-service"),
		JWTResourceMetadataURL:      r.readOptionalString("JWT_RESOURCE_METADATA_URL", ""),
		TaskJWTAllowedIssuer:        r.readOptionalString("TASK_JWT_ISSUER", "asdlc-bff"),
		TaskJWTAllowedAudience:      r.readOptionalString("TASK_JWT_AUDIENCE", "git-service"),
	}

	if len(r.errors) > 0 {
		msgs := make([]string, len(r.errors))
		for i, e := range r.errors {
			msgs[i] = e.Error()
		}
		return Config{}, fmt.Errorf("configuration errors:\n%s", strings.Join(msgs, "\n"))
	}

	return cfg, nil
}

// databaseURL builds the Postgres DSN. When DATABASE_URL is set it is used
// verbatim — convenient for local dev with a hand-written URL. Otherwise the
// URL is assembled from DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME,
// which is the shape the platform release-binding provides. Mirrors the
// approach used by agent-manager-service.
func (r *configReader) databaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	host := r.readRequiredString("DB_HOST")
	port := r.readOptionalInt("DB_PORT", 5432)
	user := r.readRequiredString("DB_USER")
	password := r.readRequiredString("DB_PASSWORD")
	name := r.readRequiredString("DB_NAME")
	params := url.Values{}
	if mode := os.Getenv("DB_SSLMODE"); mode != "" {
		params.Set("sslmode", mode)
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     "/" + name,
		RawQuery: params.Encode(),
	}
	return u.String()
}

func (r *configReader) readRequiredString(key string) string {
	val := os.Getenv(key)
	if val == "" {
		r.errors = append(r.errors, fmt.Errorf("%s is required", key))
	}
	return val
}

func (r *configReader) readOptionalString(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func (r *configReader) readOptionalInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		r.errors = append(r.errors, fmt.Errorf("%s must be an integer: %w", key, err))
		return defaultVal
	}
	return n
}

func (r *configReader) readOptionalBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return strings.EqualFold(val, "true") || val == "1"
}

// readOptionalDuration parses CREDENTIAL_VALIDATOR_INTERVAL-style env
// values via time.ParseDuration. Falls back to defaultVal on empty or
// unparseable input — bad values are recorded as errors so startup fails
// loudly rather than silently using the default.
func (r *configReader) readOptionalDuration(key string, defaultVal time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		r.errors = append(r.errors, fmt.Errorf("%s: %w", key, err))
		return defaultVal
	}
	return d
}

func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, value) //nolint:errcheck
		}
	}
	return scanner.Err()
}
