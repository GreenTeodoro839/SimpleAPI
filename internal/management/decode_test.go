package management

import (
	"strings"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
)

// reproBody is the JSON body from the issue's repro: every forward slash is
// escaped as "\/" exactly as Android's org.json serializer emits it. It is
// perfectly valid JSON but rejected by gopkg.in/yaml.v3 ("found unknown escape
// character"), which is what the management endpoints used for parsing.
const reproBody = `{"name":"repro","type":"openai_completion","url":"https:\/\/example.com\/v1","key":"","headers":{"X":"a\/b"},"models":[{"model":"m","aliasA":"a"}]}`

func TestDecodeJSONWithEscapedSlash(t *testing.T) {
	// The headline regression: application/json with "\/" must parse, with the
	// escapes collapsed to plain slashes.
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", ""} {
		var p config.Provider
		if err := decode(requestMIME(ct), []byte(reproBody), &p); err != nil {
			t.Fatalf("content-type %q: decode failed: %v", ct, err)
		}
		if p.URL != "https://example.com/v1" {
			t.Errorf("content-type %q: URL = %q, want https://example.com/v1", ct, p.URL)
		}
		if p.Headers["X"] != "a/b" {
			t.Errorf("content-type %q: Headers[X] = %q, want a/b", ct, p.Headers["X"])
		}
		if len(p.Models) != 1 || p.Models[0].Model != "m" || p.Models[0].AliasA != "a" {
			t.Errorf("content-type %q: models = %+v, want one model m/a", ct, p.Models)
		}
	}
}

func TestDecodeExplicitYAML(t *testing.T) {
	yamlBody := []byte("name: repro\ntype: openai_completion\nurl: https://example.com/v1\n")
	var p config.Provider
	if err := decode("application/yaml", yamlBody, &p); err != nil {
		t.Fatalf("decode application/yaml failed: %v", err)
	}
	if p.Name != "repro" || p.URL != "https://example.com/v1" {
		t.Errorf("got name=%q url=%q", p.Name, p.URL)
	}
}

func TestDecodeUnspecifiedFallsBackToYAML(t *testing.T) {
	// A plain YAML body is not valid JSON, so with no content type the decoder
	// must fall back to YAML rather than erroring.
	yamlBody := []byte("name: yamlclient\nurl: https://example.com/v1\n")
	var p config.Provider
	if err := decode("", yamlBody, &p); err != nil {
		t.Fatalf("decode with empty content type failed: %v", err)
	}
	if p.Name != "yamlclient" {
		t.Errorf("got name=%q, want yamlclient", p.Name)
	}
}

func TestDecodeExplicitJSONSurfacesJSONError(t *testing.T) {
	// Malformed body with an explicit JSON content type must report a JSON
	// error and must NOT be retried (and re-wrapped) as YAML.
	malformed := []byte(`{"name": `)
	err := decode("application/json", malformed, &config.Provider{})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid JSON request body") {
		t.Errorf("error = %q, want it to mention invalid JSON request body", err.Error())
	}
}

func TestDecodeMalformedUnspecifiedReportsYAMLFallback(t *testing.T) {
	// Same malformed body but no content type: it takes the YAML-fallback path,
	// so the error is wrapped as a generic body error rather than a JSON one.
	malformed := []byte(`{"name": `)
	err := decode("", malformed, &config.Provider{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "invalid JSON request body") {
		t.Errorf("error = %q, should not have taken the explicit-JSON branch", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid request body") {
		t.Errorf("error = %q, want it to mention invalid request body", err.Error())
	}
}

func TestRequestMIME(t *testing.T) {
	cases := map[string]string{
		"application/json":                  "application/json",
		"application/json; charset=utf-8":   "application/json",
		"  Application/YAML ; charset=x  ":  "application/yaml",
		"application/vnd.foo+json":          "application/vnd.foo+json",
		"":                                  "",
	}
	for in, want := range cases {
		if got := requestMIME(in); got != want {
			t.Errorf("requestMIME(%q) = %q, want %q", in, got, want)
		}
	}
}
