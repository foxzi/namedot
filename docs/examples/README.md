# Configuration Examples

This directory contains example configuration files for various namedot deployment scenarios.

## Available Examples

### Basic Configuration
- **`config.yaml`** - Basic standalone configuration
  - SQLite database
  - Local development setup
  - All features enabled for testing

### Replication Setup
- **`config.master.yaml`** - Master server configuration
  - Configured as replication master
  - Web admin enabled
  - Public REST API for slave connections

- **`config.slave.yaml`** - Slave server configuration
  - Configured as replication slave
  - Syncs from master server
  - Web admin disabled (read-only)

### Database Backends
- **`config.mysql.yaml`** - MySQL/MariaDB backend
  - Production-ready MySQL configuration
  - Connection pooling settings

- **`config.postgres.yaml`** - PostgreSQL backend
  - Production-ready PostgreSQL configuration
  - Connection pooling settings

### Container Deployment
- **`config.docker.yaml`** - Docker environment
  - Configured for containerized deployment
  - Environment variable integration

## Usage

1. Copy the example that matches your use case:
   ```bash
   cp docs/examples/config.yaml config.yaml
   ```

2. Edit the configuration:
   ```bash
   nano config.yaml
   ```

3. Generate required secrets:
   ```bash
   # Generate API token hash
   ./namedot -g your-secret-token

   # Generate admin password hash
   ./namedot -p your-admin-password
   ```

4. Update the configuration with generated hashes

5. Test the configuration:
   ```bash
   ./namedot -t
   ```

6. Start the server:
   ```bash
   ./namedot -c config.yaml
   ```

## Configuration Options

All examples support the following key features:

### REST API Security
- `api_tokens` - Named API tokens (`name` + bcrypt `hash`), the name is shown in API logs
- `api_token_hash` - Bcrypt hash of a single API token (logged as `default`)
- `allowed_cidrs` - IP-based access control (CIDR whitelist)
- `tls_cert_file` / `tls_key_file` - HTTPS/TLS configuration

### Database Options
- `driver` - Database backend: sqlite, mysql, postgres
- `dsn` - Database connection string

### GeoIP
- `enabled` - Enable/disable GeoDNS functionality
- `mmdb_path` - Path to MaxMind GeoIP database files
- `download_urls` - Automatic MMDB download URLs
- `use_ecs` - Use EDNS Client Subnet for accurate geolocation

### Replication
- `mode` - Replication mode: standalone, master, slave
- `master_url` - Master server URL (slave only)
- `sync_interval_sec` - Sync interval in seconds (slave only)
- `api_token` - Plain token for outgoing requests to master (slave only)

### Web Admin Panel
- `enabled` - Enable/disable web admin interface
- `username` / `password_hash` - Admin credentials

### Default NS Servers
- `ns.servers` - List of default NS server names (supports `{zone}` placeholder)
- `ns.auto_on_missing` - Auto-create NS records when creating new zones
- `ns.auto_fix` - Auto-fix missing NS records on server startup
- `ns.ttl` - TTL for NS records (default: 86400)

## After Installation

When installed from a package, these examples are available at:
- **DEB/RPM**: `/usr/share/doc/namedot/examples/`

The default configuration is installed at:
- **DEB/RPM**: `/etc/namedot/config.yaml`

## Documentation

For more information, see:
- [Main README](../README.md)
- [Replication Guide](../REPLICATION.md)
- [Web Admin Guide](../WEBADMIN.md)
- [Docker Guide](../DOCKER.md)
