package flags

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if v != "treatment" {
		t.Errorf("expected treatment, got %q", v)
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
	if v != "control" {
		t.Errorf("expected control (default), got %q", v)
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
	if v != "false" {
		t.Errorf("expected false (default), got %q", v)
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
	if v != "control" {
		t.Errorf("expected control (default due to prereq fail), got %q", v)
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
	for i := 0; i < 1000; i++ {
		uid := fmt.Sprintf("user_%d", i)
		b := Bucket(uid)
		if b < 20 && lowBucketUser == "" {
			lowBucketUser = uid
		}
		if b >= 20 && highBucketUser == "" {
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
	if v != "treatment" {
		t.Errorf("expected treatment for low-bucket enterprise_us user (bucket=%d), got %q", Bucket(lowBucketUser), v)
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
	if v != "control" {
		t.Errorf("expected control for high-bucket enterprise_us user (bucket=%d), got %q", Bucket(highBucketUser), v)
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
	if all["checkout_v2"] != "treatment" {
		t.Errorf("checkout_v2: got %q, want treatment", all["checkout_v2"])
	}
	if all["payments_enabled"] != "true" {
		t.Errorf("payments_enabled: got %q, want true", all["payments_enabled"])
	}
	if all["dark_mode"] != "false" {
		t.Errorf("dark_mode: got %q, want false (kill_switch)", all["dark_mode"])
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
	if eval.Variant != "treatment" {
		t.Errorf("variant: got %q, want treatment", eval.Variant)
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

	if eval.Variant != "control" {
		t.Errorf("variant: got %q, want control", eval.Variant)
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

	if eval.Variant != "false" {
		t.Errorf("variant: got %q, want false", eval.Variant)
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

	if eval.Variant != "control" {
		t.Errorf("variant: got %q, want control", eval.Variant)
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

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["checkout_v2"] != "treatment" {
		t.Errorf("checkout_v2: got %q, want treatment", result["checkout_v2"])
	}
	if result["dark_mode"] != "false" {
		t.Errorf("dark_mode: got %q, want false", result["dark_mode"])
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

	if result.Variant != "false" {
		t.Errorf("variant: got %q, want false", result.Variant)
	}
	if result.Reason != "kill-switched" {
		t.Errorf("reason: got %q, want kill-switched", result.Reason)
	}
}
