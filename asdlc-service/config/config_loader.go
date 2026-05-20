package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
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
		ServerHost: r.readOptionalString("SERVER_HOST", "0.0.0.0"),
		ServerPort: r.readOptionalInt("SERVER_PORT", 8080),
		LogLevel:   r.readOptionalString("LOG_LEVEL", "info"),
		PlatformAPI: PlatformAPIConfig{
			BaseURL:    r.readRequiredString("PLATFORM_API_SERVICE_BASE_URL"),
			HostHeader: r.readOptionalString("PLATFORM_API_SERVICE_HOST", ""),
		},
		DatabaseURL:            r.databaseURL(),
		TestMode:               r.readOptionalBool("TEST_MODE", false),
		DeploymentTier:         r.readOptionalString("DEPLOYMENT_TIER", "dev"),
		GitHubWebhookSecret:    r.readOptionalString("GITHUB_WEBHOOK_SECRET", ""),
		OAuthStateSigningKey:   r.readOptionalString("OAUTH_STATE_SIGNING_KEY", ""),
		GithubAppSlug:          r.readOptionalString("GITHUB_APP_SLUG", "asdlc-platform"),
		GithubAppClientID:      r.readOptionalString("GITHUB_CLIENT_ID", ""),
		BFFPublicURL:           r.readOptionalString("BFF_PUBLIC_URL", "http://localhost:8090"),
		BuildAuthRetryBudget:   r.readOptionalInt("BUILD_AUTH_RETRY_BUDGET", 3),
		FeatureEmitAPITrait:    r.readOptionalBool("FEATURE_EMIT_API_TRAIT", true),
		ThunderAdmin: ThunderAdminConfig{
			BaseURL:      r.readOptionalString("THUNDER_ADMIN_URL", ""),
			ClientID:     r.readOptionalString("THUNDER_SYSTEM_CLIENT_ID", "asdlc-system-client"),
			ClientSecret: r.readOptionalString("THUNDER_SYSTEM_CLIENT_SECRET", "asdlc-system-client-secret"),
		},
		PlatformIDP: PlatformIDPDefaults{
			Issuer:  r.readOptionalString("PLATFORM_IDP_ISSUER", "http://thunder.openchoreo.localhost:8080"),
			JWKSURL: r.readOptionalString("PLATFORM_IDP_JWKS_URL", "http://thunder-service.thunder.svc.cluster.local:8090/oauth2/jwks"),
		},
		UserAppsOIDC: UserAppsOIDCConfig{
			Issuer:            r.readOptionalString("USER_APPS_OIDC_ISSUER", "http://thunder.openchoreo.localhost:8080"),
			ClientID:          r.readOptionalString("USER_APPS_OIDC_CLIENT_ID", ""),
			Scopes:            r.readOptionalString("USER_APPS_OIDC_SCOPES", "openid profile"),
			InternalProxyPass: r.readOptionalString("USER_APPS_OIDC_INTERNAL_PROXY_PASS", "http://thunder-service.thunder.svc.cluster.local:8090/oauth2/"),
		},
		TaskTokenSigningKey:    r.taskSigningKey(),
		TaskTokenIssuer:        r.readOptionalString("BFF_TASK_TOKEN_ISSUER", "asdlc-bff"),
		TaskTokenAudience:      r.readOptionalString("BFF_TASK_TOKEN_AUDIENCE", "git-service"),
		JWKSURL:                r.readOptionalString("JWKS_URL", ""),
		JWTAllowedIssuer:       r.readOptionalString("JWT_ISSUER", ""),
		JWTAllowedAudience:     r.readOptionalString("JWT_AUDIENCE", "asdlc-bff"),
		JWTResourceMetadataURL: r.readOptionalString("JWT_RESOURCE_METADATA_URL", ""),
		Observability: ObservabilityConfig{
			BaseURL:      r.readOptionalString("OBSERVER_URL", r.readOptionalString("OBSERVABILITY_SERVICE_BASE_URL", "")),
			TokenURL:     r.readOptionalString("OBSERVER_OAUTH_TOKEN_URL", ""),
			ClientID:     r.readOptionalString("OBSERVER_OAUTH_CLIENT_ID", ""),
			ClientSecret: r.readOptionalString("OBSERVER_OAUTH_CLIENT_SECRET", ""),
			HostHeader:   r.readOptionalString("OBSERVER_OAUTH_HOST_HEADER", ""),
		},
		AgentsService: AgentsServiceConfig{
			BaseURL: r.readOptionalString("AGENTS_SERVICE_BASE_URL", ""),
		},
		AgentGitServiceURL: r.readOptionalString("AGENT_GIT_SERVICE_URL", ""),
		AgentPlatformURL:   r.readOptionalString("AGENT_PLATFORM_URL", ""),
		ServiceAuth: ServiceAuthConfig{
			TokenURL:     r.readOptionalString("SERVICE_AUTH_TOKEN_URL", ""),
			ClientID:     r.readOptionalString("SERVICE_AUTH_CLIENT_ID", ""),
			ClientSecret: r.readOptionalString("SERVICE_AUTH_CLIENT_SECRET", ""),
			HostHeader:   r.readOptionalString("SERVICE_AUTH_HOST_HEADER", ""),
		},
		ServiceAuthGitService: ServiceAuthConfig{
			TokenURL:     r.readOptionalString("SERVICE_AUTH_GIT_TOKEN_URL", ""),
			ClientID:     r.readOptionalString("SERVICE_AUTH_GIT_CLIENT_ID", ""),
			ClientSecret: r.readOptionalString("SERVICE_AUTH_GIT_CLIENT_SECRET", ""),
			HostHeader:   r.readOptionalString("SERVICE_AUTH_GIT_HOST_HEADER", ""),
		},
		ServiceAuthAgentsService: ServiceAuthConfig{
			TokenURL:     r.readOptionalString("SERVICE_AUTH_AGENTS_TOKEN_URL", ""),
			ClientID:     r.readOptionalString("SERVICE_AUTH_AGENTS_CLIENT_ID", ""),
			ClientSecret: r.readOptionalString("SERVICE_AUTH_AGENTS_CLIENT_SECRET", ""),
			HostHeader:   r.readOptionalString("SERVICE_AUTH_AGENTS_HOST_HEADER", ""),
		},
		GitService: GitServiceConfig{
			BaseURL: r.readOptionalString("GIT_SERVICE_BASE_URL", ""),
		},
		DatabaseService: DatabaseServiceConfig{
			BaseURL: r.readOptionalString("DATABASE_SERVICE_BASE_URL", ""),
		},
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

// taskSigningKey reads the BFF Task JWT signing PEM. BFF_TASK_SIGNING_KEY
// takes precedence; BFF_TASK_SIGNING_KEY_PATH is the file-mount fallback
// docker-compose deployments use (multi-line PEM survives a bind mount
// cleanly; env-var passing across compose `${VAR}` substitution does not).
func (r *configReader) taskSigningKey() string {
	if v := os.Getenv("BFF_TASK_SIGNING_KEY"); v != "" {
		return v
	}
	path := os.Getenv("BFF_TASK_SIGNING_KEY_PATH")
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		r.errors = append(r.errors, fmt.Errorf("read BFF_TASK_SIGNING_KEY_PATH %s: %w", path, err))
		return ""
	}
	return string(b)
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
