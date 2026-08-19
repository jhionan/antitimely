package daemon

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/store"
)

// retroCwdCaseSQL mirrors the cwd clause of ApplyRuleRetroactivelyCounted
// (queries.sql) verbatim, byte for byte, including indentation. Keep the two
// in sync by hand whenever that clause changes: sqlc exposes no reusable
// string fragment to share between the query and a test, so this
// duplication is deliberate (approved). TestRetroCwdClauseNotStale asserts
// this constant is still a literal substring of queries.sql, so a hand-edit
// to one side without the other fails loudly instead of silently drifting.
const retroCwdCaseSQL = `CASE WHEN rtrim(?, '/') = ''
                               THEN 0
                               WHEN instr(?, '*') > 0
                                    THEN (cwd GLOB rtrim(?, '/') OR cwd GLOB rtrim(?, '/') || '/*')
                               ELSE (cwd = rtrim(?, '/') OR substr(cwd, 1, length(rtrim(?, '/')) + 1) = rtrim(?, '/') || '/')
                          END`

// retroCwdProbeSQL wraps retroCwdCaseSQL in a standalone SELECT so it can be
// run directly against a test database.
const retroCwdProbeSQL = `
SELECT cwd FROM observations
WHERE ` + retroCwdCaseSQL

// TestRetroCwdClauseNotStale guards the duplication above: if
// ApplyRuleRetroactivelyCounted's cwd clause changes in queries.sql without
// retroCwdCaseSQL being updated to match, this fails immediately instead of
// leaving TestRetroMatcherParity silently exercising a stale copy that no
// longer reflects production.
func TestRetroCwdClauseNotStale(t *testing.T) {
	b, err := os.ReadFile("../../queries.sql")
	if err != nil {
		t.Fatalf("read queries.sql: %v", err)
	}
	if !strings.Contains(string(b), retroCwdCaseSQL) {
		t.Fatal("retroCwdCaseSQL is no longer a substring of queries.sql - " +
			"ApplyRuleRetroactivelyCounted's cwd clause changed without " +
			"updating the mirror in retro_parity_test.go; update retroCwdCaseSQL " +
			"to match the clause in queries.sql verbatim (including indentation)")
	}
}

const wt = "/repo/.claude/worktrees"

// retroCorpus is the set of observed cwds; retroPatterns is the set of rule
// cwd patterns. Both are shared between TestRetroMatcherParity (which
// exercises the mirrored SQL text directly) and
// TestRetroMatcherParityRealQuery (which exercises the actual generated
// store.ApplyRuleRetroactivelyCounted), so a fix to one is checked against
// both the mirror and the real query with the same fixture.
var retroCorpus = []string{
	"/repo", "/repo/daas-back-end",
	wt, wt + "/md-tracker", wt + "/md-tracker/DAAS.API",
	wt + "/md-tracker-scaffold", wt + "/md-engine",
	wt + "/packing-slip", "/repo-other", "/repo-other/x",
	"/repo/mb-tracker", "/repo/mb-tracker-foo",
	// Case-sensitivity probe: pattern "/x/myrepo" must match this cwd...
	"/x/myrepo/sub",
	// ...but not this one - same prefix modulo letter case.
	"/x/MyRepo/sub",
	// Underscore probe: pattern "/x/my_repo" must NOT match this cwd via
	// SQL LIKE's "_ matches any single character" wildcard semantics.
	"/x/myXrepo/sub",
	// ...but must match this one, where the underscore is a literal byte
	// in both the pattern and the cwd.
	"/x/my_repo/sub",
}
var retroPatterns = []string{
	"/repo", "/repo/", wt + "/md-*", wt + "/md-*/", wt + "/md-tracker",
	"/repo/mb-tracker",
	"/x/myrepo",  // case-sensitivity probe
	"/x/my_repo", // underscore-wildcard probe
	"/",          // empty-after-rtrim probe: must match nothing
}

// probeArgs repeats pat once per "?" placeholder in sql. Computing the
// count from the query text itself, rather than hardcoding it, means a
// future edit that adds or removes a placeholder can't silently desync the
// argument list from the query.
func probeArgs(sqlText, pat string) []any {
	n := strings.Count(sqlText, "?")
	args := make([]any, n)
	for i := range args {
		args[i] = pat
	}
	return args
}

// TestRetroMatcherParity asserts the mirrored SQL text (retroCwdProbeSQL)
// selects exactly the cwds that domain.MatchesCwd accepts. This only proves
// the mirror is internally consistent with the Go matcher; it does NOT
// exercise the generated store.ApplyRuleRetroactivelyCounted at all, so it
// cannot catch queries.sql drifting away from its own mirror -
// TestRetroCwdClauseNotStale covers that, and TestRetroMatcherParityRealQuery
// covers the production code path directly.
func TestRetroMatcherParity(t *testing.T) {
	_, _, _, db := newTestPipeline(t)
	defer db.Close()

	for _, cwd := range retroCorpus {
		if _, err := db.Exec(
			`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent',?,'',1)`, cwd); err != nil {
			t.Fatal(err)
		}
	}

	for _, pat := range retroPatterns {
		rows, err := db.Query(retroCwdProbeSQL, probeArgs(retroCwdProbeSQL, pat)...)
		if err != nil {
			t.Fatalf("probe %q: %v", pat, err)
		}
		sqlMatched := map[string]bool{}
		for rows.Next() {
			var cwd string
			if err := rows.Scan(&cwd); err != nil {
				t.Fatal(err)
			}
			sqlMatched[cwd] = true
		}
		rows.Close()

		for _, cwd := range retroCorpus {
			want := domain.MatchesCwd(pat, cwd)
			if sqlMatched[cwd] != want {
				t.Errorf("mirror probe: pattern %q cwd %q: SQL=%v Go=%v", pat, cwd, sqlMatched[cwd], want)
			}
		}
	}
}

// TestRetroMatcherParityRealQuery drives the actual generated
// store.ApplyRuleRetroactivelyCounted over the same corpus/pattern fixture,
// with every other match column left unconstrained (NULL), and asserts it
// retags exactly the ticks whose observation cwd domain.MatchesCwd accepts.
// This is the test that would catch a regression introduced in queries.sql
// alone (e.g. reverting the '/%' boundary fix) even if TestRetroMatcherParity
// (which only exercises the hand-mirrored SQL text) stayed green.
func TestRetroMatcherParityRealQuery(t *testing.T) {
	_, _, _, db := newTestPipeline(t)
	defer db.Close()
	q := store.New(db)
	ctx := context.Background()

	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "retro-parity-target", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	type observation struct {
		id  int64
		cwd string
	}
	obs := make([]observation, 0, len(retroCorpus))
	for i, cwd := range retroCorpus {
		var id int64
		if err := db.QueryRow(
			`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent',?,'',1) RETURNING id`,
			cwd,
		).Scan(&id); err != nil {
			t.Fatalf("insert observation %q: %v", cwd, err)
		}
		obs = append(obs, observation{id: id, cwd: cwd})
		// One unassigned tick per observation - the query only retags ticks
		// with project_id IS NULL.
		if _, err := db.Exec(
			`INSERT INTO ticks (ts, observation_id, project_id) VALUES (?, ?, NULL)`,
			int64(i)+1, id,
		); err != nil {
			t.Fatalf("insert tick for %q: %v", cwd, err)
		}
	}

	for _, pat := range retroPatterns {
		// Every pattern's run starts from an all-unassigned state; otherwise
		// an earlier pattern's correct retag would mask a later pattern's
		// under-match (the WHERE project_id IS NULL guard would just skip
		// the already-tagged tick instead of re-evaluating it).
		if _, err := db.Exec(`UPDATE ticks SET project_id = NULL`); err != nil {
			t.Fatalf("reset ticks before pattern %q: %v", pat, err)
		}

		if _, err := q.ApplyRuleRetroactivelyCounted(ctx, store.ApplyRuleRetroactivelyCountedParams{
			ProjectID:  sql.NullInt64{Int64: projID, Valid: true},
			Column2:    nil, // bundle: unconstrained
			BundleID:   "",
			Column4:    nil, // title: unconstrained
			Column5:    sql.NullString{},
			Column6:    nil, // binary: unconstrained
			BinaryName: "",
			Column8:    nil, // space: unconstrained
			SpaceID:    "",
			Column10:   pat, // cwd: constrained to this pattern
			RTRIM:      pat,
			INSTR:      pat,
			RTRIM_2:    pat,
			RTRIM_3:    pat,
			RTRIM_4:    pat,
			RTRIM_5:    pat,
			RTRIM_6:    pat,
		}); err != nil {
			t.Fatalf("ApplyRuleRetroactivelyCounted %q: %v", pat, err)
		}

		retagged := map[string]bool{}
		rows, err := db.Query(
			`SELECT o.cwd FROM ticks t JOIN observations o ON o.id = t.observation_id WHERE t.project_id = ?`,
			projID,
		)
		if err != nil {
			t.Fatalf("query retagged for %q: %v", pat, err)
		}
		for rows.Next() {
			var cwd string
			if err := rows.Scan(&cwd); err != nil {
				t.Fatal(err)
			}
			retagged[cwd] = true
		}
		rows.Close()

		for _, o := range obs {
			want := domain.MatchesCwd(pat, o.cwd)
			if retagged[o.cwd] != want {
				t.Errorf("real query: pattern %q cwd %q: retagged=%v Go=%v", pat, o.cwd, retagged[o.cwd], want)
			}
		}
	}
}
