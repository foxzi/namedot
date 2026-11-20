# Docker Deployment Guide

This guide covers running SmaillGeoDNS using Docker and Docker Compose.

## Quick Start (SQLite)

The simplest way to run SmaillGeoDNS is using Docker Compose with SQLite:

```bash
# Build and start
docker-compose up -d

# View logs
docker-compose logs -f geodns

# Stop
docker-compose down
```

The server will be available at:
- DNS: `udp://localhost:8053` and `tcp://localhost:8053`
- REST API: `http://localhost:18080`
- Health check: `http://localhost:18080/health`

## Configuration Files

- `examples/config.docker.yaml` - SQLite (default, standalone)
- `examples/config.postgres.yaml` - PostgreSQL backend
- `examples/config.mysql.yaml` - MySQL backend

## Database Backends

### PostgreSQL

Start GeoDNS with PostgreSQL:

```bash
docker-compose --profile postgres up -d
```

Services:
- `geodns-postgres` - DNS server on ports 8054 (DNS) and 18081 (API)
- `postgres` - PostgreSQL 16 database

### MySQL

Start GeoDNS with MySQL:

```bash
docker-compose --profile mysql up -d
```

Services:
- `geodns-mysql` - DNS server on ports 8055 (DNS) and 18082 (API)
- `mysql` - MySQL 8.0 database

## Building Docker Image

Build the image manually:

```bash
docker build -t namedot:latest .
```

Run standalone container:

```bash
docker run -d \
  --name geodns \
  -p 8053:53/udp \
  -p 8053:53/tcp \
  -p 18080:8080/tcp \
  -v $(pwd)/examples/config.docker.yaml:/app/config.yaml:ro \
  -v $(pwd)/geoipdb:/app/geoipdb:ro \
  -v geodns-data:/data \
  namedot:latest
```

## Volumes

- `/data` - SQLite database storage
- `/app/config.yaml` - Configuration file (mount your own)
- `/app/geoipdb` - GeoIP MMDB databases

## Environment Variables

- `TZ` - Timezone (default: UTC)

## Health Check

The health endpoint returns server status:

```bash
curl http://localhost:18080/health
```

Response:
```json
{
  "status": "ok",
  "db": "ok"
}
```

## Testing DNS

Test GeoDNS queries with different GeoIP locations:

```bash
# Test RU geo routing
dig @localhost -p 8053 www.example11.com A +subnet=127.0.1.1

# Test GB/EU geo routing
dig @localhost -p 8053 www.example11.com A +subnet=127.0.2.1
```

## Production Deployment

For production use:

1. **Change the API token** in your config file (prefer named `api_tokens` with bcrypt hashes, see [README.md](README.md))
2. **Use a proper database** (PostgreSQL or MySQL recommended)
3. **Mount production GeoIP databases** (e.g., MaxMind GeoLite2)
4. **Configure resource limits** in docker-compose.yml:

```yaml
services:
  geodns:
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 512M
        reservations:
          cpus: '0.5'
          memory: 128M
```

5. **Enable TLS** for the REST API (use a reverse proxy like nginx)
6. **Configure logging** to external storage
7. **Set up monitoring** using the `/health` endpoint

## Troubleshooting

### Check logs

```bash
docker-compose logs -f geodns
```

### Verify GeoIP databases loaded

```bash
docker-compose logs geodns | grep GeoIP
```

Expected output:
```
GeoIP: loaded ASN IPv4 DB geoipdb/asn-localhost.mmdb
GeoIP: loaded ASN IPv6 DB geoipdb/asn-localhost6.mmdb
GeoIP: loaded City IPv4 DB geoipdb/city-localhost.mmdb
GeoIP: loaded City IPv6 DB geoipdb/city-localhost6.mmdb
```

### Database connection issues

Check database health:

```bash
# PostgreSQL
docker-compose exec postgres pg_isready -U geodns

# MySQL
docker-compose exec mysql mysqladmin ping -u geodns -p
```

### Port conflicts

If ports are already in use, modify the port mappings in `docker-compose.yml`:

```yaml
ports:
  - "9053:53/udp"  # Change 8053 to 9053
  - "9053:53/tcp"
  - "19080:8080/tcp"  # Change 18080 to 19080
```

## Updating

Pull latest changes and rebuild:

```bash
git pull
docker-compose build --no-cache
docker-compose up -d
```

## Cleanup

Remove all containers, volumes, and networks:

```bash
docker-compose down -v
```

## Master-Slave Replication

You can run multiple GeoDNS instances in master-slave mode for high availability.

### Example Docker Compose Setup

Create `docker-compose.replication.yml`:

```yaml
version: '3.8'

services:
  # Master DNS server
  geodns-master:
    build: .
    container_name: geodns-master
    ports:
      - "8053:53/udp"
      - "8053:53/tcp"
      - "18080:8080/tcp"
    volumes:
      - ./config.master.yaml:/app/config.yaml:ro
      - ./geoipdb:/app/geoipdb:ro
      - master-data:/data
    environment:
      - TZ=UTC
    restart: unless-stopped

  # Slave DNS server 1
  geodns-slave1:
    build: .
    container_name: geodns-slave1
    ports:
      - "8054:53/udp"
      - "8054:53/tcp"
      - "18081:8080/tcp"
    volumes:
      - ./config.slave1.yaml:/app/config.yaml:ro
      - ./geoipdb:/app/geoipdb:ro
      - slave1-data:/data
    environment:
      - TZ=UTC
    depends_on:
      - geodns-master
    restart: unless-stopped

  # Slave DNS server 2
  geodns-slave2:
    build: .
    container_name: geodns-slave2
    ports:
      - "8055:53/udp"
      - "8055:53/tcp"
      - "18082:8080/tcp"
    volumes:
      - ./config.slave2.yaml:/app/config.yaml:ro
      - ./geoipdb:/app/geoipdb:ro
      - slave2-data:/data
    environment:
      - TZ=UTC
    depends_on:
      - geodns-master
    restart: unless-stopped

volumes:
  master-data:
  slave1-data:
  slave2-data:
```

### Configuration Files

**config.master.yaml**:
```yaml
listen: "0.0.0.0:53"
api_tokens:                  # one named token per client/slave
  - name: "slave1"
    hash: "$2a$10$..."       # namedot --gen-token "<token used in slave1 replication.api_token>"
rest_listen: "0.0.0.0:8080"

db:
  driver: "sqlite"
  dsn: "file:/data/master.db?_foreign_keys=on"

replication:
  mode: "master"
```

**config.slave1.yaml**:
```yaml
listen: "0.0.0.0:53"
api_token: "your-secure-token"
rest_listen: "0.0.0.0:8080"

db:
  driver: "sqlite"
  dsn: "file:/data/slave1.db?_foreign_keys=on"

replication:
  mode: "slave"
  master_url: "http://geodns-master:8080"
  sync_interval_sec: 30
  api_token: "your-secure-token"
```

The slave sends its plain `replication.api_token`; the master matches it against its `api_tokens` hashes and shows the entry name in its API log (`token=slave1`). Add a separate entry per slave. See [README.md](README.md) for named tokens.

### Running Replication Setup

```bash
# Start all servers
docker-compose -f docker-compose.replication.yml up -d

# View logs
docker-compose -f docker-compose.replication.yml logs -f

# Check sync status
docker-compose -f docker-compose.replication.yml logs geodns-slave1 | grep -i sync

# Test DNS on master
dig @localhost -p 8053 example.com A

# Test DNS on slave (should return same data)
dig @localhost -p 8054 example.com A
```

### Load Balancing

Use a load balancer (nginx, haproxy, etc.) to distribute DNS queries across slaves:

**nginx example** (nginx.conf):
```nginx
stream {
    upstream dns_slaves {
        server geodns-slave1:53;
        server geodns-slave2:53;
    }

    server {
        listen 53 udp;
        proxy_pass dns_slaves;
    }
}
```

For more details on replication setup, see [REPLICATION.md](REPLICATION.md).
