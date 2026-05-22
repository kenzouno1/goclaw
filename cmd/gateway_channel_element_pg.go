//go:build !sqliteonly

package cmd

import (
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/element"
)

// registerElementChannel registers the Element (Matrix) channel factory on the
// given instance loader. dataDir is the gateway data directory used to locate
// per-instance E2EE crypto SQLite files at <dataDir>/element/<instanceName>/crypto.sqlite.
//
// Separated into a build-tagged file so the element package (and its mautrix-go
// dependency tree) is excluded from the sqliteonly desktop build.
func registerElementChannel(loader *channels.InstanceLoader, writer channels.CredsWriter, dataDir string) {
	loader.RegisterContextualFactory(
		channels.TypeElement,
		element.FactoryWithCredsWriterAndDataDir(writer, dataDir),
	)
}
