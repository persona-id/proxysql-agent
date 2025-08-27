# ProxySQL Agent - Code Improvement TODO

Expert Go code review completed: 2025-12-16

## 🔴 CRITICAL ISSUES (Fix Immediately)

### 1. Constructor Anti-Pattern (`proxysql.go:59`)

**Problem:** Method on nil receiver that's called as constructor
```go
// Current: main.go:34
var psql *proxysql.ProxySQL
psql, err = psql.New(settings)  // Calling method on nil!

// Current: proxysql.go:59
func (p *ProxySQL) New(configs *configuration.Config) (*ProxySQL, error)
```

**Fix:**
```go
// Change to function
func NewProxySQL(configs *configuration.Config) (*ProxySQL, error) {
    // ... same implementation
    return &ProxySQL{
        conn:          conn,
        settings:      settings,
        shutdownPhase: PhaseRunning,
        // Remove explicit zero values: nil, sync.Once{}, sync.RWMutex{}
    }, nil
}

// main.go:34
psql, err := proxysql.NewProxySQL(settings)
```

This is **not idiomatic Go**. Constructors should be package-level functions named `New` or `NewType`.

---

### 2. Backwards Logic (`proxysql.go:153`)

**Problem:** Impossible condition
```go
switch {
case results.Backends.Online > results.Backends.Total:  // Online can't exceed Total!
    results.Status = "ok"
    results.Message = "some backends offline"
```

**Fix:**
```go
switch {
case results.Backends.Online == results.Backends.Total:
    results.Status = "ok"
    results.Message = "all backends online"
case results.Backends.Online > 0:
    results.Status = "ok"
    results.Message = "some backends offline"
```

---

### 3. SQL Injection Risk (`core.go:283`)

**Problem:** String interpolation for SQL
```go
commands = append(commands, fmt.Sprintf(
    "INSERT INTO proxysql_servers VALUES (%q, %d, 0, %q)",
    pod.Status.PodIP, port, pod.Name))
```

**Fix:** Use parameterized queries
```go
// Extract to method with proper parameterization
func (p *ProxySQL) insertProxySQLServer(ctx context.Context, ip string, port int, name string) error {
    _, err := p.conn.ExecContext(ctx,
        "INSERT INTO proxysql_servers (hostname, port, weight, comment) VALUES (?, ?, 0, ?)",
        ip, port, name)
    return err
}
```

Yes, data comes from K8s API, but defense-in-depth matters.

---

### 4. Wrong Context Usage (`restapi.go:218`)

**Problem:** Ignoring request context
```go
func startupHandler(psql *proxysql.ProxySQL, _ *configuration.Config) http.HandlerFunc {
    return func(w http.ResponseWriter, _ *http.Request) {
        err := psql.Ping(context.Background()) //nolint:contextcheck
```

**Fix:**
```go
return func(w http.ResponseWriter, r *http.Request) {
    err := psql.Ping(r.Context())  // Respects cancellation!
```

Same issue appears in multiple handlers. Request context enables proper timeout/cancellation propagation.

---

## 🟡 HIGH PRIORITY (Architectural Improvements)

### 5. Duplicate Code - Load Commands

Appears 3x identically (`core.go:287-293`, `core.go:335-341`, `satellite.go:126-130`):

**Fix:** Extract to method
```go
func (p *ProxySQL) loadRuntimeConfiguration(ctx context.Context) error {
    commands := []string{
        "LOAD PROXYSQL SERVERS TO RUNTIME",
        "LOAD ADMIN VARIABLES TO RUNTIME",
        "LOAD MYSQL VARIABLES TO RUNTIME",
        "LOAD MYSQL SERVERS TO RUNTIME",
        "LOAD MYSQL USERS TO RUNTIME",
        "LOAD MYSQL QUERY RULES TO RUNTIME",
    }

    for _, cmd := range commands {
        if p.IsShuttingDown() {
            return nil
        }
        if _, err := p.conn.ExecContext(ctx, cmd); err != nil {
            return fmt.Errorf("failed to load %s: %w", cmd, err)
        }
    }
    return nil
}
```

---

### 6. Magic Numbers Everywhere (`configuration.go:163-186`)

**Problem:** 10 instances of `//nolint:mnd`

**Fix:** Extract constants
```go
const (
    DefaultStartDelay       = 0
    DefaultCoreInterval     = 10 * time.Second
    DefaultSatelliteInterval = 10 * time.Second
    DefaultAPIPort          = 8080
    DefaultDrainTimeout     = 30 * time.Second
    DefaultShutdownTimeout  = 60 * time.Second
)

func setupDefaults() {
    viper.GetViper().SetDefault("core.interval", int(DefaultCoreInterval.Seconds()))
    viper.GetViper().SetDefault("api.port", DefaultAPIPort)
    // ...
}
```

Using `time.Duration` types is more idiomatic than raw ints for timeouts.

---

### 7. Missing Testability - Add Interfaces

**Problem:** Concrete types everywhere prevent unit testing

**Fix:** Define minimal interfaces
```go
// In proxysql package
type HealthChecker interface {
    RunProbes(ctx context.Context) (ProbeResult, error)
    IsShuttingDown() bool
    Ping(ctx context.Context) error
}

// ProxySQL implements HealthChecker
var _ HealthChecker = (*ProxySQL)(nil)

// Update restapi handlers
func livenessHandler(checker HealthChecker) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // Now testable with mock!
    }
}
```

This enables proper unit tests without sqlmock gymnastics.

---

### 8. Race Condition in Nil Check (`core.go:159`)

**Problem:** Unsynchronized access to `p.conn`
```go
if p.conn == nil || p.IsShuttingDown() {  // p.conn not protected by mutex!
    slog.Debug("skipping podAdded: shutting down")
    return
}
```

**Fix:** Protect with mutex or use atomic
```go
// Option 1: Extend shutdown mutex scope
func (p *ProxySQL) shouldSkipOperation() bool {
    p.shutdownMu.RLock()
    defer p.shutdownMu.RUnlock()
    return p.conn == nil || p.shutdownPhase != PhaseRunning
}

// Usage
if p.shouldSkipOperation() {
    slog.Debug("skipping operation")
    return
}
```

---

## 🟢 MEDIUM PRIORITY (Code Quality)

### 9. Error Variable Capture Bug (`satellite.go:35-48`)

**Problem:**
```go
var shutdownErr error
p.shutdownOnce.Do(func() {
    err := p.startDraining(ctx)  // Shadows shutdownErr!
    if err != nil {
        shutdownErr = fmt.Errorf("failed to start draining: %w", err)
        return  // Returns from closure, not function
    }
})
return shutdownErr
```

**Fix:**
```go
var shutdownErr error
p.shutdownOnce.Do(func() {
    if err := p.startDraining(ctx); err != nil {
        shutdownErr = fmt.Errorf("failed to start draining: %w", err)
        return
    }
    if err := p.gracefulShutdown(ctx); err != nil {
        shutdownErr = fmt.Errorf("graceful shutdown failed: %w", err)
    }
})
return shutdownErr
```

Avoid shadowing with `:=` inside closures that capture variables.

---

### 10. Nil Connection Returns Success (`proxysql.go:92`)

**Problem:**
```go
func (p *ProxySQL) Ping(ctx context.Context) error {
    if p.conn == nil || p.IsShuttingDown() {
        return nil  // Success?!
    }
```

**Fix:**
```go
func (p *ProxySQL) Ping(ctx context.Context) error {
    if p.conn == nil {
        return errors.New("connection not initialized")
    }
    if p.IsShuttingDown() {
        return errors.New("shutting down")
    }
    // ...
}
```

Or use sentinel errors:
```go
var (
    ErrNotConnected = errors.New("not connected to ProxySQL")
    ErrShuttingDown = errors.New("ProxySQL client shutting down")
)
```

---

### 11. Remove Zero-Value Initializations (`proxysql.go:79-87`)

**Current:**
```go
return &ProxySQL{
    clientset:     nil,           // Remove
    conn:          conn,
    settings:      settings,
    shutdownOnce:  sync.Once{},   // Remove
    shutdownPhase: PhaseRunning,  // Remove if PhaseRunning = 0
    shutdownMu:    sync.RWMutex{}, // Remove
    httpServer:    nil,           // Remove
}
```

**Better:**
```go
return &ProxySQL{
    conn:     conn,
    settings: settings,
}
```

Only initialize non-zero values. This is standard Go practice.

---

### 12. Missing Test Coverage

**Critical gaps:**
- `main.go`: 0% coverage (141 lines untested)
- `core.go:83-228`: Core() function completely untested (150 lines)
- No integration tests

**Recommendations:**
1. Add table-driven tests for signal handling in `main_test.go`
2. Mock Kubernetes informers to test `Core()` function
3. Add integration test with test container (testcontainers-go)
4. Test HTTP handlers with httptest.Server and mock interfaces

---

## 🔵 LOW PRIORITY (Polish)

### 13. Goroutine Leak (`main.go:45-61`)
Signal handler goroutine never exits. Add cleanup:
```go
defer signal.Stop(sigChan)
defer close(sigChan)
```

### 14. Unused Context (`satellite.go:152`)
```go
func (p *ProxySQL) DumpData(ctx context.Context) {
    // ctx never used! Should cancel queries if context cancelled
}
```

### 15. CSV Escaping (`satellite.go:266`)
Use `encoding/csv` properly instead of manual quoting.

### 16. Comment Typo (`core.go:11`)
"reqiured" → "required"

---

## 🎯 Simplification Opportunities

### 1. Collapse Nested Structs
```go
// Current (proxysql.go:107)
Backends: &struct {
    Total   int `json:"total,omitempty"`
    Online  int `json:"online,omitempty"`
    Shunned int `json:"shunned,omitempty"`
}

// Better: Define type at package level
type BackendStatus struct {
    Total   int `json:"total,omitempty"`
    Online  int `json:"online,omitempty"`
    Shunned int `json:"shunned,omitempty"`
}

type ProbeResult struct {
    Backends *BackendStatus `json:"backends,omitempty"`
    // ...
}
```

### 2. Reduce Configuration Verbosity
Current test helper has 49 lines of struct initialization. Use functional options pattern:

```go
type ConfigOption func(*Config)

func WithStartDelay(d time.Duration) ConfigOption {
    return func(c *Config) { c.StartDelay = d }
}

func NewTestConfig(opts ...ConfigOption) *Config {
    cfg := defaultConfig()
    for _, opt := range opts {
        opt(cfg)
    }
    return cfg
}

// Usage
cfg := NewTestConfig(WithStartDelay(5*time.Second), WithLogLevel("DEBUG"))
```

### 3. Consolidate Error Messages
Standardize format across codebase:
```go
return -1, fmt.Errorf("query %q failed: %w", query, err)
```

---

## Summary: Prioritized Action Plan

**Week 1 (Critical):**
1. ✅ Fix constructor pattern → `NewProxySQL()` function
2. ✅ Fix backwards logic in backend health check
3. ✅ Fix context usage in HTTP handlers
4. ✅ Add parameterized queries for SQL

**Week 2 (Architecture):**
5. ⬜ Extract duplicate load commands
6. ⬜ Extract magic number constants
7. ⬜ Define interfaces for testability
8. ⬜ Fix race conditions in conn checks

**Week 3 (Quality):**
9. ⬜ Add test coverage for main.go and Core()
10. ⬜ Fix error capture in sync.Once
11. ⬜ Fix nil connection error handling
12. ⬜ Add integration tests

**Ongoing:**
- Establish code review checklist
- Document architectural patterns
- Set up pre-commit hooks for linting

---

## Detailed Analysis Reference

For complete analysis with file:line references, see exploration agent output (ID: a2176a9).

**Codebase Statistics:**
- Total Go LOC: 3,628
- Test LOC: 1,565
- Test-to-code ratio: 0.43
- Configuration coverage: ~95%
- ProxySQL package coverage: ~60%
- Main package coverage: ~0%

**Key Findings:**
- 12+ instances of `context.Background()` misuse
- 10 magic number nolints that should be constants
- 3 instances of duplicate load command code
- 2 critical logic errors (constructor, backwards condition)
- 1 potential SQL injection vector
- 0 defined interfaces (100% concrete types)

The codebase is generally solid and follows many Go best practices, but has several non-idiomatic patterns that reduce maintainability. The constructor anti-pattern and backwards logic are the most critical issues requiring immediate attention.
