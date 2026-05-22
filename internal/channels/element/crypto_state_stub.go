//go:build !sqliteonly && !goolm

package element

import (
	"context"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// stubCryptoState is the no-op cryptoStateMachine used when the goolm build tag
// is absent (e.g. plain `go build ./...` or CGO-disabled builds without -tags goolm).
// E2EE is silently disabled; the channel still works for plaintext Matrix rooms.
type stubCryptoState struct{}

func newCryptoState() cryptoStateMachine { return stubCryptoState{} }

func (stubCryptoState) Init(_ context.Context, _ *mautrix.Client, _ elementConfig, _ string) error {
	return nil
}

func (stubCryptoState) Close() error { return nil }

func (stubCryptoState) IsReady() bool { return false }

func (stubCryptoState) IsRoomEncrypted(_ context.Context, _ id.RoomID) bool { return false }

func (stubCryptoState) EncryptMessage(_ context.Context, _ id.RoomID, _ any) (*event.EncryptedEventContent, error) {
	return nil, nil
}

func (stubCryptoState) SetDecryptErrorCallback(_ func(*event.Event, error)) {}

func (stubCryptoState) LoadOrGenerateCrossSigning(_ context.Context, _ string, seeds localCrossSigningSeeds) (localCrossSigningSeeds, bool, error) {
	return seeds, false, nil
}

func (stubCryptoState) RegisterVerificationHandler(_ []string, _ string) error { return nil }
