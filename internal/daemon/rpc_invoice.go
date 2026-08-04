package daemon

import (
	"context"
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

// loadValidSender loads the senders/invoice config, validates it, and
// resolves senderKey to its Sender entry. Shared by every RPC handler that
// issues an invoice (InvoiceGenerate, InvoiceAdvance): each needs the same
// config-load-validate-lookup sequence and must fail the same way when the
// config is missing/invalid or the sender key isn't registered.
func loadValidSender(senderKey string) (*invoice.SendersConfig, invoice.Sender, error) {
	cfgPath, err := configPath()
	if err != nil {
		return nil, invoice.Sender{}, err
	}
	cfg, err := invoice.LoadSendersConfig(cfgPath)
	if err != nil {
		return nil, invoice.Sender{}, err
	}
	if issues := cfg.Validate(); len(issues) > 0 {
		return nil, invoice.Sender{}, fmt.Errorf("invalid senders config: %v", issues)
	}
	sender, ok := cfg.Senders[senderKey]
	if !ok {
		return nil, invoice.Sender{}, fmt.Errorf("sender %q not in config (run `atl invoice show-senders`)", senderKey)
	}
	return cfg, sender, nil
}

// allocateInvoiceNumber returns the invoice number to stamp on this
// document. On a real run it durably allocates+increments the sender's
// counter inside the caller's transaction. On a dry run it only *peeks* at
// the counter (no mutation) so a preview never consumes a real number;
// sender_state may not have a row yet (never-invoiced sender), in which
// case it falls back to the config's configured next number.
func allocateInvoiceNumber(ctx context.Context, tx *sql.Tx, qtx *store.Queries, dryRun bool, senderKey string, configuredNext int64) (int64, error) {
	if !dryRun {
		return qtx.AllocateNextInvoiceNumber(ctx, senderKey)
	}
	row := tx.QueryRowContext(ctx, "SELECT next_invoice_number FROM sender_state WHERE sender_key = ?", senderKey)
	var allocated int64
	if err := row.Scan(&allocated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return configuredNext, nil
		}
		return 0, err
	}
	return allocated, nil
}

// resolvePDFPath returns the path a rendered invoice PDF should be written
// to. On a dry run it's a throwaway temp file (never surfaced to the
// client's directory). On a real run it's <senderDir>/<number>.pdf, where
// senderDir is the sender's own output_dir if set, else the global
// output_dir with a <senderKey>/ subfolder — creating that directory as
// needed.
func resolvePDFPath(cfg *invoice.SendersConfig, sender invoice.Sender, senderKey, number string, dryRun bool) (string, error) {
	if dryRun {
		f, err := os.CreateTemp("", "atl-dryrun-*.pdf")
		if err != nil {
			return "", err
		}
		f.Close()
		return f.Name(), nil
	}
	var senderDir string
	if sender.OutputDir != "" {
		d, err := expandHome(sender.OutputDir)
		if err != nil {
			return "", err
		}
		senderDir = d
	} else {
		outDir, err := expandHome(cfg.Invoice.OutputDir)
		if err != nil {
			return "", err
		}
		senderDir = filepath.Join(outDir, senderKey)
	}
	if err := os.MkdirAll(senderDir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(senderDir, number+".pdf"), nil
}

// renderInvoicePDF renders doc and returns the path it was actually written
// to. On a real run that is a hidden, dot-prefixed temp file beside pdfPath
// (same directory => same filesystem, so the later os.Rename in
// finalizeInvoicePDF is atomic) — never pdfPath itself. A numbered PDF must
// not exist at its final, client-facing path until the invoice row that
// describes it has committed: otherwise a daemon crash or handler-deadline
// abort between render and commit leaves a complete, numbered PDF on disk
// with no corresponding row, and a client-facing filename that looks final
// while nothing durable backs it. On a dry run pdfPath is already a
// throwaway os.CreateTemp file (see resolvePDFPath) with no client-facing
// counterpart, so it renders straight to it.
func renderInvoicePDF(doc invoice.InvoiceDoc, pdfPath string, dryRun bool) (renderedAt string, err error) {
	if dryRun {
		if err := invoice.RenderPDF(doc, pdfPath); err != nil {
			_ = os.Remove(pdfPath)
			return "", err
		}
		return pdfPath, nil
	}
	tmpPath := filepath.Join(filepath.Dir(pdfPath), "."+filepath.Base(pdfPath)+".tmp")
	if err := invoice.RenderPDF(doc, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

// finalizeInvoicePDF moves a rendered PDF into its final, client-facing path
// after the transaction that inserted its row has committed. Must only be
// called post-commit: the row is the source of truth, and this is what
// lands the file to match it. If the rename itself fails, the invoice row
// already exists (the caller should NOT retry the whole operation, which
// would allocate and consume credit against a second number) — the error
// names both paths so the file can be moved into place by hand.
func finalizeInvoicePDF(renderedAt, pdfPath, number string) error {
	if renderedAt == pdfPath {
		return nil // dry run: rendered straight to its (temp) destination
	}
	if err := os.Rename(renderedAt, pdfPath); err != nil {
		return fmt.Errorf(
			"invoice %s committed but its PDF could not be moved from %s into place at %s: %w "+
				"(the invoice row already exists — move the file by hand rather than retrying)",
			number, renderedAt, pdfPath, err)
	}
	return nil
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

	senderKey := co.BilledFrom.String
	cfg, sender, err := loadValidSender(senderKey)
	if err != nil {
		return err
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

	allocatedNumber, err := allocateInvoiceNumber(ctx, tx, qtx, args.DryRun, senderKey, sender.Invoice.NextNumber)
	if err != nil {
		return fmt.Errorf("allocate invoice number: %w", err)
	}

	number := invoice.FormatInvoiceNumber(sender.Invoice.NumberPrefix, sender.Invoice.NumberPad, allocatedNumber)

	lineTotal := invoice.ComputeLineItem(co.BillingMode, ticks, s.TickIntervalSeconds, co.RateCents.Int64).TotalCents
	applied := invoice.ApplyCredit(creditRemaining, lineTotal, args.DiscountCents)
	if applied == 0 {
		creditRef = ""
	}

	doc, err := invoice.BuildDoc(invoice.BuildDocInput{
		Now:        now,
		ClientName: co.Name,
		Client:     cfg.Clients[co.Name], // zero value if no clients entry

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

	pdfPath, err := resolvePDFPath(cfg, sender, senderKey, number, args.DryRun)
	if err != nil {
		return err
	}
	renderedAt, err := renderInvoicePDF(doc, pdfPath, args.DryRun)
	if err != nil {
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
			_ = os.Remove(renderedAt)
			return err
		}
		invoiceID = id
	}

	if err := tx.Commit(); err != nil {
		_ = os.Remove(renderedAt)
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	if err := finalizeInvoicePDF(renderedAt, pdfPath, number); err != nil {
		return err
	}

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

	senderKey := co.BilledFrom.String
	cfg, sender, err := loadValidSender(senderKey)
	if err != nil {
		return err
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

	allocatedNumber, err := allocateInvoiceNumber(ctx, tx, qtx, args.DryRun, senderKey, sender.Invoice.NextNumber)
	if err != nil {
		return fmt.Errorf("allocate invoice number: %w", err)
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
	// Advances have no billing period (no ticks are being billed), but the
	// PDF renders the period line unconditionally. To keep an advance
	// visually consistent with a normal invoice, show the issue date through
	// the end of that calendar month: PeriodFrom = issue date, PeriodTo =
	// first day of the following month (exclusive — RenderPDF prints
	// PeriodTo.AddDate(0,0,-1) as the shown end date).
	periodFrom := now
	periodTo := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
	doc := invoice.InvoiceDoc{
		Number:        number,
		IssueDate:     now,
		DueDate:       now.AddDate(0, 0, cfg.Invoice.DueDays),
		PeriodFrom:    periodFrom,
		PeriodTo:      periodTo,
		Currency:      co.Currency.String,
		ClientName:    co.Name,
		Client:        cfg.Clients[co.Name], // zero value if no clients entry
		Sender:        sender,
		LineItemLabel: cfg.Invoice.LineItemLabel,
		LineItem:      li,
		Bank:          bank,
		LogoPath:      sender.LogoPath,
	}

	pdfPath, err := resolvePDFPath(cfg, sender, senderKey, number, args.DryRun)
	if err != nil {
		return err
	}
	renderedAt, err := renderInvoicePDF(doc, pdfPath, args.DryRun)
	if err != nil {
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
			_ = os.Remove(renderedAt)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		_ = os.Remove(renderedAt)
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	if err := finalizeInvoicePDF(renderedAt, pdfPath, number); err != nil {
		return err
	}

	reply.Number = number
	reply.PDFPath = pdfPath
	reply.Currency = doc.Currency
	reply.TotalCents = doc.AmountDueCents()
	reply.CreditRemainingCents = creditBefore + doc.AmountDueCents()
	return nil
}

// InvoiceBalance is a read-only report of a company's remaining advance
// credit and the ledger of advances/drawdowns that produced it. It never
// writes rows, allocates an invoice number, or moves the billing anchor.
func (s *AntitimelyService) InvoiceBalance(args rpcapi.InvoiceBalanceArgs, reply *rpcapi.InvoiceBalanceReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()

	co, err := s.Q.GetCompanyForInvoice(ctx, args.CompanyName)
	if err != nil {
		return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
	}

	remaining, err := s.Q.CompanyCreditBalance(ctx, store.CompanyCreditBalanceParams{
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

	reply.Currency = co.Currency.String
	reply.RemainingCents = remaining
	reply.RateCents = co.RateCents.Int64
	reply.Rows = make([]rpcapi.InvoiceBalanceRow, 0, len(rows))
	for _, r := range rows {
		if !r.Number.Valid {
			continue
		}
		reply.Rows = append(reply.Rows, rpcapi.InvoiceBalanceRow{
			Number:             r.Number.String,
			Kind:               r.Kind,
			TotalCents:         r.TotalCents.Int64,
			CreditAppliedCents: r.CreditAppliedCents,
		})
	}
	return nil
}
