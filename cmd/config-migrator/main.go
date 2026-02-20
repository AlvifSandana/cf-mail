package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"tuiotp/internal/config"
)

func main() {
	var (
		configPath = flag.String("config", "./config.yml", "path to source config file")
		mode       = flag.String("mode", "hybrid", "migration mode: hybrid|inline")
		outPath    = flag.String("out", "", "output config path (default: <name>.migrated.yml)")
		inPlace    = flag.Bool("in-place", false, "overwrite source config file")
	)
	flag.Parse()

	if *mode != "hybrid" && *mode != "inline" {
		fatalf("invalid --mode %q (use hybrid|inline)", *mode)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}

	token := strings.TrimSpace(cfg.Cloudflare.APIToken)
	if token == "" {
		fatalf("cloudflare api token not resolved; set cloudflare.api_token or export %s", cfg.Cloudflare.APITokenEnv)
	}
	password := strings.TrimSpace(cfg.Mailbox.IMAP.Password)
	if password == "" {
		fatalf("imap password not resolved; set mailbox.imap.password or export %s", cfg.Mailbox.IMAP.PasswordEnv)
	}

	cfg.Cloudflare.APIToken = token
	cfg.Mailbox.IMAP.Password = password

	if *mode == "inline" {
		cfg.Cloudflare.APITokenEnv = ""
		cfg.Mailbox.IMAP.PasswordEnv = ""
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		fatalf("marshal migrated config: %v", err)
	}

	dst := *outPath
	if *inPlace {
		dst = *configPath
	}
	if dst == "" {
		dst = defaultOutPath(*configPath)
	}

	if err := os.WriteFile(dst, b, 0o600); err != nil {
		fatalf("write %s: %v", dst, err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		fatalf("chmod 600 %s: %v", dst, err)
	}

	fmt.Printf("migrated config written to %s (mode=%s)\n", dst, *mode)
}

func defaultOutPath(src string) string {
	ext := filepath.Ext(src)
	base := strings.TrimSuffix(src, ext)
	if ext == "" {
		return src + ".migrated.yml"
	}
	return base + ".migrated" + ext
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
