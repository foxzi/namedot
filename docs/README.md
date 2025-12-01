namedot — Lightweight DNS server with REST + GeoDNS

Overview
- UDP/TCP DNS on :53
- Zones and records in DB (GORM: Postgres/MySQL/SQLite)
- REST API for zone management (+ JSON/BIND export, JSON import)
- HTTPS support with automatic certificate reloading
- IP-based access control (CIDR whitelist)
- Geo-aware responses (subnet/country/continent), ECS support
- Optional forwarder for cache-miss
- Simple in-memory TTL cache
- Master-Slave replication via REST API

Installation

### From Package Repository

#### Debian/Ubuntu (APT)
```bash
# Add repository
echo "deb [trusted=yes] https://foxzi.github.io/namedot/apt-repo stable main" | sudo tee /etc/apt/sources.list.d/namedot.list

# Update and install
sudo apt-get update
sudo apt-get install namedot

# Start service
sudo systemctl enable namedot
sudo systemctl start namedot
```

#### RHEL/CentOS/Fedora (YUM/DNF)
```bash
# Add repository
sudo curl -o /etc/yum.repos.d/namedot.repo https://foxzi.github.io/namedot/yum-repo/namedot/namedot.repo

# Install
sudo yum install namedot
# or
sudo dnf install namedot

# Start service
sudo systemctl enable namedot
sudo systemctl start namedot
```

#### Manual Download
Download DEB/RPM packages from [Releases](https://github.com/foxzi/namedot/releases)

#### Package Documentation
After installation, all documentation is available at `/usr/share/doc/namedot/`:
- README.md - Main documentation
- REPLICATION.md - Replication setup guide
- DOCKER.md - Docker deployment guide
- WEBADMIN.md - Web admin panel guide
- LICENSE - MIT License

### From Source

Requirements
- Go >= 1.23 (рекомендуется 1.24+)

Quick Start
1) Create `config.yaml` in repo root:

```
listen: ":53"
forwarder: "8.8.8.8"
enable_dnssec: false
api_token: "devtoken"
rest_listen: ":8080"
default_ttl: 300
soa:
  auto_on_missing: true
  primary: "ns1.{zone}"
  hostmaster: "hostmaster.{zone}"

db:
  driver: "sqlite"
  dsn: "file:namedot.db?_foreign_keys=on"

geoip:
  enabled: false
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300
  use_ecs: true

log:
  dns_verbose: true
  sql_debug: false  # Set to true to log all SQL queries (for debugging)
```

2) Build and run:
- `go build ./cmd/namedot`
- `sudo ./namedot` (DNS on :53 requires privileges or port redirect)

Command-line flags
- `-c, --config`: path to config file (YAML). Example: `./namedot --config config.yaml`
- `-t, --test`: validate config and exit. Example: `./namedot --test`
- `-p, --password`: generate bcrypt hash for admin password and exit. Example: `./namedot --password mySecret`
- `-g, --gen-token`: generate bcrypt hash for API token and exit. Example: `./namedot --gen-token myToken`
- `-v, --version`: print version and exit. Example: `./namedot --version`

Environment and precedence
- `SGDNS_CONFIG`: if set, used as config path when `--config` is not provided.
- Precedence: `--config` > `SGDNS_CONFIG` > `./config.yaml`.

Examples
```bash
# Run with explicit config
./namedot --config /etc/namedot/config.yaml

# Validate config and exit (no network listeners)
./namedot --test --config ./config.yaml

# Generate bcrypt hash for admin password
./namedot --password 'MyStr0ng!P@ssw0rd'

# Generate bcrypt hash for API token
./namedot --gen-token 'mySecureToken123'

# Print version
./namedot --version
```

REST API (Bearer devtoken)
- Base URL: `http://127.0.0.1:8080`
- Auth: header `Authorization: Bearer devtoken`

Examples (curl)
- Create zone
  - `curl -sS -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"example.com"}' http://127.0.0.1:8080/zones`
  - Capture ID (requires jq):
    - `ZID=$(curl -sS -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
       -d '{"name":"example.com"}' http://127.0.0.1:8080/zones | jq -r .id)`

- List zones
  - `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones`

- Get zone by name (returns single zone with RRSets or 404)
  - `curl -sS -H 'Authorization: Bearer devtoken' 'http://127.0.0.1:8080/zones?name=example.com'`
  - Name is normalized: `EXAMPLE.COM` → `example.com.`, trailing dot is added automatically
  - Get zone ID by name (requires jq):
    - `ZID=$(curl -sS -H 'Authorization: Bearer devtoken' 'http://127.0.0.1:8080/zones?name=example.com' | jq -r .id)`

- Add A rrset (www)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"A","ttl":300,"records":[{"data":"192.0.2.10"},{"data":"192.0.2.11"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Add AAAA rrset (www)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"AAAA","ttl":300,"records":[{"data":"2001:db8::10"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Add CNAME rrset (api → www.example.com.)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"api","type":"CNAME","ttl":300,"records":[{"data":"www.example.com."}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Add MX rrset (at zone apex)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"@","type":"MX","ttl":3600,"records":[{"data":"10 mail.example.com."}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Add TXT rrset (at zone apex)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"@","type":"TXT","ttl":300,"records":[{"data":"\"hello world\""}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Add Geo A rrset (svc) with selectors
  - Priority: subnet > asn > country > continent > default
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"svc","type":"A","ttl":60,
          "records":[
            {"data":"198.51.100.11","country":"US"},
            {"data":"198.51.100.12"},
            {"data":"198.51.100.13","subnet":"8.8.8.0/24"},
            {"data":"198.51.100.14","continent":"EU"},
            {"data":"198.51.100.15","asn":65001}
          ]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- List rrsets
  - `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/rrsets`

- Update rrset (PUT) by id (example: change TTL)
  - `curl -sS -X PUT -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"A","ttl":120,"records":[{"data":"192.0.2.10"},{"data":"192.0.2.11"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets/<RRSET_ID>`

- Delete rrset by id
  - `curl -sS -X DELETE -H 'Authorization: Bearer devtoken' \
     http://127.0.0.1:8080/zones/$ZID/rrsets/<RRSET_ID>`

- Export zone
  - JSON: `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/export?format=json`
  - BIND: `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/export?format=bind`

- Import zone
  - JSON (upsert): `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     --data-binary @zone.json "http://127.0.0.1:8080/zones/$ZID/import?format=json&mode=upsert"`
  - BIND (replace): `curl -sS -X POST -H 'Authorization: Bearer devtoken' --data-binary @zone.bind \
     "http://127.0.0.1:8080/zones/$ZID/import?format=bind&mode=replace"`

Replication
- Master-Slave replication via REST API with automatic sync
- See [REPLICATION.md](REPLICATION.md) for setup and configuration
- Example configs: [examples/config.master.yaml](examples/config.master.yaml) and [examples/config.slave.yaml](examples/config.slave.yaml)

Notes
- DNSSEC dynamic signing is not implemented yet. You can store DNSSEC records (DNSKEY/RRSIG/DS) in DB and serve them as-is when queried.
- Geo selection currently supports subnet/country/continent attributes on records. ASN requires GeoIP DB integration and is a TODO.

GeoIP with Auto-Download
- Enable in config:
  - `geoip.enabled: true`
  - `geoip.mmdb_path: <path to .mmdb file or directory>`
  - `geoip.use_ecs: true` to honor EDNS Client Subnet
  - `geoip.download_urls: [list of URLs]` for automatic MMDB downloads
  - `geoip.download_interval_sec: 86400` for periodic updates (24 hours)

Supported Formats:
- **MaxMind GeoLite2**: GeoLite2-Country.mmdb, GeoLite2-ASN.mmdb (via geoip2 library)
- **DBIP**: dbip-country-ipv4.mmdb, dbip-country-ipv6.mmdb, dbip-asn-*.mmdb (via maxminddb library)

The server automatically detects format and database type from file metadata. Only Country and ASN databases are supported (City databases are not needed for GeoDNS).

Configuration Example:
```yaml
geoip:
  enabled: true
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300           # Reload databases every 5 minutes
  use_ecs: true
  download_urls:
    - "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb"
    - "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb"
  download_interval_sec: 86400  # Download updates every 24 hours
```

Auto-Download Features:
- **Initial Download**: If `mmdb_path` directory is empty and `download_urls` are configured, files are downloaded on startup
- **Periodic Updates**: Files are re-downloaded automatically based on `download_interval_sec`
- **Hot Reload**: After download, databases are automatically reloaded without service restart
- **Docker-Friendly**: No cron needed - everything works automatically inside containers

DBIP Alternative (free, updated weekly):
```yaml
geoip:
  enabled: true
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300
  use_ecs: true
  download_urls:
    - "https://github.com/sapics/ip-location-db/raw/refs/heads/main/dbip-country-mmdb/dbip-country-ipv4.mmdb"
    - "https://github.com/sapics/ip-location-db/raw/refs/heads/main/dbip-country-mmdb/dbip-country-ipv6.mmdb"
  download_interval_sec: 604800  # Weekly updates
```

Logs: on startup and during downloads, server logs detailed progress including file sizes, success/failure status, and which GeoIP DBs are loaded.

BIND Import
- REST: `POST /zones/{id}/import?format=bind&mode=upsert|replace` with raw zone text in body.
- Export remains available via `GET /zones/{id}/export?format=bind`.

Testing
- Unit tests (modules):
  - BIND import/export: `go test ./internal/server/rest/zoneio -run TestImportBIND_And_ToBind -count=1`
- All tests: `go test ./...`
- Tests use in-memory SQLite, сетевые сервисы не поднимаются.

Integration Tests
- End-to-end (REST + DNS):
  - `go test ./internal/integration -run TestEndToEnd_DNS_and_REST -count=1`
  - Под капотом: поднимает DNS на 19053 и REST на 18089, создаёт зону и A-запись через REST, затем делает DNS-запрос и проверяет ответ, включая повторный запрос (кэш).
- GeoDNS (requires ./geoipdb with .mmdb files):
  - Subnet/ECS selection: `go test ./internal/integration -run TestGeoDNS_WithECS_USCountry -count=1`
  - Country/Continent/ASN selection (auto-skips if data missing): `go test ./internal/integration -run TestGeoDNS_WithECS_Country_Continent_ASN -count=1`

GeoIP Databases
- Repo ships small MMDBs for local tests in `./geoipdb`:
  - IPv4 localhost ranges: `city-localhost.mmdb`, `asn-localhost.mmdb` (127.0.1.0/24 → RU/EU/AS65001, 127.0.2.0/24 → GB/EU/AS65002).
  - IPv6 documentation ranges: `city-localhost6.mmdb`, `asn-localhost6.mmdb` (2001:db8:1::/64 and 2001:db8:2::/64) for ECS IPv6 tests.
- Verify with `mmdblookup`, e.g.:
  - `mmdblookup --file geoipdb/city-localhost.mmdb --ip 127.0.1.10`
  - `mmdblookup --file geoipdb/asn-localhost6.mmdb --ip 2001:db8:1::1`

Development
- Sync deps: `go mod tidy`
- Build only main: `go build ./cmd/namedot`
- Lint/format: follow project defaults (no external config added yet)

Makefile
- Build server: `make build`
- Run with config: `make run CFG=config.yaml`
- All tests: `make test-all`
- Unit + integration: `make test`
- GeoDNS tests: `make test-geo`

Building Packages

To build DEB/RPM packages locally:

1. Install nFPM:
```bash
echo 'deb [trusted=yes] https://repo.goreleaser.com/apt/ /' | sudo tee /etc/apt/sources.list.d/goreleaser.list
sudo apt-get update
sudo apt-get install -y nfpm
```

2. Build packages using Makefile (recommended):
```bash
# Build both DEB and RPM packages
# Version is automatically detected from git tags
make package

# Or build specific package type
make package-deb
make package-rpm

# Override version if needed
make package VERSION=0.2.0
```

The Makefile automatically:
- Builds the binary with correct version and flags
- Creates DEB and RPM packages
- Shows package sizes and locations

3. Manual build (alternative):
```bash
# Set version
export VERSION="0.1.0"

# Build binary
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -v \
  -ldflags "-X main.Version=$VERSION -s -w" \
  -o namedot \
  ./cmd/namedot

# Build packages
VERSION=$VERSION nfpm pkg --packager deb --config packaging/nfpm.yaml --target .
VERSION=$VERSION nfpm pkg --packager rpm --config packaging/nfpm.yaml --target .
```

Config Reference
- `soa.auto_on_missing`: if true, при отсутствии SOA в зоне автоматически создаётся дефолтная запись SOA:
  - MNAME: `soa.primary` (по умолчанию `ns1.<zone>.`)
  - RNAME: `soa.hostmaster` (по умолчанию `hostmaster.<zone>.`)
  - SERIAL: текущий Unix timestamp
  - Refresh/Retry/Expire/Minimum: 7200/3600/1209600/300
  - TTL: 3600
- `default_ttl`: TTL по умолчанию для записей/наборов, где TTL не указан (или равен 0). Используется в JSON/BIND импорте.

Security Features

### HTTPS Support
Enable HTTPS for REST API and Web Admin interface:

```yaml
rest_listen: ":8443"
tls_cert_file: "/etc/ssl/certs/server.crt"
tls_key_file: "/etc/ssl/private/server.key"
tls_reload_sec: 3600  # Reload certificate every hour (default: 3600)
```

Features:
- **Automatic Certificate Reloading**: Certificates are periodically reloaded from disk without service restart
- **Hot Reload**: Perfect for Let's Encrypt and other auto-renewed certificates
- **TLS 1.2+**: Minimum TLS version enforced for security
- **Backward Compatible**: If TLS not configured, server runs in HTTP mode

Example with Let's Encrypt:
```yaml
rest_listen: ":443"
tls_cert_file: "/etc/letsencrypt/live/dns.example.com/fullchain.pem"
tls_key_file: "/etc/letsencrypt/live/dns.example.com/privkey.pem"
tls_reload_sec: 3600
```

### IP Access Control
Restrict REST API access to specific IP ranges using CIDR notation:

```yaml
allowed_cidrs:
  - "127.0.0.0/8"      # Localhost
  - "10.0.0.0/8"       # Private network
  - "192.168.1.0/24"   # Local subnet
  - "2001:db8::/32"    # IPv6 network
```

Features:
- **IPv4 and IPv6**: Full support for both IP versions
- **Multiple Networks**: Specify multiple CIDR blocks
- **Secure by Default**: Empty list allows all (backward compatible)
- **Automatic Validation**: Invalid CIDRs are rejected at startup

If `allowed_cidrs` is not specified or empty, all IPs are allowed (default behavior).

---

# Русская версия / Russian Version

SmaillGeoDNS — Легковесный DNS-сервер с REST API + GeoDNS

## Обзор
- UDP/TCP DNS на порту :53
- Зоны и записи в БД (GORM: Postgres/MySQL/SQLite)
- REST API для управления зонами (+ JSON/BIND экспорт, JSON импорт)
- Поддержка HTTPS с автоматической перезагрузкой сертификатов
- Контроль доступа по IP (whitelist на основе CIDR)
- Geo-aware ответы (подсеть/страна/континент), поддержка ECS
- Опциональный форвардер при отсутствии записи в кеше
- Простой in-memory TTL кеш
- Master-Slave репликация через REST API

## Установка

### Из репозитория пакетов

#### Debian/Ubuntu (APT)
```bash
# Добавить репозиторий
echo "deb [trusted=yes] https://foxzi.github.io/namedot/apt-repo stable main" | sudo tee /etc/apt/sources.list.d/namedot.list

# Обновить и установить
sudo apt-get update
sudo apt-get install namedot

# Запустить сервис
sudo systemctl enable namedot
sudo systemctl start namedot
```

#### RHEL/CentOS/Fedora (YUM/DNF)
```bash
# Добавить репозиторий
sudo curl -o /etc/yum.repos.d/namedot.repo https://foxzi.github.io/namedot/yum-repo/namedot/namedot.repo

# Установить
sudo yum install namedot
# или
sudo dnf install namedot

# Запустить сервис
sudo systemctl enable namedot
sudo systemctl start namedot
```

#### Ручная загрузка
Скачайте DEB/RPM пакеты из [Releases](https://github.com/foxzi/namedot/releases)

#### Документация в пакете
После установки вся документация доступна в `/usr/share/doc/namedot/`:
- README.md - Основная документация
- REPLICATION.md - Руководство по репликации
- DOCKER.md - Руководство по Docker
- WEBADMIN.md - Руководство по веб-панели
- LICENSE - Лицензия MIT

### Из исходников

## Требования
- Go >= 1.23 (рекомендуется 1.24+)

## Быстрый старт

1) Создайте `config.yaml` в корне репозитория:

```yaml
listen: ":53"
forwarder: "8.8.8.8"
enable_dnssec: false
api_token: "devtoken"
rest_listen: ":8080"
default_ttl: 300
soa:
  auto_on_missing: true
  primary: "ns1.{zone}"
  hostmaster: "hostmaster.{zone}"

db:
  driver: "sqlite"
  dsn: "file:namedot.db?_foreign_keys=on"

geoip:
  enabled: false
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300
  use_ecs: true

log:
  dns_verbose: true
  sql_debug: false  # Установите true для логирования SQL запросов (отладка)
```

2) Сборка и запуск:
- `go build ./cmd/namedot`
- `sudo ./namedot` (DNS на :53 требует привилегий или проброса порта)

CLI флаги
- `-c, --config`: путь к конфигу (YAML). Пример: `./namedot --config config.yaml`
- `-t, --test`: проверить конфиг и выйти. Пример: `./namedot --test`
- `-p, --password`: сгенерировать bcrypt-хеш для пароля админки и выйти. Пример: `./namedot --password mySecret`
- `-g, --gen-token`: сгенерировать bcrypt-хеш для API токена и выйти. Пример: `./namedot --gen-token myToken`
- `-v, --version`: вывести версию и выйти. Пример: `./namedot --version`

Окружение и приоритеты
- `SGDNS_CONFIG`: если установлен, используется как путь к конфигу при отсутствии `--config`.
- Приоритет: `--config` > `SGDNS_CONFIG` > `./config.yaml`.

Примеры
```bash
# Запуск с явным конфигом
./namedot --config /etc/namedot/config.yaml

# Проверка конфига и выход (без запуска сетевых слушателей)
./namedot --test --config ./config.yaml

# Генерация bcrypt-хеша для пароля админки
./namedot --password 'MyStr0ng!P@ssw0rd'

# Генерация bcrypt-хеша для API токена
./namedot --gen-token 'mySecureToken123'

# Вывести версию
./namedot --version
```

## REST API (Bearer devtoken)
- Базовый URL: `http://127.0.0.1:8080`
- Аутентификация: заголовок `Authorization: Bearer devtoken`

Примеры (curl)
- Создать зону
  - `curl -sS -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"example.com"}' http://127.0.0.1:8080/zones`
  - Сохранить ID (требуется jq):
    - `ZID=$(curl -sS -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
       -d '{"name":"example.com"}' http://127.0.0.1:8080/zones | jq -r .id)`

- Список зон
  - `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones`

- Получить зону по имени (возвращает одну зону с RRSets или 404)
  - `curl -sS -H 'Authorization: Bearer devtoken' 'http://127.0.0.1:8080/zones?name=example.com'`
  - Имя нормализуется: `EXAMPLE.COM` → `example.com.`, точка в конце добавляется автоматически
  - Получить ID зоны по имени (требуется jq):
    - `ZID=$(curl -sS -H 'Authorization: Bearer devtoken' 'http://127.0.0.1:8080/zones?name=example.com' | jq -r .id)`

- Добавить A rrset (www)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"A","ttl":300,"records":[{"data":"192.0.2.10"},{"data":"192.0.2.11"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Добавить AAAA rrset (www)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"AAAA","ttl":300,"records":[{"data":"2001:db8::10"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Добавить CNAME rrset (api → www.example.com.)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"api","type":"CNAME","ttl":300,"records":[{"data":"www.example.com."}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Добавить MX rrset (на корне зоны)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"@","type":"MX","ttl":3600,"records":[{"data":"10 mail.example.com."}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Добавить TXT rrset (на корне зоны)
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"@","type":"TXT","ttl":300,"records":[{"data":"\"hello world\""}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Добавить Geo A rrset (svc) с селекторами
  - Приоритет: subnet > asn > country > continent > default
  - `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"svc","type":"A","ttl":60,
          "records":[
            {"data":"198.51.100.11","country":"US"},
            {"data":"198.51.100.12"},
            {"data":"198.51.100.13","subnet":"8.8.8.0/24"},
            {"data":"198.51.100.14","continent":"EU"},
            {"data":"198.51.100.15","asn":65001}
          ]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets`

- Список rrset
  - `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/rrsets`

- Обновить rrset (PUT) по id (например, сменить TTL)
  - `curl -sS -X PUT -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     -d '{"name":"www","type":"A","ttl":120,"records":[{"data":"192.0.2.10"},{"data":"192.0.2.11"}]}' \
     http://127.0.0.1:8080/zones/$ZID/rrsets/<RRSET_ID>`

- Удалить rrset по id
  - `curl -sS -X DELETE -H 'Authorization: Bearer devtoken' \
     http://127.0.0.1:8080/zones/$ZID/rrsets/<RRSET_ID>`

- Экспорт зоны
  - JSON: `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/export?format=json`
  - BIND: `curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:8080/zones/$ZID/export?format=bind`

- Импорт зоны
  - JSON (upsert): `curl -sS -X POST -H 'Authorization: Bearer devtoken' -H 'Content-Type: application/json' \
     --data-binary @zone.json "http://127.0.0.1:8080/zones/$ZID/import?format=json&mode=upsert"`
  - BIND (replace): `curl -sS -X POST -H 'Authorization: Bearer devtoken' --data-binary @zone.bind \
     "http://127.0.0.1:8080/zones/$ZID/import?format=bind&mode=replace"`

## Репликация
- Master-Slave репликация через REST API с автоматической синхронизацией
- См. [REPLICATION.md](REPLICATION.md) для настройки и конфигурации
- Примеры конфигов: [examples/config.master.yaml](examples/config.master.yaml) и [examples/config.slave.yaml](examples/config.slave.yaml)

## Примечания
- Динамическая подпись DNSSEC пока не реализована. Вы можете хранить DNSSEC-записи (DNSKEY/RRSIG/DS) в БД и отдавать их как есть при запросе.
- Geo-выбор в настоящее время поддерживает атрибуты subnet/country/continent на записях. ASN требует интеграции GeoIP DB и находится в TODO.

## GeoIP с автоматическим скачиванием
- Включить в конфиге:
  - `geoip.enabled: true`
  - `geoip.mmdb_path: <путь к .mmdb файлу или директории>`
  - `geoip.use_ecs: true` для учета EDNS Client Subnet
  - `geoip.download_urls: [список URL]` для автоматического скачивания MMDB
  - `geoip.download_interval_sec: 86400` для периодических обновлений (24 часа)

Поддерживаемые форматы:
- **MaxMind GeoLite2**: GeoLite2-Country.mmdb, GeoLite2-ASN.mmdb (через библиотеку geoip2)
- **DBIP**: dbip-country-ipv4.mmdb, dbip-country-ipv6.mmdb, dbip-asn-*.mmdb (через библиотеку maxminddb)

Сервер автоматически определяет формат и тип базы данных по метаданным файла. Поддерживаются только Country и ASN базы (City базы не нужны для GeoDNS).

Пример конфигурации:
```yaml
geoip:
  enabled: true
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300           # Перезагрузка баз каждые 5 минут
  use_ecs: true
  download_urls:
    - "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb"
    - "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb"
  download_interval_sec: 86400  # Скачивание обновлений каждые 24 часа
```

Возможности автоскачивания:
- **Начальное скачивание**: Если директория `mmdb_path` пуста и настроены `download_urls`, файлы скачиваются при запуске
- **Периодические обновления**: Файлы автоматически перекачиваются согласно `download_interval_sec`
- **Горячая перезагрузка**: После скачивания базы автоматически перезагружаются без перезапуска сервиса
- **Docker-совместимость**: Не нужен cron - всё работает автоматически внутри контейнеров

Альтернатива DBIP (бесплатная, обновляется еженедельно):
```yaml
geoip:
  enabled: true
  mmdb_path: "/var/lib/namedot/geoipdb"
  reload_sec: 300
  use_ecs: true
  download_urls:
    - "https://github.com/sapics/ip-location-db/raw/refs/heads/main/dbip-country-mmdb/dbip-country-ipv4.mmdb"
    - "https://github.com/sapics/ip-location-db/raw/refs/heads/main/dbip-country-mmdb/dbip-country-ipv6.mmdb"
  download_interval_sec: 604800  # Еженедельные обновления
```

Логи: при запуске и во время скачивания сервер выводит детальный прогресс, включая размеры файлов, статус успеха/неудачи и информацию о загруженных GeoIP базах.

## BIND импорт
- REST: `POST /zones/{id}/import?format=bind&mode=upsert|replace` с сырым текстом зоны в теле.
- Экспорт остаётся доступен через `GET /zones/{id}/export?format=bind`.

## Тестирование
- Модульные тесты (модули):
  - BIND импорт/экспорт: `go test ./internal/server/rest/zoneio -run TestImportBIND_And_ToBind -count=1`
- Все тесты: `go test ./...`
- Тесты используют in-memory SQLite, сетевые сервисы не поднимаются.

## Интеграционные тесты
- End-to-end (REST + DNS):
  - `go test ./internal/integration -run TestEndToEnd_DNS_and_REST -count=1`
  - Под капотом: поднимает DNS на 19053 и REST на 18089, создаёт зону и A-запись через REST, затем делает DNS-запрос и проверяет ответ, включая повторный запрос (кэш).
- GeoDNS (требует ./geoipdb с .mmdb файлами):
  - Subnet/ECS выбор: `go test ./internal/integration -run TestGeoDNS_WithECS_USCountry -count=1`
  - Country/Continent/ASN выбор (авто-пропуск при отсутствии данных): `go test ./internal/integration -run TestGeoDNS_WithECS_Country_Continent_ASN -count=1`

## GeoIP базы данных
- Репозиторий содержит небольшие MMDB для локальных тестов в `./geoipdb`:
  - IPv4 localhost диапазоны: `city-localhost.mmdb`, `asn-localhost.mmdb` (127.0.1.0/24 → RU/EU/AS65001, 127.0.2.0/24 → GB/EU/AS65002).
  - IPv6 документационные диапазоны: `city-localhost6.mmdb`, `asn-localhost6.mmdb` (2001:db8:1::/64 и 2001:db8:2::/64) для ECS IPv6 тестов.
- Проверка с помощью `mmdblookup`, например:
  - `mmdblookup --file geoipdb/city-localhost.mmdb --ip 127.0.1.10`
  - `mmdblookup --file geoipdb/asn-localhost6.mmdb --ip 2001:db8:1::1`

## Разработка
- Синхронизация зависимостей: `go mod tidy`
- Сборка только main: `go build ./cmd/namedot`
- Линтинг/форматирование: следуйте дефолтным настройкам проекта (внешний конфиг пока не добавлен)

## Makefile
- Сборка сервера: `make build`
- Запуск с конфигом: `make run CFG=config.yaml`
- Все тесты: `make test-all`
- Модульные + интеграционные: `make test`
- GeoDNS тесты: `make test-geo`

## Сборка пакетов

Для локальной сборки DEB/RPM пакетов:

1. Установить nFPM:
```bash
echo 'deb [trusted=yes] https://repo.goreleaser.com/apt/ /' | sudo tee /etc/apt/sources.list.d/goreleaser.list
sudo apt-get update
sudo apt-get install -y nfpm
```

2. Собрать пакеты через Makefile (рекомендуется):
```bash
# Собрать оба пакета DEB и RPM
# Версия автоматически определяется из git tags
make package

# Или собрать конкретный тип пакета
make package-deb
make package-rpm

# Переопределить версию при необходимости
make package VERSION=0.2.0
```

Makefile автоматически:
- Собирает бинарник с правильной версией и флагами
- Создаёт DEB и RPM пакеты
- Показывает размеры пакетов и их расположение

3. Ручная сборка (альтернатива):
```bash
# Установить версию
export VERSION="0.1.0"

# Собрать бинарник
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -v \
  -ldflags "-X main.Version=$VERSION -s -w" \
  -o namedot \
  ./cmd/namedot

# Собрать пакеты
VERSION=$VERSION nfpm pkg --packager deb --config packaging/nfpm.yaml --target .
VERSION=$VERSION nfpm pkg --packager rpm --config packaging/nfpm.yaml --target .
```

## Справка по конфигурации
- `soa.auto_on_missing`: если true, при отсутствии SOA в зоне автоматически создаётся дефолтная запись SOA:
  - MNAME: `soa.primary` (по умолчанию `ns1.<zone>.`)
  - RNAME: `soa.hostmaster` (по умолчанию `hostmaster.<zone>.`)
  - SERIAL: текущий Unix timestamp
  - Refresh/Retry/Expire/Minimum: 7200/3600/1209600/300
  - TTL: 3600
- `default_ttl`: TTL по умолчанию для записей/наборов, где TTL не указан (или равен 0). Используется в JSON/BIND импорте.

## Функции безопасности

### Поддержка HTTPS
Включение HTTPS для REST API и веб-админки:

```yaml
rest_listen: ":8443"
tls_cert_file: "/etc/ssl/certs/server.crt"
tls_key_file: "/etc/ssl/private/server.key"
tls_reload_sec: 3600  # Перезагрузка сертификата каждый час (по умолчанию: 3600)
```

Возможности:
- **Автоматическая перезагрузка сертификатов**: Сертификаты периодически перечитываются с диска без перезапуска сервиса
- **Горячая перезагрузка**: Идеально для Let's Encrypt и других автообновляемых сертификатов
- **TLS 1.2+**: Минимальная версия TLS для безопасности
- **Обратная совместимость**: Если TLS не настроен, сервер работает в режиме HTTP

Пример с Let's Encrypt:
```yaml
rest_listen: ":443"
tls_cert_file: "/etc/letsencrypt/live/dns.example.com/fullchain.pem"
tls_key_file: "/etc/letsencrypt/live/dns.example.com/privkey.pem"
tls_reload_sec: 3600
```

### Контроль доступа по IP
Ограничение доступа к REST API по определённым IP-диапазонам в нотации CIDR:

```yaml
allowed_cidrs:
  - "127.0.0.0/8"      # Localhost
  - "10.0.0.0/8"       # Приватная сеть
  - "192.168.1.0/24"   # Локальная подсеть
  - "2001:db8::/32"    # IPv6 сеть
```

Возможности:
- **IPv4 и IPv6**: Полная поддержка обеих версий IP
- **Множественные сети**: Можно указать несколько CIDR блоков
- **Безопасно по умолчанию**: Пустой список разрешает всем (обратная совместимость)
- **Автоматическая валидация**: Неправильные CIDR отклоняются при запуске

Если `allowed_cidrs` не указан или пуст, доступ разрешён всем IP (поведение по умолчанию).

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
