package arbiter

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareInjectsDecisionIntoRequestContext(t *testing.T) {
	compiled, err := CompileFull([]byte(`
rule AllowAdminRead {
	when {
		request.method == "GET"
		and user.role == "admin"
	}
	then Allow {
		reason: "admin read",
	}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	handler := Middleware(compiled, func(r *http.Request) (map[string]any, error) {
		return map[string]any{
			"request": map[string]any{
				"method": r.Method,
			},
			"user": map[string]any{
				"role": r.Header.Get("X-Role"),
			},
		}, nil
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision, ok := DecisionFromRequest(r)
		if !ok {
			t.Fatal("missing middleware decision")
		}
		if len(decision.Matched) != 1 {
			t.Fatalf("matched = %d, want 1", len(decision.Matched))
		}
		if decision.Matched[0].Action != "Allow" {
			t.Fatalf("action = %q, want Allow", decision.Matched[0].Action)
		}
		if got := decision.Matched[0].Params["reason"]; got != "admin read" {
			t.Fatalf("reason = %#v, want admin read", got)
		}
		if got := decision.Context["user"].(map[string]any)["role"]; got != "admin" {
			t.Fatalf("context user.role = %#v, want admin", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/reports", nil)
	req.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestMiddlewareUsesDefaultHTTPContext(t *testing.T) {
	compiled, err := CompileFull([]byte(`
rule AcceptDebugRequest {
	when {
		request.method == "POST"
		and request.path == "/reviews"
		and request.query.limit == 5
		and request.query.dry_run == true
		and request.headers.x_debug == true
	}
	then Allow {}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	handler := Middleware(compiled, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision, ok := DecisionFromRequest(r)
		if !ok {
			t.Fatal("missing middleware decision")
		}
		if len(decision.Matched) != 1 {
			t.Fatalf("matched = %d, want 1", len(decision.Matched))
		}
		if decision.Matched[0].Action != "Allow" {
			t.Fatalf("action = %q, want Allow", decision.Matched[0].Action)
		}
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/reviews?limit=5&dry-run=true", nil)
	req.Header.Set("X-Debug", "true")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}
}

func TestMiddlewareCustomBuildErrorHandler(t *testing.T) {
	compiled, err := CompileFull([]byte(`
rule AllowAll {
	when { true }
	then Allow {}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	handler := MiddlewareWithOptions(compiled, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not be called")
	}), HTTPMiddlewareOptions{
		BuildContext: func(*http.Request) (map[string]any, error) {
			return nil, errors.New("bad request payload")
		},
		OnBuildError: func(w http.ResponseWriter, _ *http.Request, err error) {
			if err.Error() != "bad request payload" {
				t.Fatalf("unexpected build error: %v", err)
			}
			http.Error(w, "custom build error", http.StatusTeapot)
		},
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTeapot)
	}
	if body := strings.TrimSpace(rr.Body.String()); body != "custom build error" {
		t.Fatalf("body = %q, want custom build error", body)
	}
}

func TestMiddlewareDefaultEvalErrorHandler(t *testing.T) {
	handler := Middleware(nil, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), "evaluate arbiter rules: nil compiled ruleset") {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func TestDecisionFromContextAbsent(t *testing.T) {
	if _, ok := DecisionFromRequest(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("expected no decision on plain request")
	}
}

func TestDefaultHTTPContextHandlesNilURL(t *testing.T) {
	ctx, err := DefaultHTTPContext(&http.Request{
		Method: http.MethodGet,
		Header: http.Header{"X-Debug": []string{"true"}},
	})
	if err != nil {
		t.Fatalf("DefaultHTTPContext: %v", err)
	}
	request, ok := ctx["request"].(map[string]any)
	if !ok {
		t.Fatalf("request = %#v", ctx["request"])
	}
	if got := request["path"]; got != "" {
		t.Fatalf("path = %#v, want empty string", got)
	}
	if got, ok := request["query"].(map[string]any); ok && got != nil {
		t.Fatalf("query = %#v, want nil", got)
	}
	headers, ok := request["headers"].(map[string]any)
	if !ok || headers["x_debug"] != true {
		t.Fatalf("headers = %#v", request["headers"])
	}
}
