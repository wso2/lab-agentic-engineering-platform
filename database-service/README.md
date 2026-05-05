# Database Provisioning Service

A standalone microservice for provisioning MySQL databases and testing connections. This service is designed to be called directly by other services (not through the BFF).

## Overview

The Database Provisioning Service provides two main endpoints:
1. **Provision Database** - Creates a new MySQL database with auto-generated credentials
2. **Test Connection** - Validates connection to a provisioned database

## Endpoints

### POST /api/v1/databases/provision

Provisions a new MySQL database with auto-generated user credentials.

**Request:**
```json
{
  "projectName": "my-project"
}
```

**Response (201 Created):**
```json
{
  "host": "mysql",
  "port": 3306,
  "database": "my_project_abc123",
  "username": "user_my_project_xyz789",
  "password": "Xy7#kL@mN9$pQ2*rT"
}
```

**Error Response (400/500):**
```json
{
  "error": "failed to provision database"
}
```

### POST /api/v1/databases/test-connection

Tests whether the provided credentials can connect to the specified database.

**Request:**
```json
{
  "host": "mysql",
  "port": 3306,
  "database": "my_project_abc123",
  "username": "user_my_project_xyz789",
  "password": "Xy7#kL@mN9$pQ2*rT"
}
```

**Response (200 OK - Success):**
```json
{
  "status": "success",
  "message": "connection test passed"
}
```

**Response (200 OK - Failed):**
```json
{
  "status": "failed",
  "message": "ping database: dial tcp: lookup mysql on ...: no such host"
}
```

## Configuration

The service is configured via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | `0.0.0.0` | HTTP server host |
| `SERVER_PORT` | `3500` | HTTP server port |
| `LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `MYSQL_ROOT_URL` | `root:root@tcp(mysql:3306)/` | MySQL root connection string |
| `MYSQL_HOST` | `mysql` | MySQL host returned in credentials |
| `MYSQL_PORT` | `3306` | MySQL port returned in credentials |

## Development

```bash
# Build
go build -o database-service ./cmd/database-service

# Run locally (requires MySQL running)
MYSQL_ROOT_URL="root:password@tcp(localhost:3306)/" go run ./cmd/database-service/main.go

# Test endpoints
curl -X POST http://localhost:3500/api/v1/databases/provision \
  -H "Content-Type: application/json" \
  -d '{"projectName":"test-proj"}'

curl -X POST http://localhost:3500/api/v1/databases/test-connection \
  -H "Content-Type: application/json" \
  -d '{
    "host": "localhost",
    "port": 3306,
    "database": "test_proj_xyz",
    "username": "user_test_proj_xyz",
    "password": "MySecurePassword123!"
  }'
```

## How It Works

### Database Provisioning
1. Sanitizes the project name for use in MySQL identifiers
2. Generates a random database name (max 30 characters)
3. Generates a random username and secure password
4. Connects to MySQL as root using provided credentials
5. Creates the database with `CREATE DATABASE`
6. Creates a dedicated user with `CREATE USER`
7. Grants all privileges on that database to the user
8. Returns the connection credentials to the caller

### Connection Testing
1. Accepts database credentials
2. Attempts to connect using the provided credentials
3. Runs a simple query (`SELECT DATABASE()`) to verify access
4. Returns success/failure status

## Security Considerations

- Passwords are generated with mixed character types and are 16 characters long
- Each database gets a dedicated user (not shared)
- Users have privileges only on their assigned database (wildcard host `%` for remote connections)
- The service requires root credentials to be provided at startup (typically set in docker-compose environment)
- This is an internal service and should not be exposed to external clients
