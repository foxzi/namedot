# namedot

Lightweight GeoDNS server with REST API, Web Admin Panel, and Master-Slave replication.

## Features

- **DNS Server**: UDP/TCP DNS server with DNSSEC support
- **GeoDNS**: Geographic routing based on country, continent, or subnet
- **REST API**: Full API for zone and record management
- **Web Admin**: User-friendly web interface for DNS management
- **Replication**: Master-Slave replication for high availability
- **Multiple Backends**: SQLite, PostgreSQL, MySQL support
- **HTTPS Support**: TLS/SSL with automatic certificate reloading
- **Access Control**: IP-based CIDR whitelisting for REST API
- **Named API Tokens**: per-client REST API tokens, token name is shown in API logs

## Quick Start

```bash
# Install from package (Debian/Ubuntu)
sudo dpkg -i namedot_*.deb

# Or build from source
make build

# Generate API token hash
./namedot -g mytoken

# Start server
./namedot -c config.yaml
```

Note: REST API requests are rejected unless `api_tokens`, `api_token_hash` or `api_token` is configured. `api_tokens` gives each client its own named token (the name is shown in API logs); see [Full README](./docs/README.md).

## Documentation

📚 **Full documentation is available in the [docs](./docs) directory:**

- **[Full README](./docs/README.md)** - Complete documentation
- **[Replication Guide](./docs/REPLICATION.md)** - Master-Slave setup
- **[Web Admin Guide](./docs/WEBADMIN.md)** - Web interface documentation
- **[Docker Guide](./docs/DOCKER.md)** - Docker deployment
- **[Package Guide](./docs/PACKAGE_DOCS.md)** - Installation from packages
- **[Configuration Examples](./docs/examples/)** - Sample configurations

## Configuration Examples

After installation, configuration examples are available in:
- **Source**: `./docs/examples/`
- **Installed package**: `/usr/share/doc/namedot/examples/`

Available examples:
- `config.yaml` - Basic configuration
- `config.master.yaml` - Master replication setup
- `config.slave.yaml` - Slave replication setup
- `config.mysql.yaml` - MySQL backend
- `config.postgres.yaml` - PostgreSQL backend
- `config.docker.yaml` - Docker environment

## License

MIT License - see [LICENSE](./LICENSE) file for details.

## Author

Sergei Vorontsov <piligrim@rootnix.net>

## Repository

https://github.com/foxzi/namedot
