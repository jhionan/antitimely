// Package invoice generates client invoices as PDFs from the data antitimely
// tracks. Pure-logic types (Sender, BankAccount, InvoiceDoc, ...) and the
// maroto renderer live here; the daemon-side orchestration lives in
// internal/daemon/rpc_invoice.go.
package invoice

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Sender struct {
	LegalName    string          `yaml:"legal_name"`
	TaxID        string          `yaml:"tax_id"`
	TaxIDLabel   string          `yaml:"tax_id_label"`
	AddressLines []string        `yaml:"address_lines"`
	LogoPath     string          `yaml:"logo_path"`
	Invoice      InvoiceSeed     `yaml:"invoice"`
	BankAccounts map[string]Bank `yaml:"bank_accounts"`
}

type InvoiceSeed struct {
	NumberPrefix string `yaml:"number_prefix"`
	NumberPad    int    `yaml:"number_pad"`
	NextNumber   int64  `yaml:"next_number"`
}

type Bank struct {
	Title       string      `yaml:"title"`
	Subtitle    string      `yaml:"subtitle"`
	AlsoAccepts []string    `yaml:"also_accepts"`
	Fields      []BankField `yaml:"fields"`
}

type BankField struct {
	Label string `yaml:"label"`
	Value string `yaml:"value"`
}

type GlobalInvoiceConfig struct {
	OutputDir     string `yaml:"output_dir"`
	LineItemLabel string `yaml:"line_item_label"`
	DueDays       int    `yaml:"due_days"`
}

type SendersConfig struct {
	Senders map[string]Sender   `yaml:"senders"`
	Invoice GlobalInvoiceConfig `yaml:"invoice"`
}

// LoadSendersConfig parses the senders + invoice blocks from a YAML file.
// Other top-level keys (daemon settings) are ignored — the file is shared
// with the daemon's config.
func LoadSendersConfig(path string) (*SendersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c SendersConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}
