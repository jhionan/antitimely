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
	es := cfg.Senders["es"]
	if got := es.BankAccounts["EUR"].AlsoAccepts; len(got) != 1 || got[0] != "CAD" {
		t.Errorf("es EUR.AlsoAccepts = %v, want [CAD]", got)
	}
	if cfg.Invoice.LineItemLabel != "Software development" {
		t.Errorf("cfg.Invoice.LineItemLabel = %q", cfg.Invoice.LineItemLabel)
	}
}
