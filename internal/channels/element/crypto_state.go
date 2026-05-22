//go:build !sqliteonly

package element

import (
	"context"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// localCrossSigningSeeds holds the three cross-signing seed values persisted in elementCreds.
// Fields are base64url-encoded (unpadded) raw seed bytes matching mautrix CrossSigningSeeds encoding.
// Defined in the baseline (non-goolm) file so all build variants compile without goolm imports.
type localCrossSigningSeeds struct {
	MasterKey      string // base64url unpadded
	SelfSigningKey string // base64url unpadded
	UserSigningKey string // base64url unpadded
}

// HasSeeds returns true when all three seed fields are populated.
func (s localCrossSigningSeeds) HasSeeds() bool {
	return s.MasterKey != "" && s.SelfSigningKey != "" && s.UserSigningKey != ""
}

// credsWriterIface is a subset of channels.CredsWriter used by cross-signing bootstrap
// to persist updated seeds. Defined locally to avoid an import cycle from the element
// package back to the channels package (which imports element factories via build tags).
type credsWriterIface interface {
	UpdateCredentials(ctx context.Context, instanceID string, plaintext []byte) error
}

// cryptoStateMachine abstracts the per-instance E2EE crypto lifecycle so that
// the cryptohelper import (which requires -tags goolm or CGO libolm) is isolated
// behind a build tag without leaking into the common channel.go file.
type cryptoStateMachine interface {
	// Init constructs and initialises the crypto helper for this instance.
	// Called from Channel.Start after registerSyncHandlers. Must be idempotent
	// on restart (reuses persisted SQLite DB).
	Init(ctx context.Context, mx *mautrix.Client, cfg elementConfig, name string) error

	// Close releases the crypto helper's database connection.
	// Safe to call when Init was never called or returned an error.
	Close() error

	// IsReady reports whether the crypto helper was successfully initialised.
	// When false, encrypted-room sends fall back to plaintext.
	IsReady() bool

	// IsRoomEncrypted returns true if the given room has encryption enabled.
	// Returns false on any error (safe default: fall back to plaintext).
	IsRoomEncrypted(ctx context.Context, roomID id.RoomID) bool

	// EncryptMessage encrypts a message event content for the given room.
	// Returns the encrypted content ready for SendMessageEvent with EventEncrypted.
	EncryptMessage(ctx context.Context, roomID id.RoomID, content any) (*event.EncryptedEventContent, error)

	// SetDecryptErrorCallback registers a callback invoked on decrypt failure.
	// Must be called before Init. The callback must not block.
	SetDecryptErrorCallback(cb func(*event.Event, error))

	// LoadOrGenerateCrossSigning either imports existing seeds (when seeds.HasSeeds())
	// or generates and uploads new cross-signing keys to the homeserver using the given
	// password for UIA. On success, returns the new seeds and dirty=true if seeds changed.
	//
	// Callers must check dirty: when true, persist the returned newSeeds to encrypted storage.
	// A non-nil error means cross-signing was not set up; the channel continues without it.
	// When the helper is not ready (goolm tag absent, or Init not called), returns immediately
	// with empty seeds and dirty=false.
	LoadOrGenerateCrossSigning(ctx context.Context, password string, seeds localCrossSigningSeeds) (newSeeds localCrossSigningSeeds, dirty bool, err error)

	// RegisterVerificationHandler installs an SAS verification auto-accept handler
	// for incoming verification requests from users in allowFrom. botUserID is the
	// bot's own Matrix user ID, used to reject self-verify requests.
	//
	// No-op when the crypto helper is not initialised, the goolm build tag is absent,
	// or allowFrom is empty (verification disabled by default).
	//
	// Must be called AFTER LoadOrGenerateCrossSigning so signing keys are ready.
	RegisterVerificationHandler(allowFrom []string, botUserID string) error
}
