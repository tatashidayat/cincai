package main

import "embed"

// Templates embedded so `cincai init` works outside the repo checkout (e.g. a
// standalone installed binary scaffolding a fresh dir). Kept in lockstep with
// the tracked config/*.example files by init_embedded_test.go.
//
//go:embed templates/cincai.yaml.example
//go:embed templates/providers.yaml.example
var exampleTemplates embed.FS
