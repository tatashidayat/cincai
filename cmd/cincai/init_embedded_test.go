package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedTemplatesMatchRepoExamples keeps cmd/cincai/templates in lockstep
// with the tracked config/*.example files. If you edit an example, copy it here
// too (this test fails otherwise).
func TestEmbeddedTemplatesMatchRepoExamples(t *testing.T) {
	for _, name := range []string{"cincai.yaml.example", "providers.yaml.example"} {
		emb, err := exampleTemplates.ReadFile("templates/" + name)
		if err != nil {
			t.Fatalf("embedded %s: %v", name, err)
		}
		disk, err := os.ReadFile(filepath.Join("..", "..", "config", name))
		if err != nil {
			t.Fatalf("tracked %s: %v", name, err)
		}
		if string(emb) != string(disk) {
			t.Errorf("cmd/cincai/templates/%s drifted from config/%s — copy the tracked example back into templates/", name, name)
		}
	}
}

// TestInitIntoFreshDirUsesEmbeddedTemplates covers the standalone-binary path:
// no *.example files on disk in the target, init still scaffolds everything.
func TestInitIntoFreshDirUsesEmbeddedTemplates(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	for _, f := range []string{
		filepath.Join(dir, "config", "cincai.yaml"),
		filepath.Join(dir, "config", "providers.yaml"),
		filepath.Join(dir, "config", "cincai.dev.env"),
		filepath.Join(dir, "data", ".auth"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("init did not create %s: %v", f, err)
		}
	}
	disk, err := os.ReadFile(filepath.Join("..", "..", "config", "cincai.yaml.example"))
	if err != nil {
		t.Fatalf("tracked example: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "config", "cincai.yaml"))
	if err != nil {
		t.Fatalf("scaffolded config: %v", err)
	}
	if string(got) != string(disk) {
		t.Errorf("scaffolded cincai.yaml differs from tracked example")
	}
	// Idempotent: a second run must not error or rewrite.
	if err := runInit(dir, false); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
}
