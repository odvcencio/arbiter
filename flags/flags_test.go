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

const testFlags = `
rule NewDashboard {
    when { percent_bucket < 25 }
    then Enable { flag: "new_dashboard", variant: "treatment" }
    otherwise Enable { flag: "new_dashboard", variant: "control" }
}

rule AIFeatures {
    when { org in ["acme", "m31labs"] }
    then Enable { flag: "ai_features" }
}

rule ProOnly {
    when { plan in ["pro", "enterprise"] }
    then Enable { flag: "advanced_analytics" }
}
`

const testFlagsEnv = `
rule NewDashboard {
    when {
        (env == "production" and percent_bucket < 5)
        or env == "staging"
        or env == "development"
    }
    then Enable { flag: "new_dashboard", variant: "treatment" }
}
`

func TestLoadAndEnabled(t *testing.T) {
	f, err := Load([]byte(testFlags))
	if err != nil {
		t.Fatal(err)
	}

	// User in bucket 10 (< 25) should get new_dashboard
	ctx := map[string]any{"percent_bucket": 10.0, "org": "acme", "plan": "free"}
	if !f.Enabled("new_dashboard", ctx) {
		t.Error("expected new_dashboard enabled for bucket 10")
	}
	if !f.Enabled("ai_features", ctx) {
		t.Error("expected ai_features enabled for org acme")
	}
	if f.Enabled("advanced_analytics", ctx) {
		t.Error("expected advanced_analytics disabled for free plan")
	}
}

func TestVariant(t *testing.T) {
	f, err := Load([]byte(testFlags))
	if err != nil {
		t.Fatal(err)
	}

	// Bucket 10 -> treatment
	ctx := map[string]any{"percent_bucket": 10.0, "org": "nobody", "plan": "free"}
	v := f.Variant("new_dashboard", ctx)
	if v != "treatment" {
		t.Errorf("expected treatment, got %q", v)
	}

	// Bucket 50 -> control (fallback)
	ctx["percent_bucket"] = 50.0
	v = f.Variant("new_dashboard", ctx)
	if v != "control" {
		t.Errorf("expected control, got %q", v)
	}
}

func TestAllFlags(t *testing.T) {
	f, err := Load([]byte(testFlags))
	if err != nil {
		t.Fatal(err)
	}

	ctx := map[string]any{"percent_bucket": 10.0, "org": "m31labs", "plan": "enterprise"}
	all := f.AllFlags(ctx)

	if all["new_dashboard"] != "treatment" {
		t.Errorf("new_dashboard: got %q, want treatment", all["new_dashboard"])
	}
	if all["ai_features"] != "enabled" {
		t.Errorf("ai_features: got %q, want enabled", all["ai_features"])
	}
	if all["advanced_analytics"] != "enabled" {
		t.Errorf("advanced_analytics: got %q, want enabled", all["advanced_analytics"])
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

func TestBucketDistribution(t *testing.T) {
	buckets := make([]int, 100)
	for i := 0; i < 10000; i++ {
		b := Bucket(fmt.Sprintf("user_%d", i))
		buckets[b]++
	}
	// Each bucket should have ~100 users (10000/100), allow 50-150
	for i, count := range buckets {
		if count < 50 || count > 150 {
			t.Errorf("bucket %d has %d users, expected ~100", i, count)
		}
	}
}

func TestEnvironmentPerFile(t *testing.T) {
	dir := t.TempDir()

	// Create per-environment files
	os.WriteFile(filepath.Join(dir, "production.arb"), []byte(`
rule Feature {
    when { percent_bucket < 5 }
    then Enable { flag: "feature", variant: "slow_rollout" }
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "staging.arb"), []byte(`
rule Feature {
    when { true }
    then Enable { flag: "feature", variant: "full" }
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

func TestEnvironmentInContext(t *testing.T) {
	f, err := Load([]byte(testFlagsEnv))
	if err != nil {
		t.Fatal(err)
	}

	// Production, bucket 50 -> not enabled (need < 5)
	ctx := map[string]any{"env": "production", "percent_bucket": 50.0}
	if f.Enabled("new_dashboard", ctx) {
		t.Error("expected disabled in production for bucket 50")
	}

	// Production, bucket 3 -> enabled
	ctx["percent_bucket"] = 3.0
	if !f.Enabled("new_dashboard", ctx) {
		t.Error("expected enabled in production for bucket 3")
	}

	// Staging -> always enabled
	ctx = map[string]any{"env": "staging", "percent_bucket": 99.0}
	if !f.Enabled("new_dashboard", ctx) {
		t.Error("expected enabled in staging regardless of bucket")
	}

	// Development -> always enabled
	ctx = map[string]any{"env": "development", "percent_bucket": 99.0}
	if !f.Enabled("new_dashboard", ctx) {
		t.Error("expected enabled in development regardless of bucket")
	}
}

func TestReload(t *testing.T) {
	f, err := Load([]byte(`
rule Feature {
    when { true }
    then Enable { flag: "v1" }
}
`))
	if err != nil {
		t.Fatal(err)
	}

	if !f.Enabled("v1", map[string]any{}) {
		t.Error("expected v1 enabled")
	}

	// Reload with new rules
	err = f.Reload([]byte(`
rule Feature {
    when { true }
    then Enable { flag: "v2" }
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

func TestHTTPHandler(t *testing.T) {
	f, err := Load([]byte(testFlags))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/flags?percent_bucket=10&org=m31labs&plan=pro", nil)
	w := httptest.NewRecorder()

	f.Handler().ServeHTTP(w, req)

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["ai_features"] != "enabled" {
		t.Errorf("ai_features: got %q, want enabled", result["ai_features"])
	}
}

func TestWatchReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flags.arb")

	os.WriteFile(path, []byte(`
rule Feature {
    when { true }
    then Enable { flag: "v1" }
}
`), 0644)

	f, stop, err := Watch(path)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if !f.Enabled("v1", map[string]any{}) {
		t.Error("expected v1 enabled")
	}

	// Update file
	os.WriteFile(path, []byte(`
rule Feature {
    when { true }
    then Enable { flag: "v2" }
}
`), 0644)

	// Wait for fsnotify
	time.Sleep(200 * time.Millisecond)

	if !f.Enabled("v2", map[string]any{}) {
		t.Error("expected v2 enabled after file change")
	}
}
