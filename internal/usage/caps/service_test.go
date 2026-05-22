package caps

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestShouldEnforceProvider(t *testing.T) {
	cases := []struct {
		providerType string
		hasKey       bool
		want         bool
	}{
		{store.ProviderChatGPTOAuth, false, false},
		{store.ProviderClaudeCLI, false, false},
		{store.ProviderBailian, false, false},
		{store.ProviderOllama, false, false},
		{store.ProviderACP, false, false},
		{store.ProviderOpenAICompat, true, true},
		{store.ProviderOpenRouter, true, true},
		{store.ProviderOpenRouter, false, false},
	}
	for _, tc := range cases {
		if got := ShouldEnforceProvider(tc.providerType, tc.hasKey); got != tc.want {
			t.Fatalf("ShouldEnforceProvider(%q,%v) = %v, want %v", tc.providerType, tc.hasKey, got, tc.want)
		}
	}
}

func TestPreflightTokenOnlyCapDoesNotRequirePricing(t *testing.T) {
	providerID := uuid.New()
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxTokens: int64Ptr(1000), Enabled: true}
	usageStore := &fakeUsageCapStore{policies: []store.UsageCapPolicy{policy}, resolveErr: sql.ErrNoRows}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: providerID},
		Name:         "openrouter",
		ProviderType: store.ProviderOpenRouter,
		APIKey:       "sk-test",
	}}
	svc := NewService(usageStore, providerStore)

	reservation, err := svc.Preflight(context.Background(), Request{
		TenantID: policy.TenantID, ProviderName: "openrouter", ModelID: "missing/model",
		ReservationKey: "token-only", Messages: []providers.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	if reservation == nil || reservation.skipped {
		t.Fatalf("Preflight skipped token-only policy")
	}
	if usageStore.resolveCalls != 0 {
		t.Fatalf("ResolvePricing called %d time(s), want 0", usageStore.resolveCalls)
	}
	if usageStore.reserved.EstimatedCostMicros != 0 {
		t.Fatalf("EstimatedCostMicros = %d, want 0", usageStore.reserved.EstimatedCostMicros)
	}
}

func TestPreflightIncludesRequestPricingWhenConfigured(t *testing.T) {
	zero := "0"
	requestPrice := "0.01"
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxCostMicros: int64Ptr(20_000), Enabled: true}
	usageStore := &fakeUsageCapStore{
		policies: []store.UsageCapPolicy{policy},
		resolved: &store.ResolvedUsagePricing{
			ModelID: "priced/model",
			Source:  "catalog",
			Pricing: store.UsagePricingFields{Input: &zero, Output: &zero, Request: &requestPrice},
		},
	}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "openrouter",
		ProviderType: store.ProviderOpenRouter,
		APIKey:       "sk-test",
	}}
	svc := NewService(usageStore, providerStore)

	_, err := svc.Preflight(context.Background(), Request{
		TenantID: policy.TenantID, ProviderName: "openrouter", ModelID: "priced/model",
		ReservationKey: "request-fee", Messages: []providers.Message{{Role: "user", Content: "abcd"}},
		MaxOutputTokens: 1,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	if got := usageStore.reserved.EstimatedCostMicros; got != 10_000 {
		t.Fatalf("EstimatedCostMicros = %d, want 10000", got)
	}
}

func TestPreflightFallsBackToMasterProviderMetadata(t *testing.T) {
	tenantID := uuid.New()
	masterProviderID := uuid.New()
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: tenantID, MaxTokens: int64Ptr(1000), Enabled: true}
	usageStore := &fakeUsageCapStore{policies: []store.UsageCapPolicy{policy}}
	providerStore := &fakeProviderStore{
		masterProvider: &store.LLMProviderData{
			BaseModel:    store.BaseModel{ID: masterProviderID},
			TenantID:     store.MasterTenantID,
			Name:         "openrouter",
			ProviderType: store.ProviderOpenRouter,
			APIKey:       "sk-test",
		},
	}
	svc := NewService(usageStore, providerStore)

	reservation, err := svc.Preflight(store.WithTenantID(context.Background(), tenantID), Request{
		TenantID: tenantID, ProviderName: "openrouter", ModelID: "openai/gpt-test",
		ReservationKey: "master-provider", Messages: []providers.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	if reservation == nil || reservation.skipped {
		t.Fatal("Preflight skipped master provider fallback")
	}
	if usageStore.reserved.ProviderID != masterProviderID {
		t.Fatalf("ProviderID = %s, want %s", usageStore.reserved.ProviderID, masterProviderID)
	}
}

func TestReservationReconcileUsesDetachedContext(t *testing.T) {
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxTokens: int64Ptr(1000), Enabled: true}
	usageStore := &fakeUsageCapStore{policies: []store.UsageCapPolicy{policy}}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "openrouter",
		ProviderType: store.ProviderOpenRouter,
		APIKey:       "sk-test",
	}}
	svc := NewService(usageStore, providerStore)
	reservation, err := svc.Preflight(context.Background(), Request{
		TenantID: policy.TenantID, ProviderName: "openrouter", ModelID: "token/model",
		ReservationKey: "reconcile", Messages: []providers.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reservation.Reconcile(ctx, &providers.ChatResponse{Usage: &providers.Usage{PromptTokens: 2, CompletionTokens: 3}}, nil)

	if usageStore.reconcileCalls != 1 {
		t.Fatalf("ReconcileUsage calls = %d, want 1", usageStore.reconcileCalls)
	}
	if usageStore.reconcileCtxCanceled {
		t.Fatal("ReconcileUsage received canceled context")
	}
}

func TestReservationReconcileStreamKeepsEstimateAfterPartialError(t *testing.T) {
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxTokens: int64Ptr(1000), Enabled: true}
	usageStore := &fakeUsageCapStore{policies: []store.UsageCapPolicy{policy}}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "openrouter",
		ProviderType: store.ProviderOpenRouter,
		APIKey:       "sk-test",
	}}
	svc := NewService(usageStore, providerStore)
	reservation, err := svc.Preflight(context.Background(), Request{
		TenantID: policy.TenantID, ProviderName: "openrouter", ModelID: "token/model",
		ReservationKey: "partial-stream", Messages: []providers.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}

	reservation.ReconcileStream(context.Background(), nil, context.Canceled, true)

	if usageStore.reconciled.ActualTokens == 0 {
		t.Fatal("ReconcileStream zeroed actual tokens after partial stream error")
	}
	if usageStore.reconciled.Status != "failed" {
		t.Fatalf("Status = %q, want failed", usageStore.reconciled.Status)
	}
}

func TestReservationReconcileIgnoresUnpricedRequestCount(t *testing.T) {
	tokenPrice := "0.000001"
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxCostMicros: int64Ptr(1_000_000), Enabled: true}
	usageStore := &fakeUsageCapStore{
		policies: []store.UsageCapPolicy{policy},
		resolved: &store.ResolvedUsagePricing{
			ModelID: "priced/model",
			Source:  "catalog",
			Pricing: store.UsagePricingFields{Input: &tokenPrice, Output: &tokenPrice},
		},
	}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "openrouter",
		ProviderType: store.ProviderOpenRouter,
		APIKey:       "sk-test",
	}}
	svc := NewService(usageStore, providerStore)
	reservation, err := svc.Preflight(context.Background(), Request{
		TenantID: policy.TenantID, ProviderName: "openrouter", ModelID: "priced/model",
		ReservationKey: "unpriced-request", Messages: []providers.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 100,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}

	reservation.Reconcile(context.Background(), &providers.ChatResponse{Usage: &providers.Usage{
		PromptTokens:     2,
		CompletionTokens: 3,
		RequestCount:     1,
	}}, nil)

	if usageStore.reconciled.ActualCostMicros != 5 {
		t.Fatalf("ActualCostMicros = %d, want 5", usageStore.reconciled.ActualCostMicros)
	}
}

func TestCountImagesOnlyCountsImageMIMEs(t *testing.T) {
	messages := []providers.Message{{
		Role: "user",
		Images: []providers.ImageContent{
			{MimeType: "image/png"},
			{MimeType: "application/pdf"},
			{MimeType: "video/mp4"},
		},
	}}
	if got := CountImages(messages); got != 1 {
		t.Fatalf("CountImages = %d, want 1", got)
	}
}

type fakeUsageCapStore struct {
	policies             []store.UsageCapPolicy
	resolved             *store.ResolvedUsagePricing
	resolveErr           error
	resolveCalls         int
	reserved             store.UsageReserveRequest
	reconciled           store.UsageReconcileRequest
	reconcileCalls       int
	reconcileCtxCanceled bool
}

func (s *fakeUsageCapStore) UpsertPricingCatalog(context.Context, []store.UsagePricingCatalogEntry) (int, error) {
	return 0, nil
}
func (s *fakeUsageCapStore) ListPricingCatalog(context.Context, store.UsagePricingQuery) ([]store.UsagePricingCatalogEntry, error) {
	return nil, nil
}
func (s *fakeUsageCapStore) PutPricingOverride(context.Context, *store.UsagePricingOverride) error {
	return nil
}
func (s *fakeUsageCapStore) ListPricingOverrides(context.Context, store.UsagePricingQuery) ([]store.UsagePricingOverride, error) {
	return nil, nil
}
func (s *fakeUsageCapStore) DeletePricingOverride(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *fakeUsageCapStore) ResolvePricing(context.Context, uuid.UUID, uuid.UUID, string, string, string) (*store.ResolvedUsagePricing, error) {
	s.resolveCalls++
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.resolved, nil
}
func (s *fakeUsageCapStore) CreateUsageCapPolicy(context.Context, *store.UsageCapPolicy) error {
	return nil
}
func (s *fakeUsageCapStore) ListUsageCapPolicies(context.Context, store.UsageCapScope, bool) ([]store.UsageCapPolicy, error) {
	return s.policies, nil
}
func (s *fakeUsageCapStore) UpdateUsageCapPolicy(context.Context, uuid.UUID, uuid.UUID, store.UsageCapPolicyPatch) (*store.UsageCapPolicy, error) {
	return nil, nil
}
func (s *fakeUsageCapStore) DeleteUsageCapPolicy(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *fakeUsageCapStore) ReserveUsage(_ context.Context, req store.UsageReserveRequest, policies []store.UsageCapPolicy) (*store.UsageReservationResult, error) {
	s.reserved = req
	return &store.UsageReservationResult{ReservationKey: req.ReservationKey, Policies: policies}, nil
}
func (s *fakeUsageCapStore) ReconcileUsage(ctx context.Context, req store.UsageReconcileRequest) error {
	s.reconciled = req
	s.reconcileCalls++
	s.reconcileCtxCanceled = ctx.Err() != nil
	return nil
}
func (s *fakeUsageCapStore) ListUsageCapUtilization(context.Context, uuid.UUID) ([]store.UsageCapUtilization, error) {
	return nil, nil
}
func (s *fakeUsageCapStore) ListUsageCapEvents(context.Context, uuid.UUID, int) ([]store.UsageCapEvent, error) {
	return nil, nil
}
func (s *fakeUsageCapStore) InsertUsageCapEvent(context.Context, *store.UsageCapEvent) error {
	return nil
}

type fakeProviderStore struct {
	provider       *store.LLMProviderData
	masterProvider *store.LLMProviderData
}

func (s *fakeProviderStore) CreateProvider(context.Context, *store.LLMProviderData) error { return nil }
func (s *fakeProviderStore) GetProvider(context.Context, uuid.UUID) (*store.LLMProviderData, error) {
	return s.provider, nil
}
func (s *fakeProviderStore) GetProviderByName(ctx context.Context, _ string) (*store.LLMProviderData, error) {
	if store.TenantIDFromContext(ctx) == store.MasterTenantID && s.masterProvider != nil {
		return s.masterProvider, nil
	}
	if s.provider == nil {
		return nil, sql.ErrNoRows
	}
	return s.provider, nil
}
func (s *fakeProviderStore) ListProviders(context.Context) ([]store.LLMProviderData, error) {
	return nil, nil
}
func (s *fakeProviderStore) ListAllProviders(context.Context) ([]store.LLMProviderData, error) {
	return nil, nil
}
func (s *fakeProviderStore) UpdateProvider(context.Context, uuid.UUID, map[string]any) error {
	return nil
}
func (s *fakeProviderStore) DeleteProvider(context.Context, uuid.UUID) error { return nil }

func int64Ptr(v int64) *int64 { return &v }
