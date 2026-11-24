package daemon

import (
	"context"
	"database/sql"
	"testing"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/store"
)

const wt = "/repo/.claude/worktrees"

var retroCorpus = []string{
	"/repo", "/repo/daas-back-end",
	`C:\Work\Project`, `c:/work/PROJECT/src`, `D:\Work\Project`, `/mnt/c/work/project`,
	`\\NAS\Share\Project\src`, `\\wsl$\Ubuntu\home\Rian\app`,
	`\\wsl$\Ubuntu\home\rian\app`,
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
	`C:\Work\Project\`, `c:/work/proj*`, `\\?\UNC\nas\share\project`,
	`\\wsl.localhost\ubuntu\home\Rian`,
	"/x/myrepo",  // case-sensitivity probe
	"/x/my_repo", // underscore-wildcard probe
	"/",          // empty-after-rtrim probe: must match nothing
}

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
			CwdPattern: sql.NullString{String: pat, Valid: true},
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
