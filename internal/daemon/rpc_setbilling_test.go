package daemon

import (
	"testing"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestRPC_SetCompanyBilling(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "BClouder"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(rpcapi.ServiceName+".SetCompanyBilling",
		rpcapi.SetCompanyBillingArgs{
			Name: "BClouder", BillingMode: "hourly",
			Currency: "CAD", RateCents: 5000, BilledFrom: "es",
		}, &rpcapi.SetCompanyBillingReply{}); err != nil {
		t.Fatal(err)
	}
	row := db.QueryRow("SELECT billing_mode, currency, rate_cents, billed_from FROM companies WHERE name='BClouder'")
	var bm, curr, bf string
	var rate int64
	if err := row.Scan(&bm, &curr, &rate, &bf); err != nil {
		t.Fatal(err)
	}
	if bm != "hourly" || curr != "CAD" || rate != 5000 || bf != "es" {
		t.Errorf("got %s/%s/%d/%s", bm, curr, rate, bf)
	}
}
