//go:build !sqliteonly && goolm

package element

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	// Pure-Go SQLite driver (no CGO). Registers the "sqlite" driver name.
	// Used instead of cryptohelper's string-path form (which opens via "sqlite3-fk-wal"
	// from go.mau.fi/util/dbutil/litestream — that driver requires CGO).
	// API: dbutil.NewWithDB wraps *sql.DB with mautrix dialect+logging.
	// Ref: maunium.net/go/mautrix@v0.27.0/crypto/cryptohelper/cryptohelper.go:61,87
	_ "modernc.org/sqlite"
)

// goolmCryptoState implements cryptoStateMachine using mautrix cryptohelper.
// Only compiled when -tags goolm is set (pure-Go Olm; no CGO libolm needed).
type goolmCryptoState struct {
	helper             *cryptohelper.CryptoHelper            // nil until Init succeeds
	db                 *dbutil.Database                      // held separately so we can close on Init failure
	mx                 *mautrix.Client                       // stored after Init to access StateStore
	decryptErrCallback func(*event.Event, error)             // set via SetDecryptErrorCallback before Init
	verificationHelper *verificationhelper.VerificationHelper // installed by RegisterVerificationHandler
}

func newCryptoState() cryptoStateMachine {
	return &goolmCryptoState{}
}

// SetDecryptErrorCallback registers a callback for decrypt failures.
// Must be called before Init so the helper picks it up.
func (s *goolmCryptoState) SetDecryptErrorCallback(cb func(*event.Event, error)) {
	s.decryptErrCallback = cb
}

// Init constructs and initialises the per-instance cryptohelper.
//
// Crypto file layout: <dataDir>/element/<instanceName>/crypto.sqlite
// Directory created with 0700 perms; SQLite opened in WAL mode via modernc.org/sqlite.
//
// cryptohelper.NewCryptoHelper: cryptohelper.go:61
// cryptohelper.Init:            cryptohelper.go:113
func (s *goolmCryptoState) Init(ctx context.Context, mx *mautrix.Client, cfg elementConfig, name string) error {
	// Decode base64 pickle key → raw bytes (cryptohelper requires []byte).
	pickleBytes, err := base64.StdEncoding.DecodeString(cfg.pickleKey)
	if err != nil {
		return fmt.Errorf("element: decode pickle_key: %w", err)
	}
	if len(pickleBytes) == 0 {
		return fmt.Errorf("element: pickle_key is empty")
	}

	// Create per-instance crypto directory with restricted permissions.
	cryptoDir := filepath.Join(cfg.dataDir, "element", cfg.instanceID)
	if err := os.MkdirAll(cryptoDir, 0700); err != nil {
		return fmt.Errorf("element: create crypto dir %s: %w", cryptoDir, err)
	}

	cryptoDBPath := filepath.Join(cryptoDir, "crypto.sqlite")

	// Open pure-Go SQLite via modernc.org/sqlite (driver name "sqlite").
	// WAL mode + busy_timeout + foreign keys applied via DSN parameters.
	// MaxOpenConns=1 avoids WAL lock contention (single-writer pattern).
	rawDB, err := sql.Open("sqlite", fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL",
		cryptoDBPath,
	))
	if err != nil {
		return fmt.Errorf("element: open crypto sqlite %s: %w", cryptoDBPath, err)
	}
	rawDB.SetMaxOpenConns(1)

	// Wrap with dbutil so cryptohelper gets the *dbutil.Database it expects.
	// "sqlite" dialect maps positional $N params to ? (mautrix internal SQL uses $N).
	db, err := dbutil.NewWithDB(rawDB, "sqlite")
	if err != nil {
		_ = rawDB.Close()
		return fmt.Errorf("element: wrap crypto db: %w", err)
	}
	s.db = db

	helper, err := cryptohelper.NewCryptoHelper(mx, pickleBytes, db)
	if err != nil {
		_ = db.Close()
		s.db = nil
		return fmt.Errorf("element: build crypto helper: %w", err)
	}

	// Namespace multi-tenant crypto rows when multiple instances share a DB file.
	// We use a dedicated file per instance, but set it anyway for correctness.
	helper.DBAccountID = cfg.instanceID

	// Wire decrypt error callback BEFORE Init so failures are captured from the first sync.
	if s.decryptErrCallback != nil {
		helper.DecryptErrorCallback = s.decryptErrCallback
	}

	// Init upgrades the crypto DB schema, loads/generates the Olm account,
	// uploads device keys on first run, and registers the EventEncrypted sync handler.
	// On restart it reuses the persisted DeviceID and Olm account from the SQLite DB.
	if err := helper.Init(ctx); err != nil {
		_ = db.Close()
		s.db = nil
		return fmt.Errorf("element: crypto helper init: %w", err)
	}

	s.helper = helper
	s.mx = mx
	slog.Info("element: E2EE crypto helper initialised",
		"name", name, "crypto_db", cryptoDBPath)
	return nil
}

// Close releases the crypto helper's database connection.
func (s *goolmCryptoState) Close() error {
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		s.helper = nil
		s.mx = nil
		return err
	}
	return nil
}

// IsReady reports whether the crypto helper was successfully initialised.
func (s *goolmCryptoState) IsReady() bool {
	return s.helper != nil
}

// IsRoomEncrypted returns true if the given room has encryption enabled according
// to the StateStore populated by cryptohelper during /sync. Returns false on any
// error (safe default: fall back to plaintext send path).
//
// cryptohelper.NewCryptoHelper sets mx.StateStore to a crypto-compatible
// SQLStateStore (maunium.net/go/mautrix@v0.27.0/crypto/cryptohelper/cryptohelper.go:94).
// mautrix.StateStore.IsEncrypted signature: (ctx, roomID) (bool, error) — statestore.go:48.
func (s *goolmCryptoState) IsRoomEncrypted(ctx context.Context, roomID id.RoomID) bool {
	if s.helper == nil || s.mx == nil || s.mx.StateStore == nil {
		return false
	}
	encrypted, err := s.mx.StateStore.IsEncrypted(ctx, roomID)
	if err != nil {
		slog.Debug("element: IsRoomEncrypted lookup failed, defaulting to plaintext",
			"room_id", roomID, "error", err)
		return false
	}
	return encrypted
}

// EncryptMessage encrypts a message event content for the given room using the
// managed megolm session. Returns the encrypted content for transmission as
// event.EventEncrypted via SendMessageEvent. Returns nil, nil when the helper
// is not ready (treated as a signal to fall back to plaintext by the caller).
func (s *goolmCryptoState) EncryptMessage(ctx context.Context, roomID id.RoomID, content any) (*event.EncryptedEventContent, error) {
	if s.helper == nil {
		return nil, nil
	}
	enc, err := s.helper.Encrypt(ctx, roomID, event.EventMessage, content)
	if err != nil {
		return nil, fmt.Errorf("element: encrypt for room %s: %w", roomID, err)
	}
	return enc, nil
}

// LoadOrGenerateCrossSigning implements cryptoStateMachine.
//
// Import path (seeds.HasSeeds()):
//
//	mach.ImportCrossSigningKeys → mach.SignOwnDevice (re-sign current device on each restart)
//
// Generate path (!seeds.HasSeeds() && password != ""):
//
//	mach.GenerateAndUploadCrossSigningKeys (UIA with password) → ExportCrossSigningKeys → dirty=true
//
// No-op path (!seeds.HasSeeds() && password == ""):
//
//	returns empty seeds, dirty=false (caller logs warning and continues without cross-signing)
func (s *goolmCryptoState) LoadOrGenerateCrossSigning(
	ctx context.Context,
	password string,
	seeds localCrossSigningSeeds,
) (newSeeds localCrossSigningSeeds, dirty bool, err error) {

	if s.helper == nil {
		// Crypto not initialised (e.g. dataDir empty). No-op.
		return localCrossSigningSeeds{}, false, nil
	}

	mach := s.helper.Machine()
	if mach == nil {
		return localCrossSigningSeeds{}, false, nil
	}

	if seeds.HasSeeds() {
		// Import path: reconstruct cross-signing keys from stored seeds.
		// mautrix CrossSigningSeeds uses jsonbytes.UnpaddedURLBytes ([]byte backed by base64url).
		// localCrossSigningSeeds stores the same values as base64url strings; convert directly.
		importSeeds, decErr := localSeedsToMautrix(seeds)
		if decErr != nil {
			return localCrossSigningSeeds{}, false, fmt.Errorf("element: decode cross-signing seeds: %w", decErr)
		}

		if importErr := mach.ImportCrossSigningKeys(importSeeds); importErr != nil {
			return localCrossSigningSeeds{}, false, fmt.Errorf("element: import cross-signing keys: %w", importErr)
		}

		// Re-sign our own device on every restart so the self-signing signature stays fresh.
		ownDevice := mach.OwnIdentity()
		if ownDevice != nil {
			if signErr := mach.SignOwnDevice(ctx, ownDevice); signErr != nil {
				// Non-fatal: cross-signing keys loaded; signature upload may retry on next restart.
				slog.Warn("element: cross-signing sign own device failed (continuing)",
					"error", signErr)
			}
		}

		// Seeds unchanged — caller does not need to persist.
		return seeds, false, nil
	}

	// Generate path: attempt upload even with empty password.
	// MSC3967-capable homeservers (modern Synapse, MAS) accept the first cross-signing
	// upload without UIA — callback is never invoked. Older Synapse requires UIA;
	// the callback returns nil for empty password and the upload fails, which the
	// caller swallows as non-fatal.
	uiaCallback := buildUIAPasswordCallback(password, mach.Client.UserID.String())

	_, _, uploadErr := mach.GenerateAndUploadCrossSigningKeys(ctx, uiaCallback, "")
	if uploadErr != nil {
		return localCrossSigningSeeds{}, false, fmt.Errorf("element: generate/upload cross-signing keys: %w", uploadErr)
	}

	// Export seeds for persistence so restarts take the import path.
	exported := mach.ExportCrossSigningKeys()
	newSeeds = mautrixSeedsToLocal(exported)

	// Sign our own device with the freshly generated self-signing key.
	ownDevice := mach.OwnIdentity()
	if ownDevice != nil {
		if signErr := mach.SignOwnDevice(ctx, ownDevice); signErr != nil {
			slog.Warn("element: cross-signing sign own device after generate failed (continuing)",
				"error", signErr)
		}
	}

	return newSeeds, true, nil
}
