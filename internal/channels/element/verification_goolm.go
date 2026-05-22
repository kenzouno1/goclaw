//go:build !sqliteonly && goolm

package element

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// botVerificationCallbacks implements verificationhelper.RequiredCallbacks AND
// verificationhelper.ShowSASCallbacks. It auto-accepts SAS verification requests
// from users in allowFrom and blind-confirms the emoji compare step (no human
// operator to validate).
//
// The ONLY security gate here is the allowlist — operators MUST set
// allow_verify_from explicitly; empty allowlist disables verification.
type botVerificationCallbacks struct {
	allowFrom []string
	botUserID string                                  // bot's own Matrix user ID; used to reject self-verify
	helper    *verificationhelper.VerificationHelper  // back-ref for accept/start/confirm
}

// VerificationRequested handles incoming m.key.verification.request events.
// Rejects self-verify and non-allowlisted senders; auto-accepts the rest.
func (cb *botVerificationCallbacks) VerificationRequested(
	ctx context.Context,
	txnID id.VerificationTransactionID,
	from id.UserID,
	fromDevice id.DeviceID,
) {
	if string(from) == cb.botUserID {
		slog.Warn("element.verification.rejected_self_verify",
			"txn_id", txnID, "from_device", fromDevice)
		_ = cb.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeUser, "self-verify not supported")
		return
	}
	if !slices.Contains(cb.allowFrom, string(from)) {
		slog.Warn("element.verification.rejected_not_allowlisted",
			"sender", from, "txn_id", txnID)
		_ = cb.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeUser, "not in allowlist")
		return
	}
	slog.Info("element.verification.accepting", "sender", from, "txn_id", txnID)
	if err := cb.helper.AcceptVerification(ctx, txnID); err != nil {
		slog.Warn("element.verification.accept_failed", "error", err, "txn_id", txnID)
	}
}

// VerificationReady triggers SAS once both parties advertise capabilities.
// Cancels when the other party does not support SAS (QR-only is not handled).
func (cb *botVerificationCallbacks) VerificationReady(
	ctx context.Context,
	txnID id.VerificationTransactionID,
	otherDeviceID id.DeviceID,
	supportsSAS, _ bool,
	_ *verificationhelper.QRCode,
) {
	if !supportsSAS {
		slog.Warn("element.verification.no_sas_support", "txn_id", txnID)
		_ = cb.helper.CancelVerification(ctx, txnID, event.VerificationCancelCodeUser, "SAS method required")
		return
	}
	slog.Info("element.verification.starting_sas", "txn_id", txnID, "other_device", otherDeviceID)
	if err := cb.helper.StartSAS(ctx, txnID); err != nil {
		slog.Warn("element.verification.start_sas_failed", "error", err, "txn_id", txnID)
	}
}

// ShowSAS implements ShowSASCallbacks. The bot has no human to compare emojis,
// so it blind-confirms. Allowlist is the only mitigation; emojis are NOT logged.
func (cb *botVerificationCallbacks) ShowSAS(
	ctx context.Context,
	txnID id.VerificationTransactionID,
	_ []rune,
	_ []string,
	decimals []int,
) {
	slog.Info("element.verification.confirming_sas",
		"txn_id", txnID, "decimals", decimals)
	if err := cb.helper.ConfirmSAS(ctx, txnID); err != nil {
		slog.Warn("element.verification.confirm_failed", "error", err, "txn_id", txnID)
	}
}

// VerificationDone marks the transaction complete in the audit log.
func (cb *botVerificationCallbacks) VerificationDone(
	_ context.Context,
	txnID id.VerificationTransactionID,
	method event.VerificationMethod,
) {
	slog.Info("element.verification.done", "txn_id", txnID, "method", method)
}

// VerificationCancelled records a cancellation. mautrix internal cleanup handles
// per-transaction state release; no resources to free here.
func (cb *botVerificationCallbacks) VerificationCancelled(
	_ context.Context,
	txnID id.VerificationTransactionID,
	code event.VerificationCancelCode,
	reason string,
) {
	slog.Info("element.verification.cancelled", "txn_id", txnID, "code", code, "reason", reason)
}

// RegisterVerificationHandler installs an SAS auto-accept handler. Empty allowlist
// or uninitialised crypto state disables verification entirely (safe default).
func (s *goolmCryptoState) RegisterVerificationHandler(allowFrom []string, botUserID string) error {
	if s.helper == nil || s.mx == nil {
		return nil
	}
	if len(allowFrom) == 0 {
		slog.Info("element.verification.disabled_empty_allowlist")
		return nil
	}
	mach := s.helper.Machine()
	if mach == nil {
		return nil
	}
	cb := &botVerificationCallbacks{
		allowFrom: allowFrom,
		botUserID: botUserID,
	}
	store := verificationhelper.NewInMemoryVerificationStore()
	vh := verificationhelper.NewVerificationHelper(
		s.mx,
		mach,
		store,
		cb,
		false, // supportsQRShow
		false, // supportsQRScan
		true,  // supportsSAS
	)
	cb.helper = vh
	if err := vh.Init(context.Background()); err != nil {
		return fmt.Errorf("verification helper init: %w", err)
	}
	s.verificationHelper = vh
	slog.Info("element.verification.handler_registered",
		"bot_user_id", botUserID,
		"allow_count", len(allowFrom),
	)
	return nil
}
