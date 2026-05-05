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

	return &Config{
		ServerHost:   getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:   serverPort,
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		MySQLRootURL: getEnv("MYSQL_ROOT_URL", "root:root@tcp(mysql:3306)/"),
		MySQLHost:    getEnv("MYSQL_HOST", "mysql"),
		MySQLPort:    mysqlPort,
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
