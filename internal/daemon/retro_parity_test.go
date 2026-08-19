package daemon

import (
	"testing"

	"github.com/rian/antitimely/internal/domain"
)

// retroCwdProbeSQL mirrors the cwd clause of ApplyRuleRetroactivelyCounted
// (queries.sql). Keep the two in sync by hand whenever that clause changes:
// sqlc exposes no reusable string fragment to share between the query and a
// test, so this duplication is deliberate (approved), and this test is the
// only thing that catches the two drifting apart.
const retroCwdProbeSQL = `
SELECT cwd FROM observations
WHERE CASE WHEN instr(?, '*') > 0
           THEN (cwd GLOB rtrim(?, '/') OR cwd GLOB rtrim(?, '/') || '/*')
           ELSE (cwd = rtrim(?, '/') OR cwd LIKE rtrim(?, '/') || '/%')
      END`

// TestRetroMatcherParity asserts the SQL used by ApplyRuleRetroactivelyCounted
// selects exactly the cwds that domain.MatchesCwd accepts. A divergence here
// silently retags the wrong ticks.
func TestRetroMatcherParity(t *testing.T) {
	const wt = "/repo/.claude/worktrees"
	corpus := []string{
		"/repo", "/repo/daas-back-end",
		wt, wt + "/md-tracker", wt + "/md-tracker/DAAS.API",
		wt + "/md-tracker-scaffold", wt + "/md-engine",
		wt + "/packing-slip", "/repo-other", "/repo-other/x",
		"/repo/mb-tracker", "/repo/mb-tracker-foo",
	}
	patterns := []string{
		"/repo", "/repo/", wt + "/md-*", wt + "/md-*/", wt + "/md-tracker",
		"/repo/mb-tracker",
	}

	_, _, _, db := newTestPipeline(t)
	defer db.Close()

	for _, cwd := range corpus {
		if _, err := db.Exec(
			`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent',?,'',1)`, cwd); err != nil {
			t.Fatal(err)
		}
	}

	for _, pat := range patterns {
		rows, err := db.Query(retroCwdProbeSQL, pat, pat, pat, pat, pat)
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

		for _, cwd := range corpus {
			want := domain.MatchesCwd(pat, cwd)
			if sqlMatched[cwd] != want {
				t.Errorf("pattern %q cwd %q: SQL=%v Go=%v", pat, cwd, sqlMatched[cwd], want)
			}
		}
	}
}
