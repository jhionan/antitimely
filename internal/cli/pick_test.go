package cli

import (
	"testing"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestParseCompanyChoice(t *testing.T) {
	items := []rpcapi.Company{{ID: 7, Name: "BClouder"}, {ID: 3, Name: "Foca.app"}}
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"1", "BClouder", true},
		{"2", "Foca.app", true},
		{" 2 ", "Foca.app", true}, // trimmed
		{"3", "", false},          // out of range
		{"0", "", false},          // 1-based, 0 invalid
		{"", "", false},           // blank
		{"b", "", false},          // back
		{"x", "", false},          // non-numeric
	}
	for _, c := range cases {
		name, ok := parseCompanyChoice(items, c.in)
		if name != c.wantName || ok != c.wantOK {
			t.Errorf("parseCompanyChoice(%q) = (%q,%v), want (%q,%v)", c.in, name, ok, c.wantName, c.wantOK)
		}
	}
}

func TestParseCompanyChoiceEmptyList(t *testing.T) {
	if _, ok := parseCompanyChoice(nil, "1"); ok {
		t.Error("expected ok=false for empty company list")
	}
}
