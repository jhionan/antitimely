package store

import (
	"database/sql/driver"

	"github.com/rian/antitimely/internal/domain"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("antitimely_cwd_matches", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			pattern, ok := args[0].(string)
			cwd, valid := args[1].(string)
			return ok && valid && domain.MatchesCwd(pattern, cwd), nil
		})
}
