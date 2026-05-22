//go:build !sqliteonly && goolm

package element

import (
	"encoding/base64"
	"fmt"
	"slices"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
)

// buildUIAPasswordCallback builds a User-Interactive Auth callback that completes
// the m.login.password stage when present in a flow.
//
// Returns nil (no completion) when:
//   - password is empty
//   - no flow advertises m.login.password as a remaining stage
//
// MSC3967-capable Synapse versions no longer require UIA for the first cross-signing
// upload, so this callback is only invoked on older homeservers / MAS deployments.
func buildUIAPasswordCallback(password, userID string) mautrix.UIACallback {
	return func(uia *mautrix.RespUserInteractive) any {
		if password == "" || uia == nil {
			return nil
		}
		for _, flow := range uia.Flows {
			if !hasRemainingStage(flow, mautrix.AuthTypePassword, uia.Completed) {
				continue
			}
			return &mautrix.ReqUIAuthLogin{
				BaseAuthData: mautrix.BaseAuthData{
					Type:    mautrix.AuthTypePassword,
					Session: uia.Session,
				},
				User:     userID,
				Password: password,
			}
		}
		return nil
	}
}

// hasRemainingStage reports whether the given flow contains stageType among its
// stages that have not yet been completed.
func hasRemainingStage(flow mautrix.UIAFlow, stageType mautrix.AuthType, completed []string) bool {
	for _, stage := range flow.Stages {
		if stage != stageType {
			continue
		}
		if slices.Contains(completed, string(stage)) {
			continue
		}
		return true
	}
	return false
}

// localSeedsToMautrix decodes the three base64url-encoded seed strings into
// mautrix CrossSigningSeeds (byte-backed). All three seeds must be present;
// any decode failure aborts.
func localSeedsToMautrix(s localCrossSigningSeeds) (crypto.CrossSigningSeeds, error) {
	master, err := base64.RawURLEncoding.DecodeString(s.MasterKey)
	if err != nil {
		return crypto.CrossSigningSeeds{}, fmt.Errorf("master: %w", err)
	}
	self, err := base64.RawURLEncoding.DecodeString(s.SelfSigningKey)
	if err != nil {
		return crypto.CrossSigningSeeds{}, fmt.Errorf("self_signing: %w", err)
	}
	user, err := base64.RawURLEncoding.DecodeString(s.UserSigningKey)
	if err != nil {
		return crypto.CrossSigningSeeds{}, fmt.Errorf("user_signing: %w", err)
	}
	return crypto.CrossSigningSeeds{
		MasterKey:      master,
		SelfSigningKey: self,
		UserSigningKey: user,
	}, nil
}

// mautrixSeedsToLocal encodes the three byte-backed seeds into base64url strings.
func mautrixSeedsToLocal(s crypto.CrossSigningSeeds) localCrossSigningSeeds {
	return localCrossSigningSeeds{
		MasterKey:      base64.RawURLEncoding.EncodeToString(s.MasterKey),
		SelfSigningKey: base64.RawURLEncoding.EncodeToString(s.SelfSigningKey),
		UserSigningKey: base64.RawURLEncoding.EncodeToString(s.UserSigningKey),
	}
}
