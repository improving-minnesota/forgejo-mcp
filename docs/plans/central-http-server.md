# Central HTTP-based MCP Server Deployment

**Goal**: Enable central deployment of the MCP server providing HTTP mode, where clients provide their own Personal Access Tokens (PATs) via Authorization headers.

## Requirement

Client configuration should look like this:

```json
{
  "mcpServers": {
    "forgejo-remote": {
      "type": "http",
      "url": "https://forgejo.example.com/mcp",
      "headers": {
        "Authorization": "Bearer your_token"
      }
    }
  }
}
```

## Current State

### Existing Infrastructure

- ✅ HTTP transport already implemented (`--transport http`, port via `--http-port`)
- ✅ `mcp-go v0.44.0` with `server.NewStreamableHTTPServer` support
- ✅ Logging infrastructure in place

### Current Limitations

- ❌ Token configured at server startup (flag `--token` or env `FORGEJO_ACCESS_TOKEN`)
- ❌ Forgejo client is singleton with single token (`pkg/forgejo/forgejo.go`)
- ❌ No HTTP middleware for Authorization header extraction
- ❌ No request-context-based token management
- ❌ No multi-tenant support

## Architecture Changes

### 1. HTTP Middleware for Authorization Header

**New File**: `pkg/middleware/auth.go`

```go
package middleware

import (
    "context"
    "net/http"
    "strings"

    "codeberg.org/goern/forgejo-mcp/v2/pkg/log"
)

type contextKey string

const TokenContextKey contextKey = "forgejo_token"

// ExtractToken extracts the Bearer token from the Authorization header
// and stores it in the request context
func ExtractToken(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        authHeader := r.Header.Get("Authorization")

        if authHeader == "" {
            log.Warn("Missing Authorization header",
                log.StringField("remote_addr", r.RemoteAddr),
                log.StringField("path", r.URL.Path),
            )
            http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
            return
        }

        parts := strings.SplitN(authHeader, " ", 2)
        if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
            log.Warn("Invalid Authorization header format",
                log.StringField("remote_addr", r.RemoteAddr),
            )
            http.Error(w, "Invalid Authorization header format. Expected: Bearer <token>", http.StatusUnauthorized)
            return
        }

        token := parts[1]
        if token == "" {
            log.Warn("Empty token in Authorization header",
                log.StringField("remote_addr", r.RemoteAddr),
            )
            http.Error(w, "Empty token", http.StatusUnauthorized)
            return
        }

        // Store token in context
        ctx := context.WithValue(r.Context(), TokenContextKey, token)

        log.Debug("Token extracted from Authorization header",
            log.StringField("remote_addr", r.RemoteAddr),
            log.IntField("token_length", len(token)),
        )

        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// GetTokenFromContext extracts the token from the context
func GetTokenFromContext(ctx context.Context) (string, bool) {
    token, ok := ctx.Value(TokenContextKey).(string)
    return token, ok
}
```

### 2. Request-Context-based Forgejo Client

**Changes in**: `pkg/forgejo/forgejo.go`

```go
package forgejo

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "sync"
    "time"

    "codeberg.org/goern/forgejo-mcp/v2/pkg/flag"
    "codeberg.org/goern/forgejo-mcp/v2/pkg/log"
    "codeberg.org/goern/forgejo-mcp/v2/pkg/middleware"

    "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

var (
    // Singleton client for backward compatibility (stdio/sse)
    defaultClient     *forgejo.Client
    defaultClientOnce sync.Once

    // Client cache for HTTP mode (optional, for performance)
    clientCache      sync.Map // map[string]*forgejo.Client
)

// Client returns a Forgejo client
// - In HTTP mode: creates per-request client from context token
// - In stdio/sse mode: returns singleton client with configured token
func Client(ctx context.Context) *forgejo.Client {
    // HTTP Mode: Extract token from context
    if token, ok := middleware.GetTokenFromContext(ctx); ok {
        return getOrCreateClient(token)
    }

    // Fallback: Singleton client (stdio/sse mode)
    return getDefaultClient()
}

// getDefaultClient returns the singleton client (backward compatibility)
func getDefaultClient() *forgejo.Client {
    defaultClientOnce.Do(func() {
        if defaultClient == nil {
            userAgent := flag.UserAgent
            if userAgent == "" {
                userAgent = "forgejo-mcp/" + flag.Version
            }

            c, err := forgejo.NewClient(flag.URL,
                forgejo.SetToken(flag.Token),
                forgejo.SetUserAgent(userAgent),
            )
            if err != nil {
                log.Error("Failed to create default Forgejo client",
                    log.SanitizedURLField("url", flag.URL),
                    log.ErrorField(err),
                )
                log.Fatalf("create forgejo client err: %v", err)
            }
            defaultClient = c
            log.Info("Successfully created default Forgejo client",
                log.SanitizedURLField("url", flag.URL),
                log.BoolField("token_configured", flag.Token != ""),
                log.StringField("user_agent", userAgent),
            )
        }
    })
    return defaultClient
}

// getOrCreateClient creates or retrieves a cached client for the given token
func getOrCreateClient(token string) *forgejo.Client {
    // Cache key: Hash of token (for security, not the token itself)
    cacheKey := hashToken(token)

    // Check cache
    if cached, ok := clientCache.Load(cacheKey); ok {
        log.Debug("Using cached Forgejo client")
        return cached.(*forgejo.Client)
    }

    // Create new client
    userAgent := flag.UserAgent
    if userAgent == "" {
        userAgent = "forgejo-mcp/" + flag.Version
    }

    client, err := forgejo.NewClient(flag.URL,
        forgejo.SetToken(token),
        forgejo.SetUserAgent(userAgent),
    )
    if err != nil {
        log.Error("Failed to create Forgejo client for request",
            log.SanitizedURLField("url", flag.URL),
            log.ErrorField(err),
        )
        // Fallback to default client
        return getDefaultClient()
    }

    // Store in cache
    clientCache.Store(cacheKey, client)

    log.Debug("Created new Forgejo client for request",
        log.SanitizedURLField("url", flag.URL),
        log.StringField("user_agent", userAgent),
    )

    return client
}

// hashToken creates a cache key from token using SHA256
func hashToken(token string) string {
    hash := sha256.Sum256([]byte(token))
    return hex.EncodeToString(hash[:])
}

// VerifyConnection verifies connection with context-based token
func VerifyConnection(ctx context.Context) error {
    start := time.Now()

    log.Debug("Starting connection verification",
        log.SanitizedURLField("url", flag.URL),
    )

    // Use context-aware client
    user, resp, err := Client(ctx).GetMyUserInfo()
    duration := time.Since(start)

    if err != nil {
        log.Error("Connection verification failed",
            log.SanitizedURLField("url", flag.URL),
            log.DurationField("duration", duration),
            log.ErrorField(err),
        )
        return fmt.Errorf("failed to connect to Forgejo instance at %s: %v", flag.URL, err)
    }

    log.Info("Connection verification successful",
        log.SanitizedURLField("url", flag.URL),
        log.DurationField("duration", duration),
        log.StringField("authenticated_user", user.UserName),
        log.IntField("response_status", resp.StatusCode),
    )

    return nil
}

// HealthCheck performs a lightweight health check
func HealthCheck(ctx context.Context) error {
    start := time.Now()

    log.Debug("Starting health check")

    // Perform a lightweight API call to check connectivity
    _, resp, err := Client(ctx).GetMyUserInfo()
    duration := time.Since(start)

    if err != nil {
        log.Error("Health check failed",
            log.SanitizedURLField("url", flag.URL),
            log.DurationField("duration", duration),
            log.ErrorField(err),
        )
        return fmt.Errorf("health check failed: %v", err)
    }

    log.Debug("Health check successful",
        log.SanitizedURLField("url", flag.URL),
        log.DurationField("duration", duration),
        log.IntField("response_status", resp.StatusCode),
    )

    return nil
}

// Existing LogAPICall function remains unchanged...
```

### 3. HTTP Server with Middleware Integration

**Changes in**: `operation/operation.go`

```go
case "http":
    // Create MCP server
    mcpServer := newMCPServer(version)
    RegisterTool(mcpServer)

    // Create HTTP handler
    httpServer := server.NewStreamableHTTPServer(mcpServer)

    // Wrap with auth middleware
    handler := middleware.ExtractToken(httpServer.Handler())

    // Create HTTP server with middleware
    srv := &http.Server{
        Addr:    fmt.Sprintf(":%d", flag.HTTPPort),
        Handler: handler,
        // Add timeouts for production
        ReadTimeout:  15 * time.Second,
        WriteTimeout: 15 * time.Second,
        IdleTimeout:  60 * time.Second,
    }

    log.Info("Starting MCP streamable HTTP server with token authentication",
        log.IntField("port", flag.HTTPPort),
        log.StringField("mode", "multi-user"),
    )

    if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        log.Error("Failed to start streamable HTTP server",
            log.IntField("port", flag.HTTPPort),
            log.ErrorField(err),
        )
        return fmt.Errorf("failed to start streamable HTTP server: %w", err)
    }

    log.Info("MCP streamable HTTP server shutdown")
```

### 4. Tool Handler Adjustments

**All tool handlers** must use context:

```go
// Before:
client := forgejo.Client()

// After:
client := forgejo.Client(ctx)
```

**Example**: `operation/user/notification.go`

```go
func ListNotificationsFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // Context-aware client
    client := forgejo.Client(ctx)

    // Rest remains the same...
}
```

### 5. Server Start Mode

**New Flag**: `--mode` or `--server-mode`

```go
var serverMode string

fs.StringVar(
    &serverMode,
    "mode",
    "single-user",
    "Server mode: 'single-user' (token from config) or 'multi-user' (token from request)",
)
```

**Logic**:

- `single-user`: Current behavior, token from config/env
- `multi-user`: Token from Authorization header, config token optional as fallback

### 6. TLS/HTTPS Support for Backend

**Goal**: Enable direct HTTPS support between proxy and MCP server backend using PEM certificate files.

**New Flags**: `--tls-cert` and `--tls-key`

**Changes in**: `cmd/cmd.go`

```go
var (
    transport string
    urlFlag   string
    ssePort   int
    httpPort  int
    token     string
    userAgent string
    tlsCert   string
    tlsKey    string
    debug     bool
)

func initFlags() {
    // ... existing flags ...

    fs.StringVar(
        &tlsCert,
        "tls-cert",
        "",
        "Path to TLS certificate file (PEM format) for HTTPS",
    )
    fs.StringVar(
        &tlsKey,
        "tls-key",
        "",
        "Path to TLS private key file (PEM format) for HTTPS",
    )

    // ... rest of flags ...
}
```

**Changes in**: `pkg/flag/flag.go`

```go
var (
    URL       string
    SSEPort   int
    HTTPPort  int
    Token     string
    Version   string
    UserAgent string
    TLSCert   string
    TLSKey    string
    Debug     bool
)
```

**Changes in**: `operation/operation.go`

```go
case "http":
    // Create MCP server
    mcpServer := newMCPServer(version)
    RegisterTool(mcpServer)

    // Create HTTP handler
    httpServer := server.NewStreamableHTTPServer(mcpServer)

    // Wrap with auth middleware
    handler := middleware.ExtractToken(httpServer.Handler())

    // Create HTTP server with middleware
    srv := &http.Server{
        Addr:    fmt.Sprintf(":%d", flag.HTTPPort),
        Handler: handler,
        // Add timeouts for production
        ReadTimeout:  15 * time.Second,
        WriteTimeout: 15 * time.Second,
        IdleTimeout:  60 * time.Second,
    }

    // Check if TLS is configured
    useTLS := flag.TLSCert != "" && flag.TLSKey != ""

    if useTLS {
        log.Info("Starting MCP streamable HTTPS server with token authentication",
            log.IntField("port", flag.HTTPPort),
            log.StringField("mode", "multi-user"),
            log.StringField("tls_cert", flag.TLSCert),
            log.BoolField("tls_enabled", true),
        )

        // Validate certificate files exist
        if _, err := os.Stat(flag.TLSCert); os.IsNotExist(err) {
            log.Error("TLS certificate file not found",
                log.StringField("cert_path", flag.TLSCert),
                log.ErrorField(err),
            )
            return fmt.Errorf("TLS certificate file not found: %s", flag.TLSCert)
        }
        if _, err := os.Stat(flag.TLSKey); os.IsNotExist(err) {
            log.Error("TLS key file not found",
                log.StringField("key_path", flag.TLSKey),
                log.ErrorField(err),
            )
            return fmt.Errorf("TLS key file not found: %s", flag.TLSKey)
        }

        if err := srv.ListenAndServeTLS(flag.TLSCert, flag.TLSKey); err != nil && err != http.ErrServerClosed {
            log.Error("Failed to start streamable HTTPS server",
                log.IntField("port", flag.HTTPPort),
                log.ErrorField(err),
            )
            return fmt.Errorf("failed to start streamable HTTPS server: %w", err)
        }
    } else {
        log.Info("Starting MCP streamable HTTP server with token authentication",
            log.IntField("port", flag.HTTPPort),
            log.StringField("mode", "multi-user"),
            log.BoolField("tls_enabled", false),
        )
        log.Warn("Running HTTP server without TLS - not recommended for production",
            log.StringField("recommendation", "Use --tls-cert and --tls-key for HTTPS"),
        )

        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Error("Failed to start streamable HTTP server",
                log.IntField("port", flag.HTTPPort),
                log.ErrorField(err),
            )
            return fmt.Errorf("failed to start streamable HTTP server: %w", err)
        }
    }

    log.Info("MCP streamable HTTP(S) server shutdown")
```

**Usage Examples**:

```bash
# HTTP mode without TLS (development only)
forgejo-mcp --transport http --http-port 8080 --mode multi-user --url https://forgejo.example.com

# HTTPS mode with TLS certificates
forgejo-mcp --transport http --http-port 8443 --mode multi-user \
  --url https://forgejo.example.com \
  --tls-cert /path/to/cert.pem \
  --tls-key /path/to/key.pem
```

**Deployment Scenarios**:

1. **Direct HTTPS (No Proxy)**:
   - MCP server listens on HTTPS with own certificates
   - Clients connect directly to MCP server
   - Use case: Simple deployments, internal networks

2. **HTTPS to HTTPS (Proxy + Backend TLS)**:
   - Nginx/Traefik terminates client TLS
   - Proxy connects to MCP server via HTTPS
   - Use case: Defense in depth, encrypted internal traffic

3. **HTTPS to HTTP (Proxy Only)**:
   - Nginx/Traefik terminates TLS
   - Proxy connects to MCP server via HTTP
   - Use case: Trusted internal network, simpler setup

**Updated Docker Compose Example**:

```yaml
version: "3.8"

services:
  forgejo-mcp:
    image: forgejo-mcp:latest
    command: >
      forgejo-mcp
      --transport http
      --http-port 8443
      --mode multi-user
      --url https://forgejo.example.com
      --tls-cert /certs/server.crt
      --tls-key /certs/server.key
    ports:
      - "8443:8443"
    volumes:
      - ./certs:/certs:ro
    environment:
      - FORGEJO_DEBUG=false
    restart: unless-stopped
```

**Updated Nginx Configuration (HTTPS to HTTPS)**:

```nginx
server {
    listen 443 ssl http2;
    server_name mcp.example.com;

    # Client-facing TLS
    ssl_certificate /etc/nginx/ssl/client-cert.pem;
    ssl_certificate_key /etc/nginx/ssl/client-key.pem;

    location /mcp {
        # Backend HTTPS connection
        proxy_pass https://forgejo-mcp:8443;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Pass through Authorization header
        proxy_pass_header Authorization;

        # Backend TLS verification (optional)
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /etc/nginx/ssl/backend-ca.pem;
        proxy_ssl_name forgejo-mcp;
    }
}
```

**Certificate Generation Example**:

```bash
# Self-signed certificate for development/testing
openssl req -x509 -newkey rsa:4096 -nodes \
  -keyout server.key -out server.crt \
  -days 365 -subj "/CN=forgejo-mcp"

# Production: Use Let's Encrypt or organizational CA
certbot certonly --standalone -d mcp.example.com
```

**Security Considerations**:

- ✅ TLS 1.2+ only (Go's default)
- ✅ Strong cipher suites (Go's default)
- ⚠️ Certificate validation in proxy (optional but recommended)
- ⚠️ Certificate rotation/renewal process
- ⚠️ Private key protection (file permissions, secrets management)

**Implementation Priority**: Phase 4 (Security & Performance)

## Security Considerations

### 1. Token Validation

- ✅ Validate token format (Bearer scheme)
- ✅ Reject empty tokens
- ⚠️ Validate token length (min/max)
- ⚠️ Implement rate limiting per token

### 2. HTTPS Enforcement

- ✅ Direct HTTPS support via `--tls-cert` and `--tls-key` flags
- ✅ TLS 1.2+ with strong cipher suites (Go defaults)
- ✅ Certificate file validation at startup
- ⚠️ Document TLS configuration and deployment scenarios
- ⚠️ Consider certificate pinning for proxy-to-backend connections
- ⚠️ Implement certificate rotation/renewal process
- ⚠️ Secure private key storage (file permissions, secrets management)

### 3. Token Caching

- ✅ Cache key is hash, not token itself
- ⚠️ Implement cache expiration (TTL)
- ⚠️ Set cache size limit
- ⚠️ Keep sensitive data out of logs

### 4. Error Handling

- ✅ No token details in error messages
- ✅ Generic error messages for clients
- ✅ Detailed logs only server-side

### 5. Audit Logging

- ⚠️ Log user identification from token
- ⚠️ Track API calls with user context
- ⚠️ Log failed authentication attempts

## Implementation Order

### Phase 0: Pre-Implementation (1 day)

**Critical Verification Tasks**:

1. ⚠️ Verify `mcp-go v0.44.0` has `Handler()` method on `StreamableHTTPServer`
2. ⚠️ Count exact handler functions: `grep -r "func.*Fn(ctx context.Context" operation/ | wc -l`
3. ⚠️ Create test infrastructure (mock Forgejo server, test certificates)
4. ⚠️ Generate test data fixtures

**Deliverables**:

- API compatibility verification report
- Handler migration checklist (77 handlers across 17 files)
- Test infrastructure setup

### Phase 1: Basic Infrastructure (2-3 days)

**Implementation Tasks**:

1. ✅ Create `pkg/middleware/auth.go` with token extraction
2. ✅ Extend `pkg/forgejo/forgejo.go` with context-based client
3. ✅ Update `operation/operation.go` HTTP handler with middleware
4. ⚠️ Add environment variable support for TLS flags
5. ⚠️ Implement certificate validation helpers
6. ⚠️ Add startup validation for all configurations
7. ⚠️ Create logging helpers for security events

**Testing**:

- Unit tests for middleware (5 test cases)
- Unit tests for context-based client (5 test cases)
- Coverage target: >80%

**Deliverables**:

- Working middleware with tests
- Context-based client with caching
- TLS support infrastructure

### Phase 2: Tool Handler Migration (3-4 days)

**Implementation Tasks**:

1. ⚠️ Create automated migration script (`scripts/migrate-handlers.sh`)
2. ⚠️ Migrate handlers in batches:
   - Batch 1: version, wiki, search (6 handlers)
   - Batch 2: actions, tracking (8 handlers)
   - Batch 3: user, issue (12-14 handlers)
   - Batch 4: org (15 handlers)
   - Batch 5: pull, repo (36 handlers)
3. ⚠️ Add context propagation tests for each handler
4. ⚠️ Document handlers with special requirements
5. ⚠️ Verify no `forgejo.Client()` calls remain

**Testing**:

- Integration test per domain
- Verify all handlers work with context token
- Test fallback to default client

**Deliverables**:

- All 77 handlers migrated
- Migration verification report
- Handler-specific tests

### Phase 3: Server Modes (1-2 days)

**Implementation Tasks**:

1. ⚠️ Implement `--mode` flag (single-user/multi-user)
2. ⚠️ Add mode detection logic
3. ⚠️ Implement mode-specific validation
4. ⚠️ Add environment variable `FORGEJO_SERVER_MODE`
5. ⚠️ Create mode switching tests
6. ⚠️ Document mode selection guidelines

**Testing**:

- Test single-user mode (backward compatibility)
- Test multi-user mode (Authorization header required)
- Test mode switching

**Deliverables**:

- Working server mode selection
- Mode-specific behavior tests
- Updated documentation

### Phase 4: Security & Performance (3-4 days)

**Implementation Tasks**:

1. ⚠️ Implement cache TTL mechanism (default: 1 hour)
2. ⚠️ Add cache size limits (default: 1000 clients)
3. ⚠️ Implement LRU eviction strategy
4. ⚠️ Add rate limiting per token (optional)
5. ⚠️ Implement metrics collection
6. ⚠️ Create health check endpoint (`/_health`)
7. ⚠️ Add certificate rotation support
8. ⚠️ Implement audit logging for auth events

**Testing**:

- Cache performance tests
- Load tests (1000 req/s target)
- Security audit
- Memory leak tests

**Deliverables**:

- Production-ready caching
- Health check endpoint
- Security audit report
- Performance benchmark results

### Phase 5: Testing & Deployment (2-3 days)

**Implementation Tasks**:

1. ⚠️ Create comprehensive test suite
2. ⚠️ Run full security audit (checklist)
3. ⚠️ Perform load testing
4. ⚠️ Create DEPLOYMENT.md guide
5. ⚠️ Create MIGRATION.md guide
6. ⚠️ Create SECURITY.md guide
7. ⚠️ Create TROUBLESHOOTING.md guide
8. ⚠️ Update README.md and DEVELOPER.md

**Testing**:

- Full integration test suite
- Load tests with multiple scenarios
- Security penetration testing
- Migration testing (upgrade path)

**Deliverables**:

- Complete documentation set
- Deployment guides
- Migration procedures
- Troubleshooting guide

## Open Questions

1. **Cache Strategy**:
   - LRU cache with fixed size?
   - TTL-based expiration?
   - Combine both?

2. **Fallback Behavior**:
   - If token from header is invalid: 401 or fallback to config token?
   - Recommendation: 401 for clear error handling

3. **Backward Compatibility**:
   - Default mode `single-user` for existing deployments
   - Explicit opt-in for `multi-user` mode

4. **Rate Limiting**:
   - Per token or per IP?
   - Recommendation: Per token for fair resource distribution

5. **Health Check Endpoint**:
   - Separate endpoint without auth for load balancers?
   - `/health` or `/_health`?

## Example Deployment

### Docker Compose (HTTP - Development Only)

```yaml
services:
  forgejo-mcp:
    image: forgejo-mcp:latest
    command: >
      forgejo-mcp
      --transport http
      --http-port 8080
      --mode multi-user
      --url https://forgejo.example.com
    ports:
      - "8080:8080"
    environment:
      - FORGEJO_DEBUG=false
    restart: unless-stopped
```

### Docker Compose (HTTPS - Production)

```yaml
services:
  forgejo-mcp:
    image: forgejo-mcp:latest
    command: >
      forgejo-mcp
      --transport http
      --http-port 8443
      --mode multi-user
      --url https://forgejo.example.com
      --tls-cert /certs/server.crt
      --tls-key /certs/server.key
    ports:
      - "8443:8443"
    volumes:
      - ./certs:/certs:ro
    environment:
      - FORGEJO_DEBUG=false
    restart: unless-stopped
```

### Nginx Reverse Proxy (HTTPS to HTTP)

```nginx
server {
    listen 443 ssl http2;
    server_name mcp.example.com;

    ssl_certificate /etc/nginx/ssl/cert.pem;
    ssl_certificate_key /etc/nginx/ssl/key.pem;

    location /mcp {
        proxy_pass http://forgejo-mcp:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Pass through Authorization header
        proxy_pass_header Authorization;
    }
}
```

### Nginx Reverse Proxy (HTTPS to HTTPS - Defense in Depth)

```nginx
server {
    listen 443 ssl http2;
    server_name mcp.example.com;

    # Client-facing TLS
    ssl_certificate /etc/nginx/ssl/client-cert.pem;
    ssl_certificate_key /etc/nginx/ssl/client-key.pem;

    location /mcp {
        # Backend HTTPS connection
        proxy_pass https://forgejo-mcp:8443;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Pass through Authorization header
        proxy_pass_header Authorization;

        # Backend TLS verification (optional but recommended)
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /etc/nginx/ssl/backend-ca.pem;
        proxy_ssl_name forgejo-mcp;
    }
}
```

## Migration Path

### For Existing Single-User Deployments

1. Update to new version (backward compatible)
2. Continue using `--token` flag or `FORGEJO_ACCESS_TOKEN`
3. Default mode is `single-user`

### For New Multi-User Deployments

1. Start server with `--mode multi-user`
2. No `--token` flag needed
3. Configure clients with Authorization header

## Testing Strategy

### Unit Tests

**Test Files to Create**:

- `pkg/middleware/auth_test.go` - Token extraction and validation
- `pkg/forgejo/forgejo_test.go` - Context-based client creation and caching

**Test Patterns**:

```go
// pkg/middleware/auth_test.go
func TestExtractToken_ValidBearer(t *testing.T)
func TestExtractToken_MissingHeader(t *testing.T)
func TestExtractToken_InvalidFormat(t *testing.T)
func TestExtractToken_EmptyToken(t *testing.T)
func TestGetTokenFromContext(t *testing.T)

// pkg/forgejo/forgejo_test.go
func TestClient_WithContextToken(t *testing.T)
func TestClient_WithoutContextToken(t *testing.T)
func TestClientCache_HitAndMiss(t *testing.T)
func TestHashToken_Consistency(t *testing.T)
func TestHashToken_Uniqueness(t *testing.T)
```

**Coverage Target**: >80%

### Integration Tests

**Test Files to Create**:

- `operation/http_integration_test.go` - End-to-end HTTP server tests

**Test Scenarios**:

```go
func TestHTTPServer_MultiUser_DifferentTokens(t *testing.T)
func TestHTTPServer_MultiUser_InvalidToken(t *testing.T)
func TestHTTPServer_MultiUser_MissingToken(t *testing.T)
func TestHTTPServer_TLS_ValidCertificate(t *testing.T)
func TestHTTPServer_TLS_InvalidCertificate(t *testing.T)
func TestHTTPServer_SingleUser_Fallback(t *testing.T)
```

**Requirements**:

- Mock Forgejo server setup
- Test certificate generation (self-signed)
- Multiple concurrent client simulation

### Load Tests

**Test Files to Create**:

- `test/load/concurrent_test.go` - Performance and concurrency tests

**Test Scenarios**:

```go
func TestConcurrentRequests_100Clients_10Tokens(t *testing.T)
func TestConcurrentRequests_1000Clients_100Tokens(t *testing.T)
func TestClientCache_MemoryUsage_1000Tokens(t *testing.T)
func TestClientCache_EvictionPerformance(t *testing.T)
```

**Performance Targets**:

- Latency: <200ms end-to-end (excluding Forgejo API)
- Throughput: 1000 req/s per core
- Cache hit rate: >95% in steady state
- Memory per cached client: <1MB

**Tools**:

```bash
# Benchmark tests
go test -bench=. -benchmem ./pkg/middleware/
go test -bench=. -benchmem ./pkg/forgejo/

# Load testing
hey -n 10000 -c 100 -H "Authorization: Bearer token123" https://localhost:8443/
```

## Tool Handler Migration

**Scope**: ~77 handler functions across 17 files need migration from `forgejo.Client()` to `forgejo.Client(ctx)`

**Files to Modify**:

- `operation/actions/actions.go` - 1 handler
- `operation/actions/workflow_runs.go` - 1 handler
- `operation/issue/issue.go` - ~8-10 handlers
- `operation/org/org.go` - ~5 handlers
- `operation/org/membership.go` - ~5 handlers
- `operation/org/teams.go` - ~5 handlers
- `operation/pull/pull.go` - ~6 handlers
- `operation/pull/review.go` - ~6 handlers
- `operation/repo/repo.go` - ~8 handlers
- `operation/repo/file.go` - ~6 handlers
- `operation/repo/branch.go` - ~3 handlers
- `operation/repo/commit.go` - ~3 handlers
- `operation/search/search.go` - ~3 handlers
- `operation/tracking/tracking.go` - ~6 handlers
- `operation/user/user.go` - ~4 handlers
- `operation/user/notification.go` - ~4 handlers
- `operation/version/version.go` - 1 handler
- `operation/wiki/wiki.go` - ~2 handlers

**Migration Pattern**:

```go
// Before:
func SomeToolFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    client := forgejo.Client()
    // ... rest of handler
}

// After:
func SomeToolFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    client := forgejo.Client(ctx)
    // ... rest of handler
}
```

**Automated Migration**:

```bash
# Create migration script
cat > scripts/migrate-handlers.sh << 'EOF'
#!/bin/bash
find operation -name "*.go" -type f -exec sed -i 's/forgejo\.Client()/forgejo.Client(ctx)/g' {} +
EOF
chmod +x scripts/migrate-handlers.sh
```

**Verification**:

```bash
# Verify no old pattern remains
grep -r "forgejo\.Client()" operation/
# Should return no results after migration
```

## Error Handling & Edge Cases

### Token Extraction Errors

- **Missing Authorization header** → 401 Unauthorized
- **Invalid format** (not "Bearer <token>") → 401 Unauthorized
- **Empty token** → 401 Unauthorized
- **Token with special characters** → Validate and sanitize

### Client Creation Errors

- **Invalid Forgejo URL** → Log error, fallback to default client
- **Network timeout** → Propagate error to client
- **Invalid token** (401 from Forgejo) → Propagate 401 to client

### TLS Configuration Errors

- **Certificate file not found** → Fail fast at startup with clear error
- **Invalid certificate format** → Fail fast at startup
- **Certificate expired** → Runtime error, log and alert
- **Key file permissions too open** → Security warning in logs

### Cache Management

- **Cache size limit reached** → LRU eviction
- **Memory pressure** → Implement cache cleanup
- **Concurrent access** → Handled by sync.Map

### Backward Compatibility

- **HTTP mode without Authorization header in single-user mode** → Use config token
- **HTTP mode without Authorization header in multi-user mode** → 401 Unauthorized
- **stdio/sse modes** → Ignore middleware, use default client

## Environment Variables

### Required (one of)

```bash
FORGEJO_URL=https://forgejo.example.com
GITEA_HOST=https://forgejo.example.com  # Deprecated, use FORGEJO_URL
```

### Optional

```bash
# Authentication
FORGEJO_ACCESS_TOKEN=your_token_here     # For single-user mode
GITEA_ACCESS_TOKEN=your_token_here       # Deprecated

# Configuration
FORGEJO_USER_AGENT=custom-agent/1.0      # Custom user agent
FORGEJO_DEBUG=true                        # Enable debug logging
GITEA_DEBUG=true                          # Deprecated

# TLS Configuration
FORGEJO_TLS_CERT=/path/to/cert.pem       # TLS certificate path
FORGEJO_TLS_KEY=/path/to/key.pem         # TLS key path

# Server Mode
FORGEJO_SERVER_MODE=multi-user           # single-user or multi-user
```

**Precedence**: CLI flags > Environment variables > Defaults

## Monitoring & Observability

### Health Check Endpoint

**Endpoint**: `GET /_health` (no authentication required)

**Response**:

```json
{
  "status": "ok",
  "version": "2.0.0",
  "mode": "multi-user",
  "tls_enabled": true,
  "active_clients": 5,
  "cache_size": 10,
  "uptime_seconds": 3600
}
```

**Implementation**:

```go
// operation/operation.go
func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
    health := map[string]interface{}{
        "status":         "ok",
        "version":        flag.Version,
        "mode":           getServerMode(),
        "tls_enabled":    flag.TLSCert != "" && flag.TLSKey != "",
        "active_clients": getActiveClientCount(),
        "cache_size":     getCacheSize(),
        "uptime_seconds": time.Since(startTime).Seconds(),
    }
    json.NewEncoder(w).Encode(health)
}
```

### Metrics (Optional - Phase 4+)

```go
// pkg/metrics/metrics.go
type Metrics struct {
    ActiveClients    atomic.Int64
    TotalRequests    atomic.Int64
    FailedRequests   atomic.Int64
    CacheHits        atomic.Int64
    CacheMisses      atomic.Int64
    TokensInCache    atomic.Int64
}
```

## Performance Benchmarks

### Targets

**Latency**:

- Token extraction: <1ms
- Client cache lookup: <0.1ms
- Client creation (cache miss): <50ms
- End-to-end request: <200ms (excluding Forgejo API time)

**Throughput**:

- Concurrent requests: 1000 req/s per core
- Different tokens: 100 unique tokens/s

**Memory**:

- Base memory: <50MB
- Per cached client: <1MB
- Cache limit: 1000 clients (configurable)

**Cache Performance**:

- Hit rate target: >95% in steady state
- Eviction strategy: LRU with TTL

### Benchmark Commands

```bash
# Run Go benchmarks
go test -bench=. -benchmem ./pkg/middleware/
go test -bench=. -benchmem ./pkg/forgejo/

# Load test with hey
hey -n 10000 -c 100 -H "Authorization: Bearer token123" https://localhost:8443/

# Concurrent load test
for i in {1..10}; do
  hey -n 1000 -c 10 -H "Authorization: Bearer token$i" https://localhost:8443/ &
done
wait
```

## Security Audit Checklist

### Authentication & Authorization

- [ ] Token format validation (Bearer scheme)
- [ ] Token length validation (min 20, max 255 chars)
- [ ] Rate limiting per token implemented
- [ ] Failed authentication attempts logged
- [ ] No token leakage in logs or error messages

### TLS/HTTPS

- [ ] TLS 1.2+ enforced
- [ ] Strong cipher suites configured
- [ ] Certificate validation at startup
- [ ] Private key file permissions checked (0600)
- [ ] Certificate expiration monitoring

### Token Management

- [ ] Tokens stored as SHA256 hashes in cache
- [ ] Cache size limits enforced
- [ ] Cache TTL implemented
- [ ] No plaintext tokens in memory dumps

### Input Validation

- [ ] URL validation (scheme, host)
- [ ] Header injection prevention
- [ ] Path traversal prevention (file paths)

### Logging & Monitoring

- [ ] Sensitive data sanitized in logs
- [ ] Audit trail for authentication events
- [ ] Security events logged with context
- [ ] Log rotation configured

### Deployment

- [ ] HTTPS enforced in production
- [ ] Firewall rules configured
- [ ] Reverse proxy security headers set
- [ ] Container security best practices followed

## Migration Guide

### Prerequisites

- Forgejo MCP Server v2.x.x installed
- Access to server configuration
- TLS certificates (recommended for production)

### Step 1: Backup Current Configuration

```bash
# Backup systemd unit or startup script
cp /etc/systemd/system/forgejo-mcp.service /etc/systemd/system/forgejo-mcp.service.backup

# Backup environment file
cp /etc/forgejo-mcp/env /etc/forgejo-mcp/env.backup
```

### Step 2: Update Server Configuration

**Old Configuration (Single-User)**:

```bash
forgejo-mcp --transport http --http-port 8080 \
  --url https://forgejo.example.com \
  --token YOUR_TOKEN
```

**New Configuration (Multi-User with TLS)**:

```bash
forgejo-mcp --transport http --http-port 8443 \
  --url https://forgejo.example.com \
  --mode multi-user \
  --tls-cert /path/to/cert.pem \
  --tls-key /path/to/key.pem
```

### Step 3: Update Client Configurations

**Old Client Config**:

```json
{
  "mcpServers": {
    "forgejo": {
      "command": "forgejo-mcp",
      "args": ["--url", "https://forgejo.example.com", "--token", "YOUR_TOKEN"]
    }
  }
}
```

**New Client Config**:

```json
{
  "mcpServers": {
    "forgejo": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_TOKEN"
      }
    }
  }
}
```

### Step 4: Test Migration

```bash
# Test health check
curl -k https://localhost:8443/_health

# Test with valid token
curl -k -H "Authorization: Bearer YOUR_TOKEN" \
  https://localhost:8443/

# Test without token (should fail)
curl -k https://localhost:8443/
```

### Step 5: Update Infrastructure

- Update health check URLs in monitoring
- Update port numbers in firewall rules
- Update load balancer configuration
- Update DNS records if needed

### Rollback Procedure

```bash
# Stop new version
systemctl stop forgejo-mcp

# Restore backup
cp /etc/systemd/system/forgejo-mcp.service.backup \
   /etc/systemd/system/forgejo-mcp.service
cp /etc/forgejo-mcp/env.backup /etc/forgejo-mcp/env

# Reload and start
systemctl daemon-reload
systemctl start forgejo-mcp
```

## Documentation Updates

- [ ] README.md: Document multi-user mode and TLS support
- [ ] DEVELOPER.md: Architecture changes and context-based client
- [ ] New file: DEPLOYMENT.md for production setup
- [ ] New file: MIGRATION.md for upgrade instructions
- [ ] New file: SECURITY.md for security best practices
- [ ] New file: TROUBLESHOOTING.md for common issues
- [ ] API documentation: Authorization header requirement

## Dependencies

### Required

- ✅ `mcp-go v0.44.0` (already present)
- ✅ `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` (already present)
- ✅ Go 1.25.0+ (already present)

### Optional (Phase 4+)

- ⚠️ `golang.org/x/time/rate` - For rate limiting per token
- ⚠️ `github.com/hashicorp/golang-lru/v2` - For LRU cache with TTL
- ⚠️ Prometheus client library - For metrics export (optional)

### Development/Testing

- Test certificate generation: `openssl` command-line tool
- Load testing: `hey` or `ab` (Apache Bench)
- Mock server: `httptest` package (standard library)

## Time Estimate

### Detailed Breakdown

**Phase 0: Pre-Implementation**

- API verification: 2 hours
- Handler inventory: 2 hours
- Test infrastructure: 4 hours
- **Subtotal**: 1 day

**Phase 1: Basic Infrastructure**

- Middleware implementation: 4 hours
- Context-based client: 6 hours
- TLS support: 4 hours
- Unit tests: 6 hours
- **Subtotal**: 2-3 days

**Phase 2: Tool Handler Migration**

- Automated migration script: 2 hours
- Handler migration (77 handlers): 16 hours
- Testing per domain: 8 hours
- Verification: 2 hours
- **Subtotal**: 3-4 days

**Phase 3: Server Modes**

- Mode flag implementation: 2 hours
- Mode logic: 4 hours
- Testing: 2 hours
- **Subtotal**: 1-2 days

**Phase 4: Security & Performance**

- Cache TTL/limits: 4 hours
- Health check endpoint: 2 hours
- Metrics collection: 4 hours
- Security audit: 8 hours
- Load testing: 4 hours
- **Subtotal**: 3-4 days

**Phase 5: Testing & Deployment**

- Integration tests: 8 hours
- Documentation: 8 hours
- Migration guides: 4 hours
- Final testing: 4 hours
- **Subtotal**: 2-3 days

**Total**: ~2-3 weeks (80-120 hours) for complete implementation with tests and documentation

### Risk Buffer

- Add 20% buffer for unexpected issues: +3-4 days
- **Realistic Total**: 3-4 weeks

## Key Changes Required

### Files to Create

1. `pkg/middleware/auth.go` - Authorization header extraction middleware

### Files to Modify

1. `pkg/forgejo/forgejo.go` - Add context-based client creation
2. `operation/operation.go` - Integrate middleware in HTTP mode
3. `cmd/cmd.go` - Add `--mode` flag (optional, for explicit multi-user mode)
4. All tool handlers in `operation/*/` - Change `forgejo.Client()` to `forgejo.Client(ctx)`

### Backward Compatibility

- Existing stdio/sse modes continue to work unchanged
- HTTP mode without Authorization header falls back to configured token (if `--mode single-user`)
- Default behavior remains unchanged for existing deployments

## Pre-Implementation Verification

### Critical Checks (Must Complete Before Starting)

1. **Verify mcp-go API Compatibility**

   ```bash
   # Check if Handler() method exists
   go doc github.com/mark3labs/mcp-go/server.StreamableHTTPServer
   # Look for: func (s *StreamableHTTPServer) Handler() http.Handler
   ```

2. **Count Handler Functions**

   ```bash
   # Expected: ~77 handlers
   grep -r "func.*Fn(ctx context.Context" operation/ | wc -l
   ```

3. **Verify Current Client Usage**

   ```bash
   # Should find ~86 occurrences
   grep -r "forgejo\.Client()" operation/ | wc -l
   ```

4. **Test Infrastructure Setup**
   - Generate self-signed test certificates
   - Create mock Forgejo server template
   - Set up test data fixtures

### Success Criteria Checklist

#### Functional Requirements

- [ ] Multi-user mode works with Authorization headers
- [ ] Single-user mode maintains backward compatibility
- [ ] TLS/HTTPS support functional with PEM certificates
- [ ] All 77 tool handlers migrated to context-based clients
- [ ] Client caching works correctly with SHA256 token hashing
- [ ] Token validation prevents unauthorized access
- [ ] Health check endpoint returns correct status

#### Non-Functional Requirements

- [ ] Performance targets met:
  - [ ] Latency <200ms end-to-end
  - [ ] Throughput 1000 req/s per core
  - [ ] Cache hit rate >95%
- [ ] Security audit passed (all checklist items)
- [ ] Test coverage >80%
- [ ] Documentation complete (7 documents)
- [ ] Migration guide tested with real upgrade
- [ ] Zero downtime deployment possible

#### Quality Gates

- [ ] All unit tests pass
- [ ] All integration tests pass
- [ ] Load tests pass (10,000 requests)
- [ ] Security scan clean (no critical/high vulnerabilities)
- [ ] Code review approved
- [ ] Documentation reviewed
- [ ] No `forgejo.Client()` calls remain (only `forgejo.Client(ctx)`)

## Risk Assessment

### High Risk Items

1. **mcp-go API Compatibility** (Blocker)
   - Risk: Handler() method may not exist
   - Mitigation: Verify before starting, have alternative approach ready
   - Impact: Could require significant rework

2. **Handler Migration Scope** (High)
   - Risk: 77 handlers is significant, may miss some
   - Mitigation: Automated script + verification step
   - Impact: Incomplete migration breaks multi-user mode

3. **Backward Compatibility** (High)
   - Risk: Breaking existing deployments
   - Mitigation: Comprehensive testing, default to single-user mode
   - Impact: User complaints, rollback required

### Medium Risk Items

1. **Performance** (Medium)
   - Risk: Cache implementation may not meet targets
   - Mitigation: Benchmark early, optimize if needed
   - Impact: Poor user experience, need optimization

2. **Security** (Medium)
   - Risk: Token handling vulnerabilities
   - Mitigation: Security audit, peer review
   - Impact: Security incident, reputation damage

3. **Testing Coverage** (Medium)
   - Risk: Insufficient test coverage
   - Mitigation: Set coverage targets, enforce in CI
   - Impact: Bugs in production

### Low Risk Items

1. **TLS Implementation** (Low)
   - Risk: Standard Go library, well-documented
   - Mitigation: Follow best practices
   - Impact: Minimal

2. **Documentation** (Low)
   - Risk: Time-consuming but straightforward
   - Mitigation: Write as you go
   - Impact: Poor user experience

## Next Steps

### Immediate Actions (Before Implementation)

1. ✅ Review and approve this design document
2. ⚠️ **CRITICAL**: Verify mcp-go Handler() method exists
3. ⚠️ Create handler migration checklist (77 handlers)
4. ⚠️ Set up test infrastructure
5. ⚠️ Generate test certificates

### Phase Execution

1. Complete Phase 0 verification tasks
2. Create implementation issues for each phase
3. Start with Phase 1 (basic infrastructure)
4. Iteratively implement and test each phase
5. Update documentation throughout
6. Run security audit before production deployment

### Post-Implementation

1. Monitor production deployment
2. Collect user feedback
3. Address issues promptly
4. Plan for future enhancements (rate limiting, metrics)
