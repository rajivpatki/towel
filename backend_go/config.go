package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sqliteVec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	sqliteDriver "github.com/mattn/go-sqlite3"
)

const (
	sqliteDriverName        = "sqlite3_vec"
	sqliteBusyTimeoutMillis = 10000
	sqliteMaxOpenConns      = 4
	sqliteMaxIdleConns      = 2
)

var sqliteConnectionHookOnce sync.Once

func loadConfig() Config {
	databaseURL := envOrDefault("DATABASE_URL", "sqlite+aiosqlite:////data/towel.db")
	dataDir := envOrDefault("DATA_DIR", "/data")
	apiPrefix := strings.TrimSpace(envOrDefault("API_PREFIX", "/api"))
	if apiPrefix == "" {
		apiPrefix = "/api"
	}
	if !strings.HasPrefix(apiPrefix, "/") {
		apiPrefix = "/" + apiPrefix
	}
	corsRaw := envOrDefault("CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")
	origins := make([]string, 0)
	for _, origin := range strings.Split(corsRaw, ",") {
		trimmed := strings.TrimSpace(origin)
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return Config{
		AppName:          envOrDefault("APP_NAME", "Towel"),
		APIPrefix:        apiPrefix,
		DatabaseURL:      databaseURL,
		DatabasePath:     parseDatabasePath(databaseURL, dataDir),
		DataDir:          dataDir,
		PublicAPIBaseURL: strings.TrimRight(envOrDefault("PUBLIC_API_BASE_URL", "http://localhost:8000"), "/"),
		CORSOrigins:      origins,
	}
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func parseDatabasePath(databaseURL string, dataDir string) string {
	prefixes := []string{"sqlite+aiosqlite:///", "sqlite:///"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(databaseURL, prefix) {
			trimmed := databaseURL[len(prefix):]
			if !strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, ":") {
				trimmed = filepath.Join(dataDir, trimmed)
			}
			return filepath.Clean(trimmed)
		}
	}
	if strings.TrimSpace(databaseURL) == "" {
		return filepath.Join(dataDir, "towel.db")
	}
	return filepath.Clean(databaseURL)
}

func newApp(config Config) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(config.DatabasePath), 0o755); err != nil {
		return nil, err
	}
	registerSQLiteDriver()
	db, err := sql.Open(sqliteDriverName, config.DatabasePath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxIdleConns)
	app := &App{
		config: config,
		db:     db,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		streamSessions: make(map[string]*streamSession),
	}
	app.emailEmbeddingCond = sync.NewCond(&app.emailEmbeddingMu)
	app.emailEmbeddingPendingLimit = defaultEmailEmbeddingQueueSize
	if err := app.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	appInstance = app
	app.startEmailSyncLoop()
	app.startEmailEmbeddingWorkers()
	app.refreshMemoryEmbeddingsInBackground("startup", false)
	if err := app.restartGoogleChatListener(); err != nil {
		log.Printf("google chat listener not started: %v", err)
	}
	return app, nil
}

func registerSQLiteDriver() {
	sqliteConnectionHookOnce.Do(func() {
		sqliteVec.Auto()
		sql.Register(sqliteDriverName, &sqliteDriver.SQLiteDriver{
			ConnectHook: configureSQLiteConnection,
		})
	})
}

func configureSQLiteConnection(conn *sqliteDriver.SQLiteConn) error {
	busyTimeout := strconv.Itoa(sqliteBusyTimeoutMillis)
	for _, pragma := range []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = ` + busyTimeout + `;`,
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA synchronous = NORMAL;`,
	} {
		if _, err := conn.Exec(pragma, nil); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	config := loadConfig()
	app, err := newApp(config)
	if err != nil {
		log.Fatalf("failed to initialize backend: %v", err)
	}
	defer app.db.Close()

	server := &http.Server{
		Addr:              ":8000",
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server failed: %v", err)
	}
}
