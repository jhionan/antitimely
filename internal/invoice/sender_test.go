package invoice

import (
	"os"
	"path/filepath"
	"testing"
)

const validSenderYAML = `
senders:
  br:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "34.012.215/0001-44"
    tax_id_label: "CNPJ"
    address_lines: ["Mateus Leme 2830", "curitiba", "82200000", "Paraná", "Brazil"]
    logo_path: ""
    output_dir: "/Users/x/Documents/BR Invoices"
    invoice:
      number_prefix: "INV-"
      number_pad: 3
      next_number: 14
    bank_accounts:
      EUR:
        title: "Local bank details"
        subtitle: "Wise EUR"
        fields:
          - { label: "IBAN", value: "BE16..." }
  es:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "ESZ2614896P"
    tax_id_label: "VAT"
    address_lines: ["Escultor Miquel Navarro Navarro 2", "Mislata", "46920", "Spain"]
    invoice:
      number_prefix: "ES-"
      number_pad: 4
      next_number: 1
    bank_accounts:
      EUR:
        also_accepts: [CAD]
        fields:
          - { label: "IBAN", value: "ES51..." }

invoice:
  output_dir: "/tmp/inv"
  line_item_label: "Software development"
  due_days: 0
`

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSendersConfig_ParsesValidFile(t *testing.T) {
	path := writeYAML(t, validSenderYAML)
	cfg, err := LoadSendersConfig(path)
	if err != nil {
		t.Fatalf("LoadSendersConfig: %v", err)
	}
	if len(cfg.Senders) != 2 {
		t.Fatalf("want 2 senders, got %d", len(cfg.Senders))
	}
	br, ok := cfg.Senders["br"]
	if !ok {
		t.Fatal("missing br sender")
	}
	if br.TaxIDLabel != "CNPJ" {
		t.Errorf("br.TaxIDLabel = %q, want CNPJ", br.TaxIDLabel)
	}
	if br.Invoice.NextNumber != 14 {
		t.Errorf("br.Invoice.NextNumber = %d, want 14", br.Invoice.NextNumber)
	}
	if br.OutputDir != "/Users/x/Documents/BR Invoices" {
		t.Errorf("br.OutputDir = %q, want per-sender output dir", br.OutputDir)
	}
	es := cfg.Senders["es"]
	if es.OutputDir != "" {
		t.Errorf("es.OutputDir = %q, want empty (no per-sender override)", es.OutputDir)
	}
	if got := es.BankAccounts["EUR"].AlsoAccepts; len(got) != 1 || got[0] != "CAD" {
		t.Errorf("es EUR.AlsoAccepts = %v, want [CAD]", got)
	}
	if cfg.Invoice.LineItemLabel != "Software development" {
		t.Errorf("cfg.Invoice.LineItemLabel = %q", cfg.Invoice.LineItemLabel)
	}
}

func TestValidate_HappyPath(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	issues := cfg.Validate()
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidate_DetectsMissingFields(t *testing.T) {
	bad := `
senders:
  bad:
    legal_name: ""
    tax_id: ""
    address_lines: []
    invoice:
      number_prefix: ""
      number_pad: 0
      next_number: 0
    bank_accounts: {}
invoice:
  output_dir: ""
`
	cfg, err := LoadSendersConfig(writeYAML(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	issues := cfg.Validate()
	if len(issues) == 0 {
		t.Fatal("expected issues, got none")
	}
	// Should report all of: legal_name, tax_id, address_lines, number_prefix,
	// next_number, bank_accounts empty, output_dir empty.
	wantSubstr := []string{"legal_name", "tax_id", "address_lines",
		"number_prefix", "next_number", "bank_accounts", "output_dir"}
	for _, want := range wantSubstr {
		found := false
		for _, iss := range issues {
			if contains(iss, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no issue mentions %q; got %v", want, issues)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestBankFor_DirectMatch(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	br := cfg.Senders["br"]
	bank, ok := br.BankFor("EUR")
	if !ok {
		t.Fatal("EUR not found on br")
	}
	if len(bank.Fields) == 0 || bank.Fields[0].Label != "IBAN" {
		t.Errorf("unexpected bank: %+v", bank)
	}
}

func TestBankFor_AlsoAcceptsFallback(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	es := cfg.Senders["es"]
	// es has only EUR but EUR.also_accepts=[CAD] → lookup for CAD finds EUR.
	bank, ok := es.BankFor("CAD")
	if !ok {
		t.Fatal("CAD not found on es via also_accepts")
	}
	if len(bank.AlsoAccepts) != 1 || bank.AlsoAccepts[0] != "CAD" {
		t.Errorf("found wrong bank: %+v", bank)
	}
}

func TestBankFor_NoMatch(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	br := cfg.Senders["br"]
	if _, ok := br.BankFor("USD"); ok {
		t.Error("USD should not match anything on br")
	}
}
