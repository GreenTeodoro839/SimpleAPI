package payload

import (
	"net/http"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/tidwall/gjson"
)

func mc() MatchContext {
	return MatchContext{InternalID: "p_a", AliasA: "a", UpstreamModel: "m", Protocol: "anthropic", FromProtocol: "anthropic", Headers: http.Header{}}
}

func TestDefaultFirstMatchWinsAndNoOverwrite(t *testing.T) {
	cfg := &config.PayloadConfig{Default: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: map[string]interface{}{"x": 1}},
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: map[string]interface{}{"x": 2}},
	}}
	e := NewEngine(cfg)
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), mc()), "x").Int(); got != 1 {
		t.Errorf("first-match-wins: x=%d want 1", got)
	}
	// existing value must not be overwritten by default
	if got := gjson.GetBytes(e.Apply([]byte(`{"x":9}`), mc()), "x").Int(); got != 9 {
		t.Errorf("default overwrote existing: x=%d want 9", got)
	}
}

func TestOverrideLastMatchWins(t *testing.T) {
	cfg := &config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: map[string]interface{}{"x": 1}},
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: map[string]interface{}{"x": 2}},
	}}
	e := NewEngine(cfg)
	if got := gjson.GetBytes(e.Apply([]byte(`{"x":0}`), mc()), "x").Int(); got != 2 {
		t.Errorf("last-match-wins: x=%d want 2", got)
	}
}

func TestOverrideRawSetsFragment(t *testing.T) {
	cfg := &config.PayloadConfig{OverrideRaw: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: map[string]interface{}{"fmt": `{"type":"json_object"}`}},
	}}
	e := NewEngine(cfg)
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), mc()), "fmt.type").String(); got != "json_object" {
		t.Errorf("raw fragment: fmt.type=%q want json_object", got)
	}
}

func TestFilterDeletes(t *testing.T) {
	// Reverse-sort deletion must keep array indices valid: deleting a.0 and a.2
	// from [1,2,3] should leave [2] (delete higher index first).
	cfg := &config.PayloadConfig{Filter: []config.PayloadFilterRule{
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: []string{"a.0", "a.2"}},
	}}
	e := NewEngine(cfg)
	out := e.Apply([]byte(`{"a":[1,2,3]}`), mc())
	got := gjson.GetBytes(out, "a").Array()
	if len(got) != 1 || got[0].Int() != 2 {
		t.Errorf("filter array delete: a=%v want [2]", got)
	}
	// whole-key delete
	cfg2 := &config.PayloadConfig{Filter: []config.PayloadFilterRule{
		{Models: []config.PayloadModelRule{{Name: "p_a"}}, Params: []string{"drop"}},
	}}
	out2 := NewEngine(cfg2).Apply([]byte(`{"drop":1,"keep":{"me":1}}`), mc())
	if gjson.GetBytes(out2, "drop").Exists() || !gjson.GetBytes(out2, "keep.me").Exists() {
		t.Errorf("filter whole-key delete wrong: %s", out2)
	}
}

func TestModelNameGlobAndProtocolMatch(t *testing.T) {
	cfg := &config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "p_*", Protocol: "openai_completion"}}, Params: map[string]interface{}{"k": "v"}},
	}}
	e := NewEngine(cfg)
	// matches glob but protocol differs -> no write
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), mc()), "k").Exists(); got {
		t.Error("rule should not match (protocol mismatch)")
	}
	// protocol matches -> write
	mc2 := mc()
	mc2.Protocol = "openai_completion"
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), mc2), "k").String(); got != "v" {
		t.Error("rule should match glob + protocol")
	}
}

func TestHeaderMatch(t *testing.T) {
	cfg := &config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "p_a", Headers: map[string]string{"X-Tier": "dev-*"}}}, Params: map[string]interface{}{"k": 1}},
	}}
	e := NewEngine(cfg)
	h := http.Header{}
	h.Set("X-Tier", "dev-pro")
	m := mc()
	m.Headers = h
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), m), "k").Int(); got != 1 {
		t.Error("header glob should match")
	}
	h.Set("X-Tier", "prod")
	if got := gjson.GetBytes(e.Apply([]byte(`{}`), m), "k").Exists(); got {
		t.Error("header glob should NOT match prod")
	}
}
