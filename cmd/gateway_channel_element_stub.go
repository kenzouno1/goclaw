//go:build sqliteonly

package cmd

import (
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// registerElementChannel is a no-op stub for the sqliteonly (desktop) build.
// The Element channel requires mautrix-go which is excluded from the Lite edition.
func registerElementChannel(_ *channels.InstanceLoader, _ channels.CredsWriter, _ string) {}
