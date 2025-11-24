package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"namedot/internal/config"
	"namedot/internal/db"
	"namedot/internal/replication"
	dnssrv "namedot/internal/server/dns"
	restsrv "namedot/internal/server/rest"
)

// Build information set via -ldflags during build.
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

func main() {
	// Normalize GNU-style flags ("--flag") to Go's default ("-flag")
	if len(os.Args) > 1 {
		norm := make([]string, 0, len(os.Args))
		norm = append(norm, os.Args[0])
		for i := 1; i < len(os.Args); i++ {
			a := os.Args[i]
			if a == "--" {
				norm = append(norm, os.Args[i:]...)
				break
			}
			if strings.HasPrefix(a, "--") {
				a = "-" + strings.TrimPrefix(a, "--")
			}
			norm = append(norm, a)
		}
		os.Args = norm
	}

	var (
		cfgPath    string
		testOnly   bool
		password   string
		token      string
		showVer    bool
		exportFile string
		importFile string
		importMode string
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "namedot - GeoDNS server with master-slave replication\n\n")
		fmt.Fprintf(os.Stderr, "Usage: namedot [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -c, -config <file>        Path to config file (default: config.yaml)\n")
		fmt.Fprintf(os.Stderr, "  -t, -test                 Validate config and exit\n")
		fmt.Fprintf(os.Stderr, "  -p, -password <password>  Generate bcrypt hash for admin password and exit\n")
		fmt.Fprintf(os.Stderr, "  -g, -gen-token <token>    Generate bcrypt hash for API token and exit\n")
		fmt.Fprintf(os.Stderr, "  -export <file>            Export all zones to JSON file and exit\n")
		fmt.Fprintf(os.Stderr, "  -import <file>            Import zones from JSON file and exit\n")
		fmt.Fprintf(os.Stderr, "  -import-mode <mode>       Import mode: merge (default) or replace\n")
		fmt.Fprintf(os.Stderr, "  -v, -version              Print version and exit\n")
		fmt.Fprintf(os.Stderr, "  -h, -help                 Show this help message\n")
		fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
		fmt.Fprintf(os.Stderr, "  SGDNS_CONFIG              Config file path (overridden by -c flag)\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  namedot                          Start server with config.yaml\n")
		fmt.Fprintf(os.Stderr, "  namedot -c prod.yaml             Start with custom config\n")
		fmt.Fprintf(os.Stderr, "  namedot -t                       Validate config\n")
		fmt.Fprintf(os.Stderr, "  namedot -p mypassword            Generate password hash\n")
		fmt.Fprintf(os.Stderr, "  namedot -g mytoken               Generate API token hash\n")
		fmt.Fprintf(os.Stderr, "  namedot -export backup.json      Export all zones to file\n")
		fmt.Fprintf(os.Stderr, "  namedot -import backup.json      Import zones from file (merge)\n")
		fmt.Fprintf(os.Stderr, "  namedot -import backup.json -import-mode replace\n")
		fmt.Fprintf(os.Stderr, "                                   Import zones (replace all)\n")
		fmt.Fprintf(os.Stderr, "\nDocumentation: https://github.com/foxzi/namedot\n")
	}

	flag.StringVar(&cfgPath, "c", "", "")
	flag.StringVar(&cfgPath, "config", "", "")
	flag.BoolVar(&testOnly, "t", false, "")
	flag.BoolVar(&testOnly, "test", false, "")
	flag.StringVar(&password, "p", "", "")
	flag.StringVar(&password, "password", "", "")
	flag.StringVar(&token, "g", "", "")
	flag.StringVar(&token, "gen-token", "", "")
	flag.StringVar(&exportFile, "export", "", "")
	flag.StringVar(&importFile, "import", "", "")
	flag.StringVar(&importMode, "import-mode", "merge", "")
	flag.BoolVar(&showVer, "v", false, "")
	flag.BoolVar(&showVer, "version", false, "")
	flag.Parse()

	if showVer {
		fmt.Printf("namedot %s\n", Version)
		fmt.Printf("  Commit:    %s\n", GitCommit)
		fmt.Printf("  Built:     %s\n", BuildDate)
		fmt.Printf("  Go:        %s\n", runtime.Version())
		fmt.Printf("  Platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return
	}

	// If password flag provided, generate bcrypt and exit
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("error generating bcrypt: %v", err)
		}
		fmt.Printf("Bcrypt hash for '%s':\n%s\n", password, string(hash))
		fmt.Println("\nAdd this to your config.yaml:")
		fmt.Println("admin:")
		fmt.Println("  enabled: true")
		fmt.Println("  username: admin")
		fmt.Printf("  password_hash: \"%s\"\n", string(hash))
		return
	}

	// If token flag provided, generate bcrypt and exit
	if token != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("error generating bcrypt: %v", err)
		}
		fmt.Printf("Bcrypt hash for API token '%s':\n%s\n", token, string(hash))
		fmt.Println("\nAdd this to your config.yaml:")
		fmt.Printf("api_token_hash: \"%s\"\n", string(hash))
		fmt.Println("\nFor replication slave config:")
		fmt.Println("replication:")
		fmt.Println("  mode: slave")
		fmt.Println("  master_url: \"http://master:8080\"")
		fmt.Printf("  api_token: \"%s\"  # Use plain token for outgoing requests\n", token)
		return
	}

	// Determine config path precedence: -c/--config > env > default
	if cfgPath == "" {
		cfgPath = os.Getenv("SGDNS_CONFIG")
	}
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if testOnly {
		fmt.Printf("Config OK: %s\n", cfgPath)
		return
	}

	gormDB, err := db.OpenWithDebug(cfg.DB, cfg.Log.SQLDebug)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(gormDB); err != nil {
		log.Fatalf("migrate db: %v", err)
	}

	// Handle export command
	if exportFile != "" {
		fmt.Printf("Exporting zones to %s...\n", exportFile)
		if err := db.ExportZones(gormDB, exportFile); err != nil {
			log.Fatalf("export failed: %v", err)
		}
		var count int64
		gormDB.Model(&db.Zone{}).Count(&count)
		fmt.Printf("Successfully exported %d zones to %s\n", count, exportFile)
		return
	}

	// Handle import command
	if importFile != "" {
		if importMode != "merge" && importMode != "replace" {
			log.Fatalf("invalid import mode: %s (must be 'merge' or 'replace')", importMode)
		}
		fmt.Printf("Importing zones from %s (mode: %s)...\n", importFile, importMode)
		if err := db.ImportZones(gormDB, importFile, importMode); err != nil {
			log.Fatalf("import failed: %v", err)
		}
		ensureAllSOA(gormDB, cfg)
		var count int64
		gormDB.Model(&db.Zone{}).Count(&count)
		fmt.Printf("Successfully imported zones. Total zones in database: %d\n", count)
		return
	}

	// Ensure SOA exists/updated on startup when auto is enabled
	ensureAllSOA(gormDB, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dnsServer, err := dnssrv.NewServer(cfg, gormDB)
	if err != nil {
		log.Fatalf("dns server: %v", err)
	}

	restServer := restsrv.NewServer(cfg, gormDB, dnsServer)

	go func() {
		if err := dnsServer.Start(); err != nil {
			log.Fatalf("dns start: %v", err)
		}
	}()

	go func() {
		if err := restServer.Start(); err != nil {
			log.Fatalf("rest start: %v", err)
		}
	}()

	// Start replication sync worker for slave mode
	if cfg.Replication.Mode == "slave" {
		syncClient := replication.NewSyncClient(cfg, gormDB)
		go func() {
			// Wait a bit for REST server to start
			time.Sleep(2 * time.Second)
			syncClient.StartPeriodicSync(ctx)
		}()
		log.Printf("Slave mode enabled: syncing from %s every %d seconds",
			cfg.Replication.MasterURL, cfg.Replication.SyncIntervalSec)
	} else if cfg.Replication.Mode == "master" {
		log.Println("Master mode enabled: ready to serve replication data")
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 5*time.Second)
	defer shutdownCancel()

	_ = restServer.Shutdown(shutdownCtx)
	_ = dnsServer.Shutdown()
}

// ensureAllSOA creates/updates SOA for all zones if auto is enabled.
func ensureAllSOA(gormDB *gorm.DB, cfg *config.Config) {
	if !(cfg.SOA.AutoOnMissing || cfg.AutoSOAOnMissing) {
		return
	}
	var zones []db.Zone
	if err := gormDB.Find(&zones).Error; err != nil {
		log.Printf("SOA ensure: failed to load zones: %v", err)
		return
	}
	for _, z := range zones {
		db.BumpSOASerialAuto(gormDB, z, true, cfg.SOA.Primary, cfg.SOA.Hostmaster)
	}
}
