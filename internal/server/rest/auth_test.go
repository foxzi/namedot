package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"namedot/internal/config"
	dbm "namedot/internal/db"
)

// Aliases for cleaner code
type (
	Zone           = dbm.Zone
	RRSet          = dbm.RRSet
	RData          = dbm.RData
	Template       = dbm.Template
	TemplateRecord = dbm.TemplateRecord
)

func setupTestServer(t *testing.T, cfg *config.Config) (*Server, *gin.Engine) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	// Auto-migrate tables
	if err := db.AutoMigrate(
		&Zone{},
		&RRSet{},
		&RData{},
		&Template{},
		&TemplateRecord{},
	); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	mockDNSServer := &mockDNSServer{}
	server := NewServer(cfg, db, mockDNSServer)

	return server, server.r
}

type mockDNSServer struct {
	invalidateCalled bool
}

func (m *mockDNSServer) InvalidateZoneCache() {
	m.invalidateCalled = true
}

func TestAuthMiddleware(t *testing.T) {
	// Generate bcrypt hash for testing
	hashedToken, err := bcrypt.GenerateFromPassword([]byte("test-token-hash"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	tests := []struct {
		name           string
		apiToken       string
		apiTokenHash   string
		authHeader     string
		expectedStatus int
		description    string
	}{
		{
			name:           "valid hashed token",
			apiToken:       "",
			apiTokenHash:   string(hashedToken),
			authHeader:     "Bearer test-token-hash",
			expectedStatus: http.StatusOK,
			description:    "Should authenticate with valid bcrypt hashed token",
		},
		{
			name:           "invalid hashed token",
			apiToken:       "",
			apiTokenHash:   string(hashedToken),
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject invalid token when hash is configured",
		},
		{
			name:           "valid plain token",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "Bearer plain-token-123",
			expectedStatus: http.StatusOK,
			description:    "Should authenticate with valid plain text token (deprecated)",
		},
		{
			name:           "invalid plain token",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject invalid plain text token",
		},
		{
			name:           "missing token when hash configured",
			apiToken:       "",
			apiTokenHash:   string(hashedToken),
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject request without token when hash is configured",
		},
		{
			name:           "missing token when plain configured",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject request without token when plain token is configured",
		},
		{
			name:           "no authentication configured - rejects",
			apiToken:       "",
			apiTokenHash:   "",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject request when no authentication is configured",
		},
		{
			name:           "no authentication configured with token - rejects",
			apiToken:       "",
			apiTokenHash:   "",
			authHeader:     "Bearer any-token",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject request even with token when no authentication is configured",
		},
		{
			name:           "token without Bearer prefix - edge case",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "different-value",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject when token doesn't match (even without Bearer prefix)",
		},
		{
			name:           "token without Bearer prefix matches configured token",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "plain-token-123",
			expectedStatus: http.StatusOK,
			description:    "Edge case: Token without 'Bearer ' prefix passes if it matches configured token",
		},
		{
			name:           "empty bearer token",
			apiToken:       "plain-token-123",
			apiTokenHash:   "",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject empty Bearer token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				APIToken:     tt.apiToken,
				APITokenHash: tt.apiTokenHash,
			}

			_, router := setupTestServer(t, cfg)

			// Test endpoint that should be protected
			req := httptest.NewRequest("GET", "/zones", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nResponse: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestAuthMiddleware_SecurityRecommendation tests that unauthenticated access is rejected
// when no API token is configured.
func TestAuthMiddleware_SecurityRecommendation(t *testing.T) {
	t.Run("reject when no auth configured", func(t *testing.T) {
		cfg := &config.Config{
			APIToken:     "",
			APITokenHash: "",
		}

		_, router := setupTestServer(t, cfg)

		req := httptest.NewRequest("GET", "/zones", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})
}

// TestAuthMiddleware_BothTokensConfigured tests error handling when both
// api_token and api_token_hash are configured (should be prevented in config validation)
func TestAuthMiddleware_BothTokensConfigured(t *testing.T) {
	hashedToken, _ := bcrypt.GenerateFromPassword([]byte("test-token"), bcrypt.DefaultCost)

	cfg := &config.Config{
		APIToken:     "plain-token",
		APITokenHash: string(hashedToken),
	}

	// Note: This scenario should be prevented by config.Validate()
	// Testing the middleware behavior if it somehow gets through

	_, router := setupTestServer(t, cfg)

	t.Run("prefers hash over plain when both configured", func(t *testing.T) {
		// With correct hash token
		req := httptest.NewRequest("GET", "/zones", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected hash to be checked first and succeed, got status %d", w.Code)
		}

		// With plain token only (should fail because hash is checked first and doesn't fallback)
		req = httptest.NewRequest("GET", "/zones", nil)
		req.Header.Set("Authorization", "Bearer plain-token")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Current behavior: Hash is checked first, if it fails, authentication fails
		// There is NO fallback to plain token when hash is configured
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected no fallback to plain token (should be 401), got status %d", w.Code)
		}
	})
}

// TestAuthMiddleware_CaseInsensitiveBearer tests Bearer prefix handling
func TestAuthMiddleware_CaseInsensitiveBearer(t *testing.T) {
	cfg := &config.Config{
		APIToken: "test-token",
	}

	_, router := setupTestServer(t, cfg)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"Bearer with capital B", "Bearer test-token", http.StatusOK},
		{"bearer lowercase", "bearer test-token", http.StatusUnauthorized}, // Case sensitive!
		{"BEARER uppercase", "BEARER test-token", http.StatusUnauthorized}, // Case sensitive!
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/zones", nil)
			req.Header.Set("Authorization", tt.authHeader)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Expected status %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}

// countingReader counts how many bytes the server consumed from the request body
type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}

func TestBodyLimit_AppliesToAdminRoutes(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	cfg := &config.Config{APIToken: "t", Admin: config.AdminConfig{Enabled: true, Username: "admin", PasswordHash: string(hash)}}
	_, r := setupTestServer(t, cfg)

	const size = 20 << 20
	body := "--b\r\nContent-Disposition: form-data; name=\"username\"\r\n\r\n" + strings.Repeat("x", size) + "\r\n--b--\r\n"
	cr := &countingReader{r: strings.NewReader(body)}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", cr)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if cr.n > 11<<20 {
		t.Fatalf("server read %d bytes of the body, limit is 10 MB", cr.n)
	}
}
