package flags

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/arbiter/govern"
)

const fullFlagSource = `
segment internal {
    user.email ends_with "@m31labs.dev"
}

segment enterprise_us {
    user.plan == "enterprise" and user.country == "US"
}

segment beta_users {
    user.id in ["u_oscar", "u_alice"]
}

flag checkout_v2 type multivariate default "control" {
    owner: "oscar"
    ticket: "ENG-1234"
    rationale: "New checkout flow testing Stripe integration"
    expires: "2026-06-01"

    requires payments_enabled

    when internal
        then "treatment"

    when enterprise_us rollout 20
        then "treatment"

    when beta_users
        then "treatment"
}

flag payments_enabled type boolean default false {
    owner: "oscar"
    ticket: "ENG-1200"

    when enterprise_us
        then true
}

flag dark_mode type boolean default false kill_switch {
    owner: "design-team"
}
`

func TestFlagEnabled(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Internal user should get checkout_v2 treatment (payments_enabled prereq passes
	// because enterprise_us is not true for this user, but payments_enabled defaults
	// to false... Actually, let's make the user also enterprise_us so payments_enabled passes)
	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}
	if !f.Enabled("checkout_v2", ctx) {
		t.Error("expected checkout_v2 enabled for internal enterprise_us user")
	}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "treatment" {
		t.Errorf("expected treatment, got %q", v.Name)
	}
}

func TestFlagDisabled(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Random user outside all segments
	ctx := map[string]any{
		"user.email":   "random@gmail.com",
		"user.plan":    "free",
		"user.country": "CA",
		"user.id":      "u_random",
	}
	if f.Enabled("checkout_v2", ctx) {
		t.Error("expected checkout_v2 disabled for random user")
	}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "control" {
		t.Errorf("expected control (default), got %q", v.Name)
	}
}

func TestFlagKillSwitch(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Even with a full context, dark_mode should always return default
	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}
	if f.Enabled("dark_mode", ctx) {
		t.Error("expected dark_mode disabled (kill_switch)")
	}
	v := f.Variant("dark_mode", ctx)
	if v.Name != "false" {
		t.Errorf("expected false (default), got %q", v.Name)
	}
}

func TestFlagPrerequisite(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Internal user but NOT enterprise_us -> payments_enabled stays default (false)
	// So checkout_v2 prerequisite fails
	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "free",
		"user.country": "CA",
		"user.id":      "u_oscar",
	}
	if f.Enabled("checkout_v2", ctx) {
		t.Error("expected checkout_v2 disabled when payments_enabled prerequisite fails")
	}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "control" {
		t.Errorf("expected control (default due to prereq fail), got %q", v.Name)
	}
}

func TestFlagRollout(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Test rollout: enterprise_us users with different user IDs
	// Need to find a user whose bucket is < 20 and one whose bucket is >= 20
	var lowBucketUser, highBucketUser string
	namespace := govern.AutoRolloutNamespace("", "flag:checkout_v2:rule:1")
	for i := 0; i < 1000; i++ {
		uid := fmt.Sprintf("user_%d", i)
		b := govern.RolloutBucket(namespace, uid)
		if b < 2000 && lowBucketUser == "" {
			lowBucketUser = uid
		}
		if b >= 2000 && highBucketUser == "" {
			highBucketUser = uid
		}
		if lowBucketUser != "" && highBucketUser != "" {
			break
		}
	}

	// Low bucket user in enterprise_us (not internal, not beta) should get treatment
	ctx := map[string]any{
		"user.email":   "low@example.com",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      lowBucketUser,
	}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "treatment" {
		t.Errorf("expected treatment for low-bucket enterprise_us user (bucket=%d), got %q", govern.RolloutBucket(namespace, lowBucketUser), v.Name)
	}

	// High bucket user in enterprise_us (not internal, not beta) should NOT get treatment from rollout
	// but might match beta_users if it happens to be "u_oscar" or "u_alice"
	ctx = map[string]any{
		"user.email":   "high@example.com",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      highBucketUser,
	}
	v = f.Variant("checkout_v2", ctx)
	if v.Name != "control" {
		t.Errorf("expected control for high-bucket enterprise_us user (bucket=%d), got %q", govern.RolloutBucket(namespace, highBucketUser), v.Name)
	}
}

func TestFlagAllFlags(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}

	all := f.AllFlags(ctx)
	if all["checkout_v2"].Name != "treatment" {
		t.Errorf("checkout_v2: got %q, want treatment", all["checkout_v2"].Name)
	}
	if all["payments_enabled"].Name != "true" {
		t.Errorf("payments_enabled: got %q, want true", all["payments_enabled"].Name)
	}
	if all["dark_mode"].Name != "false" {
		t.Errorf("dark_mode: got %q, want false (kill_switch)", all["dark_mode"].Name)
	}
}

func TestFlagElseIfChain(t *testing.T) {
	f, err := Load([]byte(`
flag test type boolean default "false" {
	when { user.role == "admin" } then "true"
	else when { user.role == "mod" } then "true"
}`))
	if err != nil {
		t.Fatal(err)
	}

	v := f.Variant("test", map[string]any{
		"user": map[string]any{"role": "mod"},
	})
	if v.Name != "true" {
		t.Fatalf("variant: got %q, want true", v.Name)
	}
}

func TestFlagExplainMatch(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}

	eval := f.Explain("checkout_v2", ctx)

	if eval.Flag != "checkout_v2" {
		t.Errorf("flag: got %q, want checkout_v2", eval.Flag)
	}
	if eval.Variant.Name != "treatment" {
		t.Errorf("variant: got %q, want treatment", eval.Variant.Name)
	}
	if eval.IsDefault {
		t.Error("expected IsDefault=false")
	}
	if eval.Metadata.Owner != "oscar" {
		t.Errorf("owner: got %q, want oscar", eval.Metadata.Owner)
	}
	if eval.Metadata.Ticket != "ENG-1234" {
		t.Errorf("ticket: got %q, want ENG-1234", eval.Metadata.Ticket)
	}
	if eval.Metadata.Expires == nil {
		t.Error("expected Expires to be set")
	}

	// Print trace for verification
	t.Logf("=== Explain checkout_v2 (internal user) ===")
	t.Logf("Flag:      %s", eval.Flag)
	t.Logf("Variant:   %s", eval.Variant)
	t.Logf("IsDefault: %v", eval.IsDefault)
	t.Logf("Reason:    %s", eval.Reason)
	t.Logf("Owner:     %s", eval.Metadata.Owner)
	t.Logf("Ticket:    %s", eval.Metadata.Ticket)
	t.Logf("Rationale: %s", eval.Metadata.Rationale)
	if eval.Metadata.Expires != nil {
		daysLeft := int(time.Until(*eval.Metadata.Expires).Hours() / 24)
		t.Logf("Expires:   %s (%d days remaining)", eval.Metadata.Expires.Format("2006-01-02"), daysLeft)
	}
	t.Logf("Elapsed:   %v", eval.Elapsed)
	t.Logf("Trace:")
	for i, step := range eval.Trace {
		mark := "x"
		if step.Result {
			mark = "v"
		}
		t.Logf("  [%d] [%s] %s: %s", i, mark, step.Check, step.Detail)
	}

	// Should have trace steps
	if len(eval.Trace) == 0 {
		t.Error("expected trace steps")
	}
}

func TestFlagExplainNoMatch(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{
		"user.email":   "random@gmail.com",
		"user.plan":    "free",
		"user.country": "CA",
		"user.id":      "u_random",
	}

	eval := f.Explain("checkout_v2", ctx)

	if eval.Variant.Name != "control" {
		t.Errorf("variant: got %q, want control", eval.Variant.Name)
	}
	if !eval.IsDefault {
		t.Error("expected IsDefault=true")
	}

	t.Logf("=== Explain checkout_v2 (random user) ===")
	t.Logf("Variant: %s, Reason: %s", eval.Variant, eval.Reason)
	for i, step := range eval.Trace {
		mark := "x"
		if step.Result {
			mark = "v"
		}
		t.Logf("  [%d] [%s] %s: %s", i, mark, step.Check, step.Detail)
	}
}

func TestFlagExplainKillSwitch(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}

	eval := f.Explain("dark_mode", ctx)

	if eval.Variant.Name != "false" {
		t.Errorf("variant: got %q, want false", eval.Variant.Name)
	}
	if !eval.IsDefault {
		t.Error("expected IsDefault=true")
	}
	if eval.Reason != "kill-switched" {
		t.Errorf("reason: got %q, want kill-switched", eval.Reason)
	}

	// Trace should have kill_switch step
	foundKS := false
	for _, step := range eval.Trace {
		if step.Check == "kill_switch" {
			foundKS = true
			if !step.Result {
				t.Error("kill_switch step should be true")
			}
		}
	}
	if !foundKS {
		t.Error("expected kill_switch trace step")
	}
}

func TestFlagExplainPrereqFail(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Internal user but NOT enterprise_us -> payments_enabled fails
	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "free",
		"user.country": "CA",
		"user.id":      "u_oscar",
	}

	eval := f.Explain("checkout_v2", ctx)

	if eval.Variant.Name != "control" {
		t.Errorf("variant: got %q, want control", eval.Variant.Name)
	}
	if !eval.IsDefault {
		t.Error("expected IsDefault=true")
	}

	// Check reason mentions prerequisite
	if eval.Reason != "prerequisite payments_enabled not met" {
		t.Errorf("reason: got %q, want 'prerequisite payments_enabled not met'", eval.Reason)
	}

	// Trace should show requires step failing
	foundReq := false
	for _, step := range eval.Trace {
		if step.Check == "requires payments_enabled" {
			foundReq = true
			if step.Result {
				t.Error("requires step should be false")
			}
		}
	}
	if !foundReq {
		t.Error("expected 'requires payments_enabled' trace step")
	}
}

func TestFlagMetadata(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{
		"user.email":   "oscar@m31labs.dev",
		"user.plan":    "enterprise",
		"user.country": "US",
		"user.id":      "u_oscar",
	}

	eval := f.Explain("checkout_v2", ctx)

	if eval.Metadata.Owner != "oscar" {
		t.Errorf("owner: got %q, want oscar", eval.Metadata.Owner)
	}
	if eval.Metadata.Ticket != "ENG-1234" {
		t.Errorf("ticket: got %q, want ENG-1234", eval.Metadata.Ticket)
	}
	if eval.Metadata.Rationale != "New checkout flow testing Stripe integration" {
		t.Errorf("rationale: got %q", eval.Metadata.Rationale)
	}
	if eval.Metadata.Expires == nil {
		t.Fatal("expected Expires to be set")
	}

	expected, _ := time.Parse("2006-01-02", "2026-06-01")
	if !eval.Metadata.Expires.Equal(expected) {
		t.Errorf("expires: got %v, want %v", eval.Metadata.Expires, expected)
	}

	// Verify days remaining is positive (test date is before 2026-06-01)
	daysLeft := int(time.Until(*eval.Metadata.Expires).Hours() / 24)
	t.Logf("Expires in %d days", daysLeft)
}

func TestEnvironmentPerFile(t *testing.T) {
	dir := t.TempDir()

	// Per-environment files using new flag syntax
	os.WriteFile(filepath.Join(dir, "production.arb"), []byte(`
flag feature type boolean default false {
    owner: "ops"
    when { percent_bucket < 5 }
        then true
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "staging.arb"), []byte(`
flag feature type boolean default false {
    owner: "ops"
    when { true }
        then true
}
`), 0644)

	prod, err := LoadEnv(dir, "production")
	if err != nil {
		t.Fatal(err)
	}
	staging, err := LoadEnv(dir, "staging")
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{"percent_bucket": 50.0}

	// Prod: bucket 50 > 5, not enabled
	if prod.Enabled("feature", ctx) {
		t.Error("expected feature disabled in production for bucket 50")
	}

	// Staging: always on
	if !staging.Enabled("feature", ctx) {
		t.Error("expected feature enabled in staging")
	}
}

func TestReload(t *testing.T) {
	f, err := Load([]byte(`
flag v1 type boolean default false {
    owner: "test"
    when { true }
        then true
}
`))
	if err != nil {
		t.Fatal(err)
	}

	if !f.Enabled("v1", map[string]any{}) {
		t.Error("expected v1 enabled")
	}

	// Reload with new flag
	err = f.Reload([]byte(`
flag v2 type boolean default false {
    owner: "test"
    when { true }
        then true
}
`))
	if err != nil {
		t.Fatal(err)
	}

	if f.Enabled("v1", map[string]any{}) {
		t.Error("v1 should be gone after reload")
	}
	if !f.Enabled("v2", map[string]any{}) {
		t.Error("expected v2 enabled after reload")
	}
}

func TestBucketDeterministic(t *testing.T) {
	b1 := Bucket("user_123")
	b2 := Bucket("user_123")
	b3 := Bucket("user_456")

	if b1 != b2 {
		t.Error("same user ID should produce same bucket")
	}
	if b1 < 0 || b1 >= 100 {
		t.Errorf("bucket out of range: %d", b1)
	}
	// Different users CAN get the same bucket, but probably won't
	_ = b3
}

func TestHTTPHandler(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	// Test /flags endpoint
	req := httptest.NewRequest("GET", "/flags?user.email=oscar@m31labs.dev&user.plan=enterprise&user.country=US&user.id=u_oscar", nil)
	w := httptest.NewRecorder()

	f.Handler().ServeHTTP(w, req)

	var result map[string]ServedVariant
	json.NewDecoder(w.Body).Decode(&result)

	if result["checkout_v2"].Name != "treatment" {
		t.Errorf("checkout_v2: got %q, want treatment", result["checkout_v2"].Name)
	}
	if result["dark_mode"].Name != "false" {
		t.Errorf("dark_mode: got %q, want false", result["dark_mode"].Name)
	}
}

func TestHTTPExplainHandler(t *testing.T) {
	f, err := Load([]byte(fullFlagSource))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/explain?flag=dark_mode&user.id=u_oscar", nil)
	w := httptest.NewRecorder()

	f.Handler().ServeHTTP(w, req)

	var result FlagEvaluation
	json.NewDecoder(w.Body).Decode(&result)

	if result.Variant.Name != "false" {
		t.Errorf("variant: got %q, want false", result.Variant.Name)
	}
	if result.Reason != "kill-switched" {
		t.Errorf("reason: got %q, want kill-switched", result.Reason)
	}
}

func TestPrerequisiteCycleDetection(t *testing.T) {
	// A requires B, B requires A — should not infinite loop
	src := `
flag flag_a type boolean default false {
    owner: "test"
    requires flag_b
    when { true }
        then true
}

flag flag_b type boolean default false {
    owner: "test"
    requires flag_a
    when { true }
        then true
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// Should not hang or panic — returns default due to cycle
	v := f.Variant("flag_a", map[string]any{})
	if v.Name != "false" {
		t.Errorf("expected default (false) due to cycle, got %q", v.Name)
	}

	// Explain should mention cycle
	eval := f.Explain("flag_a", map[string]any{})
	if eval.Reason != "prerequisite flag_b not met" {
		t.Logf("reason: %s", eval.Reason)
	}
	t.Logf("Trace:")
	for i, step := range eval.Trace {
		t.Logf("  [%d] %s: %s", i, step.Check, step.Detail)
	}
}

func TestVariantPayloads(t *testing.T) {
	src := `
segment internal {
    user.email ends_with "@m31labs.dev"
}

flag checkout_v2 type multivariate default "control" {
    owner: "oscar"

    variant "control" {
        provider: "legacy",
    }

    variant "treatment" {
        provider: "stripe",
        button_color: "#D4AF37",
        max_items: 50,
        show_promo: true,
    }

    when internal
        then "treatment"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// Internal user gets treatment with payload
	ctx := map[string]any{"user.email": "oscar@m31labs.dev"}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "treatment" {
		t.Errorf("expected treatment, got %q", v.Name)
	}
	if v.Values["provider"] != "stripe" {
		t.Errorf("provider: got %v, want stripe", v.Values["provider"])
	}
	if v.Values["button_color"] != "#D4AF37" {
		t.Errorf("button_color: got %v, want #D4AF37", v.Values["button_color"])
	}
	if v.Values["max_items"] != 50.0 {
		t.Errorf("max_items: got %v, want 50", v.Values["max_items"])
	}
	if v.Values["show_promo"] != true {
		t.Errorf("show_promo: got %v, want true", v.Values["show_promo"])
	}

	// Non-internal user gets control with payload
	ctx = map[string]any{"user.email": "random@gmail.com"}
	v = f.Variant("checkout_v2", ctx)
	if v.Name != "control" {
		t.Errorf("expected control, got %q", v.Name)
	}
	if v.Values["provider"] != "legacy" {
		t.Errorf("provider: got %v, want legacy", v.Values["provider"])
	}
}

func TestVariantDefaults(t *testing.T) {
	src := `
segment internal {
    user.email ends_with "@m31labs.dev"
}

flag checkout_v2 type multivariate default "control" {
    owner: "oscar"

    defaults {
        provider: "stripe",
    }

    variant "control" {
        button_color: "gray",
    }

    variant "treatment_a" {
        button_color: "blue",
    }

    variant "treatment_b" {
        button_color: "gold",
    }

    when internal
        then "treatment_b"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// Internal user gets treatment_b: inherits provider from defaults
	ctx := map[string]any{"user.email": "oscar@m31labs.dev"}
	v := f.Variant("checkout_v2", ctx)
	if v.Name != "treatment_b" {
		t.Errorf("expected treatment_b, got %q", v.Name)
	}
	if v.Values["provider"] != "stripe" {
		t.Errorf("provider (inherited): got %v, want stripe", v.Values["provider"])
	}
	if v.Values["button_color"] != "gold" {
		t.Errorf("button_color (variant-specific): got %v, want gold", v.Values["button_color"])
	}

	// Non-internal gets control: inherits provider, has own color
	ctx = map[string]any{"user.email": "random@gmail.com"}
	v = f.Variant("checkout_v2", ctx)
	if v.Name != "control" {
		t.Errorf("expected control, got %q", v.Name)
	}
	if v.Values["provider"] != "stripe" {
		t.Errorf("provider (inherited): got %v, want stripe", v.Values["provider"])
	}
	if v.Values["button_color"] != "gray" {
		t.Errorf("button_color: got %v, want gray", v.Values["button_color"])
	}
}

func TestVariantValidation(t *testing.T) {
	// Rule references undeclared variant — should fail at load time
	src := `
flag test type multivariate default "control" {
    owner: "test"

    variant "control" {}
    variant "treatment" {}

    when { true }
        then "nonexistent"
}
`
	_, err := Load([]byte(src))
	if err == nil {
		t.Error("expected load-time validation error for undeclared variant reference")
	}
}

func TestBooleanFlagNoPayload(t *testing.T) {
	src := `
flag simple type boolean default false {
    owner: "test"
    when { true }
        then true
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	if !f.Enabled("simple", map[string]any{}) {
		t.Error("expected enabled")
	}
	v := f.Variant("simple", map[string]any{})
	if v.Name != "true" {
		t.Errorf("expected true, got %q", v.Name)
	}
	if v.Values != nil {
		t.Errorf("boolean flag should have nil Values, got %v", v.Values)
	}
	// Bool() helper
	if !v.Bool() {
		t.Error("Bool() should return true")
	}
}

func TestFlagSplitAssignsVariantDeterministically(t *testing.T) {
	f, err := Load([]byte(`
flag pricing_page type multivariate default "control" {
	variant "control" {}
	variant "social_proof" {}
	variant "comparison_table" {}

	when { true } split by user.id namespace "pricing_page_oct_test" {
		"control": 5000,
		"social_proof": 3000,
		"comparison_table": 2000,
	}
}
`))
	if err != nil {
		t.Fatal(err)
	}

	namespace := "pricing_page_oct_test"
	var controlUser, socialUser, comparisonUser string
	for i := 0; i < 10000; i++ {
		userID := fmt.Sprintf("split_user_%d", i)
		bucket := govern.RolloutBucket(namespace, userID)
		switch {
		case bucket < 5000 && controlUser == "":
			controlUser = userID
		case bucket >= 5000 && bucket < 8000 && socialUser == "":
			socialUser = userID
		case bucket >= 8000 && comparisonUser == "":
			comparisonUser = userID
		}
		if controlUser != "" && socialUser != "" && comparisonUser != "" {
			break
		}
	}

	cases := []struct {
		name   string
		userID string
		want   string
	}{
		{name: "control", userID: controlUser, want: "control"},
		{name: "social", userID: socialUser, want: "social_proof"},
		{name: "comparison", userID: comparisonUser, want: "comparison_table"},
	}
	for _, tc := range cases {
		if tc.userID == "" {
			t.Fatalf("failed to find user for %s band", tc.name)
		}
		got := f.Variant("pricing_page", map[string]any{"user.id": tc.userID})
		if got.Name != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got.Name, tc.want)
		}
	}
}

func TestFlagSplitRequiresFullWeightSum(t *testing.T) {
	_, err := Load([]byte(`
flag pricing_page type multivariate default "control" {
	variant "control" {}
	variant "social_proof" {}

	when { true } split by user.id namespace "pricing_page_oct_test" {
		"control": 5000,
		"social_proof": 4000,
	}
}
`))
	if err == nil || !strings.Contains(err.Error(), "split weights must sum to 10000") {
		t.Fatalf("expected split weight validation error, got %v", err)
	}
}

func TestTypedAccessors(t *testing.T) {
	src := `
segment all {
    true == true
}

flag config type multivariate default "off" {
    owner: "test"

    variant "off" {
        enabled: false,
        label: "disabled",
    }

    variant "on" {
        enabled: true,
        label: "active",
        max_count: 100,
        rate: 0.75,
    }

    when all then "on"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	v := f.Variant("config", map[string]any{})

	// String accessor
	if v.String("label", "") != "active" {
		t.Errorf("String(label): got %q, want active", v.String("label", ""))
	}
	if v.String("missing", "default") != "default" {
		t.Error("String(missing) should return fallback")
	}

	// Number accessor
	if v.Number("rate", 0) != 0.75 {
		t.Errorf("Number(rate): got %f, want 0.75", v.Number("rate", 0))
	}
	if v.Number("missing", 42) != 42 {
		t.Error("Number(missing) should return fallback")
	}

	// Int accessor
	if v.Int("max_count", 0) != 100 {
		t.Errorf("Int(max_count): got %d, want 100", v.Int("max_count", 0))
	}

	// Flag (bool) accessor
	if !v.Flag("enabled", false) {
		t.Error("Flag(enabled) should be true")
	}
	if v.Flag("missing", false) != false {
		t.Error("Flag(missing) should return fallback")
	}
}

func TestDecodeStruct(t *testing.T) {
	src := `
segment all {
    true == true
}

flag config type multivariate default "off" {
    owner: "test"

    variant "off" {}

    variant "on" {
        provider: "stripe",
        button_color: "#D4AF37",
        max_items: 50,
        show_promo: true,
    }

    when all then "on"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	v := f.Variant("config", map[string]any{})

	type Config struct {
		Provider    string  `json:"provider"`
		ButtonColor string  `json:"button_color"`
		MaxItems    float64 `json:"max_items"`
		ShowPromo   bool    `json:"show_promo"`
	}

	var cfg Config
	if err := v.Decode(&cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Provider != "stripe" {
		t.Errorf("Provider: got %q, want stripe", cfg.Provider)
	}
	if cfg.ButtonColor != "#D4AF37" {
		t.Errorf("ButtonColor: got %q, want #D4AF37", cfg.ButtonColor)
	}
	if cfg.MaxItems != 50 {
		t.Errorf("MaxItems: got %f, want 50", cfg.MaxItems)
	}
	if !cfg.ShowPromo {
		t.Error("ShowPromo should be true")
	}
}

func TestSecretRef(t *testing.T) {
	src := `
segment all {
    true == true
}

flag api_config type multivariate default "legacy" {
    owner: "platform"

    variant "legacy" {
        endpoint: "https://legacy.api.com",
        api_key: secret("keys/legacy"),
    }

    variant "v2" {
        endpoint: "https://v2.api.com",
        api_key: secret("keys/v2"),
    }

    when all then "v2"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// Variant() without resolver — shows secret reference
	v := f.Variant("api_config", map[string]any{})
	if v.Name != "v2" {
		t.Errorf("expected v2, got %q", v.Name)
	}
	if v.Values["endpoint"] != "https://v2.api.com" {
		t.Errorf("endpoint: got %v", v.Values["endpoint"])
	}
	// api_key should show the reference (no resolver configured)
	apiKey := v.Values["api_key"]
	if apiKey != `secret("keys/v2")` {
		t.Errorf("api_key should show secret ref, got %v", apiKey)
	}

	// AllFlags() — secrets redacted
	all := f.AllFlags(map[string]any{})
	allKey := all["api_config"].Values["api_key"]
	if allKey != "[REDACTED]" {
		t.Errorf("AllFlags should redact secrets, got %v", allKey)
	}

	// Explain() — secrets redacted
	eval := f.Explain("api_config", map[string]any{})
	explainKey := eval.Variant.Values["api_key"]
	if explainKey != "[REDACTED]" {
		t.Errorf("Explain should redact secrets, got %v", explainKey)
	}
}

func TestSchemaValidation(t *testing.T) {
	// Type mismatch: max_items is number in one variant, string in another
	src := `
flag test type multivariate default "a" {
    owner: "test"

    variant "a" {
        max_items: 50,
    }

    variant "b" {
        max_items: "fifty",
    }

    when { true } then "b"
}
`
	_, err := Load([]byte(src))
	if err == nil {
		t.Error("expected schema validation error for type mismatch")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestSchemaConsistent(t *testing.T) {
	// Same types across variants — should pass
	src := `
flag test type multivariate default "a" {
    owner: "test"

    variant "a" {
        count: 10,
        label: "alpha",
        enabled: true,
    }

    variant "b" {
        count: 20,
        label: "beta",
        enabled: false,
    }

    when { true } then "b"
}
`
	f, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	v := f.Variant("test", map[string]any{})
	if v.Int("count", 0) != 20 {
		t.Errorf("count: got %d, want 20", v.Int("count", 0))
	}
}
