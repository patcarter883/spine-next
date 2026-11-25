package agent

import (
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
)

func TestProviderFailure(t *testing.T) {
	for _, c := range []struct {
		msg    string
		tokens int64
		want   api.ProviderFailure
	}{
		{"Connection error.\nprovider retries exhausted: Connection error.", 0, api.ProviderUnreachable},
		{"connect ECONNREFUSED 127.0.0.1:8000", 0, api.ProviderUnreachable},
		{"read ECONNRESET", 0, api.ProviderUnreachable},
		{"getaddrinfo ENOTFOUND gpu-box.lan", 0, api.ProviderUnreachable},
		{"connect ETIMEDOUT 10.0.0.5:443", 0, api.ProviderUnreachable},
		{"TypeError: fetch failed", 0, api.ProviderUnreachable},
		{"socket hang up", 0, api.ProviderUnreachable},
		{"503 Service Unavailable", 0, api.ProviderUnreachable},
		{"502 Bad Gateway", 0, api.ProviderUnreachable},
		{"504 Gateway Timeout", 0, api.ProviderUnreachable},
		{`529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, 0, api.ProviderUnreachable},
		{"429 Too Many Requests", 0, api.ProviderRateLimit},
		{"Rate limit reached for requests", 0, api.ProviderRateLimit},
		{`401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, 0, api.ProviderAuth},
		{"403 Forbidden", 0, api.ProviderAuth},
		{"Incorrect API key provided", 0, api.ProviderAuth},
		{"No API key found for openai", 0, api.ProviderAuth},

		{"Connection error.\nprovider retries exhausted: Connection error.", 1200, ""},
		{"Model local/qwen not found", 0, ""},
		{"Unknown option --bogus", 0, ""},
		{"500 from vLLM", 0, ""},
		{"context length exceeded: 140000 > 131072", 0, ""},
		{"connect ECONNREFUSED 127.0.0.1:18503", 0, api.ProviderUnreachable},
		{"listening on 127.0.0.1:15030", 0, ""},
		{"", 0, ""},
	} {
		if got := ProviderFailure(c.msg, c.tokens); got != c.want {
			t.Errorf("ProviderFailure(%q, %d) = %q, want %q", c.msg, c.tokens, got, c.want)
		}
	}
}

// The failure from issue #7, a dead local model server, as the Pi path
// reports it; a Pi that cannot start is not a provider failure.
func TestPiProviderFailureOutcome(t *testing.T) {
	e := setup(t, map[string]map[string]any{"*": {"down": 1}})
	out, _, err := e.runPi(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "pi stopped with \"error\": Connection error.\nprovider retries exhausted: Connection error."
	if out.Reason != api.OutcomeCrashed || out.Message != want || out.TokensUsed != 0 || out.Provider != api.ProviderUnreachable {
		t.Fatalf("%+v", out)
	}

	e = setup(t, map[string]map[string]any{"*": {"write": map[string]string{"b.txt": "hi\n"}}})
	out, _, err = e.runPi(t, func(a *Adapter, _ *RunRequest) {
		a.Env["FAKE_PI_CHECK_MODELS"] = "1"
		a.Env[PiAgentDirEnv] = t.TempDir()
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != api.OutcomeCrashed || out.Provider != "" || !strings.Contains(out.Message, "not found") {
		t.Fatalf("startup failure classified: %+v", out)
	}
}
