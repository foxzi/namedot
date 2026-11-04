# Testing Documentation

## Overview
This document describes the test coverage for namedot project, particularly focusing on REST API authentication and replication functionality.

## Test Structure

### REST Authentication Tests (`internal/server/rest/auth_test.go`)

Comprehensive unit tests for the REST API authentication middleware.

#### Test Coverage

**1. Token Hash Authentication**
- Valid bcrypt-hashed token authentication
- Invalid token rejection
- Missing token handling

**2. Plain Text Token Authentication** (deprecated)
- Valid plain text token authentication
- Invalid token rejection
- Fallback behavior when hash is not configured

**3. No Authentication Configured**
- Permissive behavior (allows all requests)
- Security recommendation documentation
- Edge cases with tokens when auth is disabled

**4. Security Edge Cases**
- Bearer prefix handling (case-sensitive)
- Empty token handling
- Token without Bearer prefix
- Both hash and plain token configured

**5. Security Recommendations**
The tests document important security considerations:
- When no authentication is configured, the API allows all requests
- Recommended: Require explicit configuration or fail-safe defaults
- Tests provide documentation for future security improvements

#### Running Authentication Tests

```bash
# Run all authentication tests
go test -v ./internal/server/rest -run TestAuth

# Run with coverage
go test -cover ./internal/server/rest -run TestAuth
```

### Replication Tests (`internal/server/rest/replication_test.go`)

Table-driven tests for master-slave replication functionality covering export and import operations.

#### Test Coverage

**1. Export Tests (`TestSyncExport`)**
- Empty database export
- Single zone with records export
- Multiple zones with records export
- Templates with records export
- Combined zones and templates export
- Proper preloading of relationships (RRSets, Records)

**2. Import Tests (`TestSyncImport`)**
- New zone creation
- Conflicting zone handling (replaces old records)
- New template creation
- Conflicting template handling (updates and replaces)
- Multiple zones and templates
- Geo-aware template records (Country, Continent, ASN, Subnet)
- Invalid JSON rejection

**3. Conflict Resolution**
Tests verify that import properly handles existing data:
- Existing zones: Old RRSets and Records are hard-deleted (Unscoped)
- Existing templates: Description is updated, old records are hard-deleted
- No soft-delete markers left behind

**4. Data Integrity**
- Transaction rollback on errors (documented limitation with SQLite)
- Hard delete verification (no soft-deleted records remain)
- Geo-attributes preservation
- Record relationship integrity

**5. Edge Cases**
- Empty import data
- Invalid JSON payloads
- Missing required fields
- Database constraints

#### Running Replication Tests

```bash
# Run all replication tests
go test -v ./internal/server/rest -run TestSync

# Run with coverage
go test -cover ./internal/server/rest -run TestSync

# Run all REST tests
go test -v ./internal/server/rest
```

### Local Cluster Test (`internal/integration/cluster_test.go`)

Builds the `namedot` binary and runs 5 real processes on free localhost ports:

- `master-a` with zones `example.com` and child zone `sub.example.com`, slaves `slave-a1`, `slave-a2`
- `master-b` with zones `example.org` (geo records by subnet) and `example.net`, slave `slave-b1`

Checks:
- masters answer A, AAAA, MX, TXT, CNAME, wildcard, child zone, NXDOMAIN/NODATA with SOA, geo selection by ECS
- slaves give the same answers as their master and know nothing about zones of the other cluster
- update of an rrset, a new zone and a deleted zone on the master reach the slaves (sync every second)
- a slave keeps serving its data after the master stops

Run: `make test-cluster` (skipped with `go test -short`). Node logs are printed when the test fails.

## Test Statistics

```bash
# Run both authentication and replication tests
go test -v ./internal/server/rest -run "TestAuth|TestSync"

# Coverage: ~26% of internal/server/rest package
go test -cover ./internal/server/rest -run "TestAuth|TestSync"
```

## Test Helpers

### `setupTestServer(t *testing.T, cfg *config.Config)`
Sets up an in-memory SQLite database with proper schema migration for testing.

### `setupTestDB(t *testing.T)`
Creates and migrates an in-memory database for replication tests.

### `stringPtr(s string) *string`
Helper function for creating string pointers (needed for geo-aware fields).

## Important Notes

### SQLite Limitations
The tests use SQLite in-memory databases which have some limitations:
- Transaction rollback behavior differs from PostgreSQL/MySQL
- Some constraint violations may not trigger in SQLite
- Production databases (Postgres/MySQL) will have more comprehensive constraint checking

### Security Considerations
1. **Authentication Tests** document the current permissive behavior when no auth is configured
2. Tests include recommendations for improving security defaults
3. Edge cases are documented for awareness

### Hard Delete vs Soft Delete
- Tests verify that replication uses hard delete (Unscoped)
- Old records are completely removed, not just marked as deleted
- This is important for data consistency in master-slave replication

## Future Improvements

1. **Authentication**
   - Add tests for IP-based access control (CIDR whitelist)
   - Test certificate-based authentication
   - Test rate limiting

2. **Replication**
   - Test concurrent import/export operations
   - Test partial sync failures and recovery
   - Test large dataset performance
   - Add integration tests with real master-slave setup

3. **Integration Tests**
   - End-to-end replication tests
   - Multi-zone conflict resolution
   - Network failure scenarios
   - Sync timing and scheduling

## Related Documentation

- [Security Improvements](./SECURITY_IMPROVEMENTS.md) - CSRF and cookie security
- [Replication Guide](./REPLICATION.md) - Master-slave setup and configuration
- [Main README](./README.md) - Complete project documentation
