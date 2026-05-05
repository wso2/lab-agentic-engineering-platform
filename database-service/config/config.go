package config

// Config holds the service configuration.
type Config struct {
	ServerHost   string
	ServerPort   int
	LogLevel     string
	MySQLRootURL string
	MySQLHost    string
	MySQLPort    int
}
