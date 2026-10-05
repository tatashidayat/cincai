package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func initCmd(args []string) int {
	fs := newFlagSet("init")
	dir := fs.String("dir", ".", "project directory to initialize")
	force := fs.Bool("force", false, "overwrite existing config files")
	if wantsHelp(args) {
		printCommandHelp("cincai init — scaffold local dev config",
			"  cincai init [--dir PATH] [--force]", fs)
		return 0
	}
	if err := parseFlags(fs, args); err != nil {
		return 2
	}
	if err := runInit(*dir, *force); err != nil {
		fmt.Fprintf(os.Stderr, "cincai init: %v\n", err)
		return 1
	}
	return 0
}

func runInit(dir string, force bool) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	configDir := filepath.Join(root, "config")
	authDir := filepath.Join(root, "data", ".auth")
	for _, d := range []string{configDir, authDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	copies := []struct {
		src string
		dst string
	}{
		{filepath.Join(configDir, "cincai.yaml.example"), filepath.Join(configDir, "cincai.yaml")},
		{filepath.Join(configDir, "providers.yaml.example"), filepath.Join(configDir, "providers.yaml")},
	}
	for _, c := range copies {
		if err := copyExample(c.src, c.dst, force); err != nil {
			return err
		}
	}

	devEnv := filepath.Join(configDir, "cincai.dev.env")
	if _, err := os.Stat(devEnv); err != nil || force {
		if err := writeDevEnv(devEnv, force); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", devEnv)
	}

	fmt.Fprintf(os.Stderr, "ready — run:\n")
	fmt.Fprintf(os.Stderr, "  set -a && source %s && set +a\n", devEnv)
	fmt.Fprintf(os.Stderr, "  cincai credential import PROFILE --api-key KEY --config %s\n", filepath.Join(configDir, "cincai.yaml"))
	fmt.Fprintf(os.Stderr, "  cincai serve --config %s\n", filepath.Join(configDir, "cincai.yaml"))
	return nil
}

func copyExample(src, dst string, force bool) error {
	if _, err := os.Stat(src); err != nil {
		// Outside the repo checkout the tracked *.example files are absent;
		// fall back to the copies embedded in the binary (same content, kept
		// in lockstep by init_embedded_test.go).
		base := filepath.Base(src)
		if b, embErr := exampleTemplates.ReadFile("templates/" + base); embErr == nil {
			if _, err := os.Stat(dst); err == nil && !force {
				return nil
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s (embedded template)\n", dst)
			return nil
		}
		return fmt.Errorf("missing %s (run from project root or set --dir)", src)
	}
	if _, err := os.Stat(dst); err == nil && !force {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", dst)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func writeDevEnv(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return nil
	}
	key, err := randomBase64(32)
	if err != nil {
		return err
	}
	body := strings.Join([]string{
		fmt.Sprintf("# Generated %s — gitignored; keep this file to reuse broker.db", time.Now().UTC().Format(time.RFC3339)),
		fmt.Sprintf("export CINCAI_BROKER_KEY=%q", key),
		"",
	}, "\n")
	return os.WriteFile(path, []byte(body), 0o600)
}

func randomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
