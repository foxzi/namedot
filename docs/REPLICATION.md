# Репликация Master-Slave

SmailGeoDNS поддерживает репликацию данных между мастер и слейв серверами через REST API.

## Особенности

- **Идентичный код**: мастер и слейв работают на одном и том же бинарнике
- **Настройка через конфиг**: режим работы (master/slave) настраивается в config.yaml
- **Автоматическая синхронизация**: слейв автоматически запрашивает данные у мастера с заданным интервалом
- **Полная синхронизация**: синхронизируются все зоны, записи и шаблоны

## Конфигурация

### Master сервер

```yaml
replication:
  mode: "master"
```

Мастер-сервер:
- Принимает запросы на `/sync/export` и возвращает все данные
- Разрешает изменения через REST API и веб-интерфейс
- Является источником данных для слейв-серверов

Рекомендуется выдавать каждому слейву отдельный именованный токен - запись в `api_tokens` на мастере (например, `name: "slave-ns1"`, `name: "slave-ns2"`): имя видно в логах API мастера, а токен одного слейва можно отозвать, не затрагивая остальные. Подробнее: [README.md](README.md).

```yaml
# на мастере
api_tokens:
  - name: "slave-ns1"
    hash: "$2a$10$..."   # namedot --gen-token "$(openssl rand -hex 32)"
```

### Slave сервер

```yaml
replication:
  mode: "slave"
  master_url: "http://master-server:8080"
  sync_interval_sec: 60
  api_token: "your-secure-token-here"
```

Параметры слейва:
- **mode**: должен быть `"slave"`
- **master_url**: URL мастер-сервера (с протоколом и портом)
- **sync_interval_sec**: интервал синхронизации в секундах (по умолчанию 60)
- **api_token**: токен (в открытом виде) для авторизации на мастер-сервере; на мастере должен соответствовать одной из записей `api_tokens`

**Важно**: При включении режима `slave` автоматически отключаются:
- `admin.enabled: false` - веб-интерфейс администратора
- `update.enabled: false` - DNS обновления через API

Это защищает слейв от прямых изменений и обеспечивает read-only режим.

## Пример использования

### 1. Запуск мастера

```bash
# Создайте конфиг для мастера
cp examples/config.master.yaml config.yaml

# Запустите мастер
./namedot
```

Мастер будет слушать на порту 8080 и готов принимать запросы репликации.

### 2. Запуск слейва

```bash
# Создайте конфиг для слейва
cp examples/config.slave.yaml config.yaml

# Отредактируйте master_url на реальный адрес мастера
# Например: master_url: "http://192.168.1.100:8080"

# Запустите слейв
./namedot
```

Слейв автоматически начнет синхронизацию с мастером каждые 60 секунд (или с интервалом, указанным в конфиге).

### 3. Проверка работы

Логи мастера:
```
Master mode enabled: ready to serve replication data
```

Логи слейва:
```
Slave mode enabled: syncing from http://master-server:8080 every 60 seconds
Starting periodic sync every 1m0s
Starting sync from master...
Fetched 5 zones and 3 templates from master
Sync completed successfully
```

## API эндпоинты репликации

### GET /sync/export

Возвращает все данные для репликации (зоны и шаблоны).

**Требуется авторизация**: Bearer token

**Ответ**:
```json
{
  "zones": [
    {
      "id": 1,
      "name": "example.com.",
      "rrsets": [
        {
          "id": 1,
          "name": "example.com.",
          "type": "A",
          "ttl": 300,
          "records": [
            {
              "data": "192.168.1.1",
              "country": "US"
            }
          ]
        }
      ]
    }
  ],
  "templates": [...]
}
```

### POST /sync/import

Импортирует данные на слейв-сервер.

**Требуется авторизация**: Bearer token

**Запрос**: тот же формат что и ответ `/sync/export`

**Ответ**:
```json
{
  "status": "ok",
  "zones": 5,
  "templates": 3
}
```

## Безопасность

1. **Используйте надежные токены**:
   ```bash
   # Генерация токена
   openssl rand -base64 32
   ```

2. **HTTPS в продакшене**: используйте reverse proxy (nginx, traefik) с TLS

3. **Firewall**: ограничьте доступ к REST API только с доверенных IP

4. **Модификации на слейве**: автоматически отключаются при `mode: "slave"`
   - `update.enabled` → автоматически устанавливается в `false`
   - `admin.enabled` → автоматически устанавливается в `false`

## Мониторинг

Проверка здоровья:
```bash
curl http://slave-server:8080/health
```

Проверка синхронизации:
```bash
# На мастере
curl -H "Authorization: Bearer your-token" http://master:8080/zones

# На слейве (должны быть те же данные)
curl -H "Authorization: Bearer your-token" http://slave:8080/zones
```

## Troubleshooting

### Слейв не синхронизируется

1. Проверьте логи слейва на наличие ошибок
2. Проверьте доступность мастера: `curl http://master:8080/health`
3. Проверьте правильность токена
4. Проверьте firewall и сетевую доступность

### Ошибка авторизации

```
master returned status 401: Unauthorized
```

Решение: убедитесь что `replication.api_token` на слейве совпадает с одним из токенов на мастере (запись `api_tokens`, `api_token_hash` или `api_token`). В логе мастера успешные запросы синхронизации выглядят как `API GET /sync/export 200 325ms from 1.2.3.4 token=replication` - имя токена помогает понять, какой слейв (и с каким токеном) обращается; `token=-` означает, что запрос не авторизован. Если слейв получает 403, проверьте `allowed_cidrs` на мастере.

### Медленная синхронизация

Увеличьте `sync_interval_sec` если у вас большой объем данных и редкие изменения.

Уменьшите `sync_interval_sec` если нужна более частая синхронизация (минимум рекомендуется 10 секунд).

## Архитектура

```
┌─────────────┐
│   Master    │
│   Server    │
│             │
│ - REST API  │
│ - Web UI    │
│ - DNS       │
└──────┬──────┘
       │
       │ HTTP GET /sync/export
       │ (каждые N секунд)
       │
       ▼
┌─────────────┐
│   Slave     │
│   Server    │
│             │
│ - DNS only  │
│ (read-only) │
└─────────────┘
```

Процесс синхронизации:
1. Слейв по таймеру отправляет GET /sync/export на мастер
2. Мастер возвращает все зоны и шаблоны с записями
3. Слейв вызывает локальный POST /sync/import
4. /sync/import обновляет локальную БД в транзакции
5. DNS сервер на слейве использует обновленные данные

## Ограничения

- **Односторонняя репликация**: только master → slave
- **Полная синхронизация**: при каждом sync передаются все данные
- **Нет conflict resolution**: слейв всегда перезаписывает свои данные данными мастера
- **Нет каскадной репликации**: слейв не может быть мастером для других слейвов

---

# English Version

# Master-Slave Replication

SmailGeoDNS supports data replication between master and slave servers via REST API.

## Features

- **Identical code**: master and slave run on the same binary
- **Configuration-based**: mode (master/slave) is configured in config.yaml
- **Automatic synchronization**: slave automatically requests data from master at configurable intervals
- **Full synchronization**: all zones, records, and templates are synchronized

## Configuration

### Master Server

```yaml
replication:
  mode: "master"
```

Master server:
- Accepts requests to `/sync/export` and returns all data
- Allows modifications via REST API and web interface
- Acts as the data source for slave servers

Use a dedicated named token per slave: an `api_tokens` entry on the master (for example `name: "slave-ns1"`, `name: "slave-ns2"`). The name is shown in the master's API logs, and one slave's token can be revoked without affecting the others. See [README.md](README.md) for details.

```yaml
# on the master
api_tokens:
  - name: "slave-ns1"
    hash: "$2a$10$..."   # namedot --gen-token "$(openssl rand -hex 32)"
```

### Slave Server

```yaml
replication:
  mode: "slave"
  master_url: "http://master-server:8080"
  sync_interval_sec: 60
  api_token: "your-secure-token-here"
```

Slave parameters:
- **mode**: must be `"slave"`
- **master_url**: master server URL (with protocol and port)
- **sync_interval_sec**: synchronization interval in seconds (default: 60)
- **api_token**: plain token for master server authentication; the master must have a matching entry in `api_tokens`

**Important**: When `slave` mode is enabled, the following are automatically disabled:
- `admin.enabled: false` - web admin interface
- `update.enabled: false` - DNS updates via API

This protects the slave from direct modifications and ensures read-only mode.

## Usage Example

### 1. Starting Master

```bash
# Create master config
cp examples/config.master.yaml config.yaml

# Start master
./namedot
```

Master will listen on port 8080 and be ready to accept replication requests.

### 2. Starting Slave

```bash
# Create slave config
cp examples/config.slave.yaml config.yaml

# Edit master_url to point to actual master server
# Example: master_url: "http://192.168.1.100:8080"

# Start slave
./namedot
```

Slave will automatically start synchronizing with master every 60 seconds (or at the interval specified in config).

### 3. Verification

Master logs:
```
Master mode enabled: ready to serve replication data
```

Slave logs:
```
Slave mode enabled: syncing from http://master-server:8080 every 60 seconds
Starting periodic sync every 1m0s
Starting sync from master...
Fetched 5 zones and 3 templates from master
Sync completed successfully
```

## Replication API Endpoints

### GET /sync/export

Returns all data for replication (zones and templates).

**Authentication required**: Bearer token

**Response**:
```json
{
  "zones": [
    {
      "id": 1,
      "name": "example.com.",
      "rrsets": [
        {
          "id": 1,
          "name": "example.com.",
          "type": "A",
          "ttl": 300,
          "records": [
            {
              "data": "192.168.1.1",
              "country": "US"
            }
          ]
        }
      ]
    }
  ],
  "templates": [...]
}
```

### POST /sync/import

Imports data to slave server.

**Authentication required**: Bearer token

**Request**: same format as `/sync/export` response

**Response**:
```json
{
  "status": "ok",
  "zones": 5,
  "templates": 3
}
```

## Security

1. **Use strong tokens**:
   ```bash
   # Generate token
   openssl rand -base64 32
   ```

2. **HTTPS in production**: use reverse proxy (nginx, traefik) with TLS

3. **Firewall**: restrict REST API access to trusted IPs only

4. **Slave modifications**: automatically disabled when `mode: "slave"`
   - `update.enabled` → automatically set to `false`
   - `admin.enabled` → automatically set to `false`

## Monitoring

Health check:
```bash
curl http://slave-server:8080/health
```

Synchronization check:
```bash
# On master
curl -H "Authorization: Bearer your-token" http://master:8080/zones

# On slave (should return same data)
curl -H "Authorization: Bearer your-token" http://slave:8080/zones
```

## Troubleshooting

### Slave not synchronizing

1. Check slave logs for errors
2. Verify master availability: `curl http://master:8080/health`
3. Verify token is correct
4. Check firewall and network connectivity

### Authorization error

```
master returned status 401: Unauthorized
```

Solution: ensure `replication.api_token` on the slave matches one of the tokens on the master (an `api_tokens` entry, `api_token_hash` or `api_token`). In the master log, successful sync requests look like `API GET /sync/export 200 325ms from 1.2.3.4 token=replication` - the token name shows which slave (and which token) is calling; `token=-` means the request was not authenticated. If the slave gets 403, check `allowed_cidrs` on the master.

### Slow synchronization

Increase `sync_interval_sec` if you have large amounts of data and infrequent changes.

Decrease `sync_interval_sec` if you need more frequent synchronization (minimum recommended: 10 seconds).

## Architecture

```
┌─────────────┐
│   Master    │
│   Server    │
│             │
│ - REST API  │
│ - Web UI    │
│ - DNS       │
└──────┬──────┘
       │
       │ HTTP GET /sync/export
       │ (every N seconds)
       │
       ▼
┌─────────────┐
│   Slave     │
│   Server    │
│             │
│ - DNS only  │
│ (read-only) │
└─────────────┘
```

Synchronization process:
1. Slave sends GET /sync/export to master on timer
2. Master returns all zones and templates with records
3. Slave calls local POST /sync/import
4. /sync/import updates local DB in transaction
5. DNS server on slave uses updated data

## Limitations

- **One-way replication**: master → slave only
- **Full synchronization**: all data is transferred on each sync
- **No conflict resolution**: slave always overwrites its data with master's data
- **No cascading replication**: slave cannot be a master for other slaves
