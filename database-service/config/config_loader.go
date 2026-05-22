package config

import (
	"os"
	"strconv"
)

// Load reads environment variables and returns a Config.
func Load() (*Config, error) {
	serverPort := 3500 // default port
	if p := os.Getenv("SERVER_PORT"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			serverPort = parsed
		}
	}

	mysqlPort := 3306 // default MySQL port
	if p := os.Getenv("MYSQL_PORT"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			mysqlPort = parsed
		}
	}

	mysqlHost := getEnv("MYSQL_HOST", "mysql")
	mysqlRootUser := getEnv("MYSQL_ROOT_USER", "root")
	mysqlRootPassword := getEnv("MYSQL_ROOT_PASSWORD", "root")
	// Build the root DSN from individual env vars so MYSQL_HOST/MYSQL_ROOT_USER/
	// MYSQL_ROOT_PASSWORD (as provided by docker-compose) are honoured.
	// Fall back to a MYSQL_ROOT_URL override if explicitly set.
	defaultRootURL := mysqlRootUser + ":" + mysqlRootPassword + "@tcp(" + mysqlHost + ":" + strconv.Itoa(mysqlPort) + ")/"
	return &Config{
		ServerHost:   getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:   serverPort,
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		DatabaseURL:  getEnv("DATABASE_URL", ""),
		MySQLRootURL:    getEnv("MYSQL_ROOT_URL", defaultRootURL),
		MySQLHost:       mysqlHost,
		MySQLPort:       mysqlPort,
		BFFJWKSURL:      getEnv("BFF_JWKS_URL", ""),
		TaskJWTIssuer:   getEnv("TASK_JWT_ISSUER", "asdlc-bff"),
		TaskJWTAudience: getEnv("TASK_JWT_AUDIENCE", "database-service"),
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
