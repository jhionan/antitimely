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

	// Read the credit balance BEFORE BeginTx: the DB is SetMaxOpenConns(1),
	// so a s.Q query issued once the transaction holds that single
	// connection deadlocks until the handler's context deadline.
	var creditRemaining int64
	var creditRef string
	if !args.NoCredit {
		creditRemaining, err = s.Q.CompanyCreditBalance(ctx, store.CompanyCreditBalanceParams{
			CompanyID: co.ID,
			Currency:  co.Currency,
		})
		if err != nil {
			return fmt.Errorf("read credit balance: %w", err)
		}
		rows, err := s.Q.CompanyCreditRows(ctx, store.CompanyCreditRowsParams{
			CompanyID: co.ID,
			Currency:  co.Currency,
		})
		if err != nil {
			return fmt.Errorf("read credit rows: %w", err)
		}
		// FIFO: the oldest advance is the one we name on the document.
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Kind == "advance" && rows[i].Number.Valid {
				creditRef = rows[i].Number.String
				break
			}
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

	lineTotal := invoice.ComputeLineItem(co.BillingMode, ticks, s.TickIntervalSeconds, co.RateCents.Int64).TotalCents
	applied := invoice.ApplyCredit(creditRemaining, lineTotal, args.DiscountCents)
	if applied == 0 {
		creditRef = ""
	}

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

		CreditAppliedCents: applied,
		CreditAppliedRef:   creditRef,
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
		// Per-sender output_dir wins and is used directly; otherwise fall back
		// to the global output_dir with a <senderKey>/ subfolder.
		var senderDir string
		if sender.OutputDir != "" {
			senderDir, err = expandHome(sender.OutputDir)
			if err != nil {
				return err
			}
		} else {
			outDir, err := expandHome(cfg.Invoice.OutputDir)
			if err != nil {
				return err
			}
			senderDir = filepath.Join(outDir, senderKey)
		}
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

			Kind:               "hourly",
			CreditAppliedCents: applied,
			DiscountCents:      args.DiscountCents,
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
	reply.FromUnix = from.Unix()
	reply.ToUnix = to.Unix()
	reply.Ticks = ticks
	reply.BillingMode = co.BillingMode
	reply.CreditAppliedCents = applied
	reply.CreditRemainingCents = creditRemaining - applied
	return nil
}

// InvoiceAdvance records a prepayment: a company pays money up front, before
// any hours are billed. It mirrors InvoiceGenerate's config/sender/number/
// render/insert sequence but deliberately differs in three ways: it never
// reads or applies existing credit (a fresh advance must not be discounted
// by a balance that predates it), it never moves the billing anchor (an
// advance bills no hours, so LastInvoiceSentForCompany already excludes
// kind='advance' rows — see Task 5), and its LineItem is constructed
// directly from the amount rather than derived from tick counts.
func (s *AntitimelyService) InvoiceAdvance(args rpcapi.InvoiceAdvanceArgs, reply *rpcapi.InvoiceAdvanceReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()

	if args.AmountCents <= 0 {
		return fmt.Errorf("advance amount must be positive (got %d cents)", args.AmountCents)
	}

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
	if co.RateCents.Int64 <= 0 {
		return fmt.Errorf("company %q has no rate; cannot express an advance in hours", co.Name)
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

	// Read the credit balance BEFORE BeginTx (same single-connection
	// deadlock hazard as InvoiceGenerate): the reply reports the balance
	// *after* this advance, but this advance itself never draws it down.
	creditBefore, err := s.Q.CompanyCreditBalance(ctx, store.CompanyCreditBalanceParams{
		CompanyID: co.ID,
		Currency:  co.Currency,
	})
	if err != nil {
		return fmt.Errorf("read credit balance: %w", err)
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

	// Hourly line shape: amount / rate hours at rate. Exact when the amount is
	// a whole multiple of the rate's cents-per-hour; the LineItem carries the
	// authoritative total either way.
	li := invoice.LineItem{
		QuantityHoursTimes100: args.AmountCents * 100 / co.RateCents.Int64,
		UnitCents:             co.RateCents.Int64,
		TotalCents:            args.AmountCents,
	}

	bank, ok := sender.BankFor(co.Currency.String)
	if !ok {
		return fmt.Errorf("sender has no bank account for currency %q (and no also_accepts fallback)", co.Currency.String)
	}
	doc := invoice.InvoiceDoc{
		Number:        number,
		IssueDate:     now,
		DueDate:       now.AddDate(0, 0, cfg.Invoice.DueDays),
		Currency:      co.Currency.String,
		ClientName:    co.Name,
		Client:        cfg.Clients[co.Name], // zero value if no clients entry
		Sender:        sender,
		LineItemLabel: cfg.Invoice.LineItemLabel,
		LineItem:      li,
		Bank:          bank,
		LogoPath:      sender.LogoPath,
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
		// Per-sender output_dir wins and is used directly; otherwise fall back
		// to the global output_dir with a <senderKey>/ subfolder.
		var senderDir string
		if sender.OutputDir != "" {
			senderDir, err = expandHome(sender.OutputDir)
			if err != nil {
				return err
			}
		} else {
			outDir, err := expandHome(cfg.Invoice.OutputDir)
			if err != nil {
				return err
			}
			senderDir = filepath.Join(outDir, senderKey)
		}
		if err := os.MkdirAll(senderDir, 0o700); err != nil {
			return err
		}
		pdfPath = filepath.Join(senderDir, number+".pdf")
	}
	if err := invoice.RenderPDF(doc, pdfPath); err != nil {
		_ = os.Remove(pdfPath)
		return err
	}

	if !args.DryRun {
		if _, err := qtx.InsertInvoiceFull(ctx, store.InsertInvoiceFullParams{
			CompanyID:  co.ID,
			SentAt:     now.Unix(),
			Note:       args.Note,
			CreatedAt:  time.Now().Unix(),
			Number:     sql.NullString{String: number, Valid: true},
			PdfPath:    sql.NullString{String: pdfPath, Valid: true},
			TotalCents: sql.NullInt64{Int64: doc.AmountDueCents(), Valid: true},
			Currency:   sql.NullString{String: co.Currency.String, Valid: true},
			SenderKey:  sql.NullString{String: senderKey, Valid: true},

			Kind:               "advance",
			CreditAppliedCents: 0,
			DiscountCents:      0,
		}); err != nil {
			_ = os.Remove(pdfPath)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		_ = os.Remove(pdfPath)
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	reply.Number = number
	reply.PDFPath = pdfPath
	reply.Currency = doc.Currency
	reply.TotalCents = doc.AmountDueCents()
	reply.CreditRemainingCents = creditBefore + doc.AmountDueCents()
	return nil
}
