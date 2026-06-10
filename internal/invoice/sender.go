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
	// OutputDir, when set, is where this sender's PDFs are written (used
	// directly, no per-sender subfolder). Empty ⇒ fall back to the global
	// invoice.output_dir with a <senderKey>/ subfolder. Supports ~ expansion.
	OutputDir string `yaml:"output_dir"`
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

// Client is the billed-to party for a company's invoices. Keyed by company
// name in config; absent entry = render the company name only (back-compat).
type Client struct {
	LegalName    string   `yaml:"legal_name"`
	TaxID        string   `yaml:"tax_id"`
	TaxIDLabel   string   `yaml:"tax_id_label"`
	Email        string   `yaml:"email"`
	AddressLines []string `yaml:"address_lines"`
}

type SendersConfig struct {
	Senders map[string]Sender   `yaml:"senders"`
	Clients map[string]Client   `yaml:"clients"`
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

// Validate returns a list of human-readable issues found in the config.
// Empty slice = config is good to use.
func (c *SendersConfig) Validate() []string {
	var issues []string
	if c.Invoice.OutputDir == "" {
		issues = append(issues, "invoice.output_dir is empty")
	}
	if c.Invoice.LineItemLabel == "" {
		issues = append(issues, "invoice.line_item_label is empty")
	}
	for key, s := range c.Senders {
		if s.LegalName == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.legal_name is empty", key))
		}
		if s.TaxID == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.tax_id is empty", key))
		}
		if len(s.AddressLines) == 0 {
			issues = append(issues, fmt.Sprintf("senders.%s.address_lines is empty", key))
		}
		if s.Invoice.NumberPrefix == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.invoice.number_prefix is empty", key))
		}
		if s.Invoice.NextNumber < 1 {
			issues = append(issues, fmt.Sprintf("senders.%s.invoice.next_number must be >= 1", key))
		}
		if len(s.BankAccounts) == 0 {
			issues = append(issues, fmt.Sprintf("senders.%s.bank_accounts has no entries", key))
		}
		for ccy, bank := range s.BankAccounts {
			if len(bank.Fields) == 0 {
				issues = append(issues, fmt.Sprintf("senders.%s.bank_accounts.%s.fields is empty", key, ccy))
			}
		}
	}
	return issues
}

// BankFor returns the bank account block to display for the given currency.
// Tries an exact key match first; falls back to any block whose AlsoAccepts
// list contains the currency. Returns (zero, false) if neither matches.
func (s Sender) BankFor(currency string) (Bank, bool) {
	if b, ok := s.BankAccounts[currency]; ok {
		return b, true
	}
	for _, b := range s.BankAccounts {
		for _, alt := range b.AlsoAccepts {
			if alt == currency {
				return b, true
			}
		}
	}
	return Bank{}, false
}
