package config

// Config holds the service configuration.
type Config struct {
	ServerHost   string
	ServerPort   int
	LogLevel     string
	DatabaseURL  string
	MySQLRootURL string
	MySQLHost    string
	MySQLPort    int

	// JWT authentication — task JWTs issued by the BFF.
	BFFJWKSURL     string
	TaskJWTIssuer  string
	TaskJWTAudience string
}
