package govern

import "testing"

func TestNestDottedKeys(t *testing.T) {
	nested := NestDottedKeys(map[string]any{
		"user.id":    "u_123",
		"user.plan":  "enterprise",
		"request_id": "req_1",
	})

	user, ok := nested["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested user map, got %#v", nested["user"])
	}
	if user["id"] != "u_123" || user["plan"] != "enterprise" {
		t.Fatalf("unexpected nested user values: %#v", user)
	}
	if nested["request_id"] != "req_1" {
		t.Fatalf("expected request_id passthrough, got %#v", nested["request_id"])
	}
}
