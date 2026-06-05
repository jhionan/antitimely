package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rian/antitimely/internal/invoice"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

// configPath returns the senders/invoice config path. Honors ANTITIMELY_CONFIG
// for tests; otherwise defaults to ~/.antitimely/config.yaml.
func configPath() (string, error) {
	if p := os.Getenv("ANTITIMELY_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".antitimely", "config.yaml"), nil
}

// InvoiceGenerate implements the full generation flow per the design spec.
func (s *AntitimelyService) InvoiceGenerate(args rpcapi.InvoiceGenerateArgs, reply *rpcapi.InvoiceGenerateReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()

	co, err := s.Q.GetCompanyForInvoice(ctx, args.CompanyName)
	if err != nil {
		return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
	}
	if co.BillingMode == "none" {
		return fmt.Errorf("company %q is not billable (billing_mode='none')", co.Name)
	}
	if !co.BilledFrom.Valid || co.BilledFrom.String == "" {
		return fmt.Errorf("company %q has no billed_from sender", co.Name)
	}

	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := invoice.LoadSendersConfig(cfgPath)
	if err != nil {
		return err
	}
	if issues := cfg.Validate(); len(issues) > 0 {
		return fmt.Errorf("invalid senders config: %v", issues)
	}
	senderKey := co.BilledFrom.String
	sender, ok := cfg.Senders[senderKey]
	if !ok {
		return fmt.Errorf("sender %q not in config (run `atl invoice show-senders`)", senderKey)
	}

	var now time.Time
	if args.IssueDateUnix > 0 {
		now = time.Unix(args.IssueDateUnix, 0).Local()
	} else {
		now = time.Now()
	}
	lastSent, err := s.Q.LastInvoiceSentForCompany(ctx, co.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		lastSent = 0
	case err != nil:
		return err
	}
	from, to := invoice.DefaultPeriod(co.BillingMode, now, lastSent)
	if args.FromUnix > 0 {
		from = time.Unix(args.FromUnix, 0).Local()
	}
	if args.ToUnix > 0 {
		to = time.Unix(args.ToUnix, 0).Local()
	}
	if from.IsZero() && co.BillingMode == "hourly" {
		from = time.Unix(co.CreatedAt, 0).Local()
	}

	var ticks int64
	if co.BillingMode == "hourly" {
		t, err := s.Q.CountTicksForCompanyInRange(ctx, store.CountTicksForCompanyInRangeParams{
			CompanyID: sql.NullInt64{Int64: co.ID, Valid: true},
			Ts:        from.Unix(),
			Ts_2:      to.Unix(),
		})
		if err != nil {
			return err
		}
		ticks = t
		if ticks == 0 && !args.AllowEmpty {
			return fmt.Errorf("no time tracked for %q in %s..%s; pass --allow-empty to override",
				co.Name, from.Format("2006-01-02"), to.Format("2006-01-02"))
		}
	}

	if !args.DryRun {
		if err := s.Q.SeedSenderState(ctx, store.SeedSenderStateParams{
			SenderKey:         senderKey,
			NextInvoiceNumber: sender.Invoice.NextNumber,
		}); err != nil {
			return err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.Q.WithTx(tx)

	var allocatedNumber int64
	if args.DryRun {
		row := tx.QueryRowContext(ctx, "SELECT next_invoice_number FROM sender_state WHERE sender_key = ?", senderKey)
		if err := row.Scan(&allocatedNumber); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				allocatedNumber = sender.Invoice.NextNumber
			} else {
				return err
			}
		}
	} else {
		a, err := qtx.AllocateNextInvoiceNumber(ctx, senderKey)
		if err != nil {
			return fmt.Errorf("allocate invoice number: %w", err)
		}
		allocatedNumber = a
	}

	number := invoice.FormatInvoiceNumber(sender.Invoice.NumberPrefix, sender.Invoice.NumberPad, allocatedNumber)

	doc, err := invoice.BuildDoc(invoice.BuildDocInput{
		Now:           now,
		ClientName:    co.Name,
		Client:        cfg.Clients[co.Name], // zero value if no clients entry

		BillingMode:   co.BillingMode,
		Currency:      co.Currency.String,
		RateCents:     co.RateCents.Int64,
		Sender:        sender,
		InvoiceNumber: number,
		PeriodFrom:    from,
		PeriodTo:      to,
		DueDays:       cfg.Invoice.DueDays,
		LineItemLabel: cfg.Invoice.LineItemLabel,
		Ticks:         ticks,
		TickSec:       s.TickIntervalSeconds,
		DiscountCents: args.DiscountCents,
	})
	if err != nil {
		return err
	}

	var pdfPath string
	if args.DryRun {
		f, ferr := os.CreateTemp("", "atl-dryrun-*.pdf")
		if ferr != nil {
			return ferr
		}
		f.Close()
		pdfPath = f.Name()
	} else {
		outDir, err := expandHome(cfg.Invoice.OutputDir)
		if err != nil {
			return err
		}
		senderDir := filepath.Join(outDir, senderKey)
		if err := os.MkdirAll(senderDir, 0o700); err != nil {
			return err
		}
		pdfPath = filepath.Join(senderDir, number+".pdf")
	}
	if err := invoice.RenderPDF(doc, pdfPath); err != nil {
		_ = os.Remove(pdfPath)
		return err
	}

	var invoiceID int64
	if !args.DryRun {
		id, err := qtx.InsertInvoiceFull(ctx, store.InsertInvoiceFullParams{
			CompanyID:  co.ID,
			SentAt:     now.Unix(),
			Note:       args.Note,
			CreatedAt:  time.Now().Unix(),
			Number:     sql.NullString{String: number, Valid: true},
			PdfPath:    sql.NullString{String: pdfPath, Valid: true},
			TotalCents: sql.NullInt64{Int64: doc.AmountDueCents(), Valid: true},
			Currency:   sql.NullString{String: co.Currency.String, Valid: true},
			SenderKey:  sql.NullString{String: senderKey, Valid: true},
		})
		if err != nil {
			_ = os.Remove(pdfPath)
			return err
		}
		invoiceID = id
	}

	if err := tx.Commit(); err != nil {
		_ = os.Remove(pdfPath)
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	reply.InvoiceID = invoiceID
	reply.Number = number
	reply.PDFPath = pdfPath
	reply.TotalCents = doc.AmountDueCents()
	reply.Currency = doc.Currency
	reply.SenderKey = senderKey
	reply.IssueDateUnix = doc.IssueDate.Unix()
	reply.DueDateUnix = doc.DueDate.Unix()
	return nil
}
