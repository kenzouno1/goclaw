//go:build !sqliteonly && goolm

package element

import (
	"bytes"
	"encoding/base64"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
)

func TestSeedRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{0xA1}, 32)
	self := bytes.Repeat([]byte{0xB2}, 32)
	user := bytes.Repeat([]byte{0xC3}, 32)
	orig := crypto.CrossSigningSeeds{
		MasterKey:      master,
		SelfSigningKey: self,
		UserSigningKey: user,
	}

	local := mautrixSeedsToLocal(orig)
	if local.MasterKey == "" || local.SelfSigningKey == "" || local.UserSigningKey == "" {
		t.Fatalf("encode produced empty fields: %+v", local)
	}

	decoded, err := localSeedsToMautrix(local)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded.MasterKey, master) {
		t.Errorf("master roundtrip mismatch")
	}
	if !bytes.Equal(decoded.SelfSigningKey, self) {
		t.Errorf("self_signing roundtrip mismatch")
	}
	if !bytes.Equal(decoded.UserSigningKey, user) {
		t.Errorf("user_signing roundtrip mismatch")
	}
}

func TestLocalSeedsToMautrix_InvalidBase64(t *testing.T) {
	good := base64.RawURLEncoding.EncodeToString([]byte("abcd"))

	cases := []struct {
		name  string
		seeds localCrossSigningSeeds
	}{
		{"master invalid", localCrossSigningSeeds{MasterKey: "@@@", SelfSigningKey: good, UserSigningKey: good}},
		{"self invalid", localCrossSigningSeeds{MasterKey: good, SelfSigningKey: "@@@", UserSigningKey: good}},
		{"user invalid", localCrossSigningSeeds{MasterKey: good, SelfSigningKey: good, UserSigningKey: "@@@"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := localSeedsToMautrix(tc.seeds); err == nil {
				t.Errorf("expected error for invalid base64, got nil")
			}
		})
	}
}

func TestUIACallback_PasswordEmptyReturnsNil(t *testing.T) {
	cb := buildUIAPasswordCallback("", "@bot:example.com")
	got := cb(&mautrix.RespUserInteractive{
		Flows:   []mautrix.UIAFlow{{Stages: []mautrix.AuthType{mautrix.AuthTypePassword}}},
		Session: "sess1",
	})
	if got != nil {
		t.Errorf("expected nil when password empty, got %+v", got)
	}
}

func TestUIACallback_NilRespReturnsNil(t *testing.T) {
	cb := buildUIAPasswordCallback("hunter2", "@bot:example.com")
	if got := cb(nil); got != nil {
		t.Errorf("expected nil on nil RespUserInteractive, got %+v", got)
	}
}

func TestUIACallback_PasswordFlowMatched(t *testing.T) {
	cb := buildUIAPasswordCallback("hunter2", "@bot:example.com")
	got := cb(&mautrix.RespUserInteractive{
		Flows:   []mautrix.UIAFlow{{Stages: []mautrix.AuthType{mautrix.AuthTypePassword}}},
		Session: "sess1",
	})
	resp, ok := got.(*mautrix.ReqUIAuthLogin)
	if !ok {
		t.Fatalf("expected *ReqUIAuthLogin, got %T", got)
	}
	if resp.Type != mautrix.AuthTypePassword {
		t.Errorf("expected type m.login.password, got %s", resp.Type)
	}
	if resp.Session != "sess1" {
		t.Errorf("expected session sess1, got %s", resp.Session)
	}
	if resp.User != "@bot:example.com" {
		t.Errorf("expected user @bot:example.com, got %s", resp.User)
	}
	if resp.Password != "hunter2" {
		t.Errorf("expected password hunter2, got %s", resp.Password)
	}
}

func TestUIACallback_PasswordStageAlreadyCompleted(t *testing.T) {
	cb := buildUIAPasswordCallback("hunter2", "@bot:example.com")
	got := cb(&mautrix.RespUserInteractive{
		Flows:     []mautrix.UIAFlow{{Stages: []mautrix.AuthType{mautrix.AuthTypePassword}}},
		Completed: []string{string(mautrix.AuthTypePassword)},
		Session:   "sess1",
	})
	if got != nil {
		t.Errorf("expected nil when password stage already completed, got %+v", got)
	}
}

func TestUIACallback_UnknownStageOnly(t *testing.T) {
	cb := buildUIAPasswordCallback("hunter2", "@bot:example.com")
	got := cb(&mautrix.RespUserInteractive{
		Flows:   []mautrix.UIAFlow{{Stages: []mautrix.AuthType{mautrix.AuthTypeSSO}}},
		Session: "sess1",
	})
	if got != nil {
		t.Errorf("expected nil when no password stage, got %+v", got)
	}
}

func TestUIACallback_MultiStageWithPassword(t *testing.T) {
	cb := buildUIAPasswordCallback("hunter2", "@bot:example.com")
	got := cb(&mautrix.RespUserInteractive{
		Flows: []mautrix.UIAFlow{
			{Stages: []mautrix.AuthType{mautrix.AuthTypeSSO}},
			{Stages: []mautrix.AuthType{mautrix.AuthTypePassword}},
		},
		Session: "sess1",
	})
	if _, ok := got.(*mautrix.ReqUIAuthLogin); !ok {
		t.Fatalf("expected ReqUIAuthLogin when one flow has password, got %T", got)
	}
}
