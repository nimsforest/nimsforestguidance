package guidance

import (
	"path/filepath"
	"testing"
)

func example() Forecast {
	n := int64(10000)
	return Forecast{Entity: "Example operator", EntityRef: "operator-test", Owner: "COO", Scenario: "base", AsOf: "2026-09-16", Horizon: "2027-09-16", Currency: "EUR", Opening: &n, OpeningStatus: "verified", OpeningEvidence: "Statement 16 September", Buffer: 1000, Complete: true, Assumptions: "Expected cash dates reviewed", Status: "draft", Movements: []Movement{{ID: "bill", Date: "2026-09-20", Kind: "payment", Amount: 15000, Certainty: "contracted"}, {ID: "sale", Date: "2026-09-18", Kind: "receipt", Amount: 2000, Certainty: "estimated"}}}
}
func TestFundingTimingAndPeakShortfall(t *testing.T) {
	f := example()
	v := Calculate(f, "")
	if v.Need != 4000 || v.Breach != "2026-09-20" {
		t.Fatalf("unexpected funding view: %+v", v)
	}
	f.Movements = append(f.Movements, Movement{ID: "finance", Date: "2026-09-19", Kind: "financing", Amount: 2500, Certainty: "committed", Evidence: "Signed facility; draw available"})
	v = Calculate(f, "")
	if v.Need != 1500 {
		t.Fatalf("on-time financing: %+v", v)
	}
	f.Movements[2].Date = "2026-09-25"
	v = Calculate(f, "")
	if v.Need != 4000 {
		t.Fatalf("late financing hid earlier breach: %+v", v)
	}
	f.Movements[2].Certainty = "proposed"
	v = Calculate(f, "")
	if v.Financing != 0 || v.Proposed != 2500 || v.Need != 4000 {
		t.Fatalf("proposed financing counted: %+v", v)
	}
	f.Movements = append(f.Movements, Movement{ID: "later", Date: "2026-10-10", Kind: "payment", Amount: 1000, Certainty: "estimated"})
	v = Calculate(f, "")
	if v.Need != 5000 {
		t.Fatalf("successive shortfalls should use peak, not sum: %+v", v)
	}
	if Calculate(f, "2026-09-19").Need != 0 {
		t.Fatal("horizon included later payment")
	}
	f.Opening = nil
	if Calculate(f, "").Known {
		t.Fatal("missing cash became zero")
	}
}
func TestSourceAdjustmentsAndApprovalInputs(t *testing.T) {
	f := example()
	f.Movements[0].Original = &SourceValue{Amount: 14000, Date: f.Movements[0].Date}
	if Validate(f, false) == nil {
		t.Fatal("source adjustment accepted without reason")
	}
	f.Movements[0].Adjustment = "Supplier updated quote"
	if e := Validate(f, true); e != nil {
		t.Fatal(e)
	}
	f.Complete = false
	if Validate(f, true) == nil {
		t.Fatal("incomplete schedule approved")
	}
	f.Complete = true
	f.Restricted = 11000
	if Validate(f, false) == nil {
		t.Fatal("restricted cash exceeded cash")
	}
	f.Restricted = 0
	f.Currency = "JPY"
	if Validate(f, false) == nil {
		t.Fatal("unsupported currency accepted")
	}
}
func TestApprovedHistoryIdempotencyAndRestore(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(filepath.Join(dir, "guidance.db"), "test")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	c := Command{Ref: "create", Org: "test", Action: "save", Forecast: example(), Actor: "coo"}
	r, e := s.Apply(c)
	if e != nil || r.Status != "applied" {
		t.Fatalf("save: %+v %v", r, e)
	}
	c.Forecast.Entity = "Changed on redelivery"
	again, e := s.Apply(c)
	if e != nil || again.ID != r.ID || again.Revision != 1 {
		t.Fatal("redelivery duplicated create")
	}
	f, _ := s.Get(r.ID)
	if f.Entity != "Example operator" {
		t.Fatal("redelivery changed stored record")
	}
	run := func(ref, action string, expected int, admin bool) Result {
		rr, err := s.Apply(Command{Ref: ref, Org: "test", Action: action, Forecast: Forecast{ID: r.ID}, Expected: expected, Actor: "coo", CanApprove: admin})
		if err != nil {
			t.Fatal(err)
		}
		return rr
	}
	if run("submit", "submit", 1, false).Status != "applied" {
		t.Fatal("submit failed")
	}
	if run("unauthorized", "review", 2, false).Status != "failed" {
		t.Fatal("nonadmin reviewed")
	}
	if run("review", "review", 2, true).Status != "applied" || run("approve", "approve", 3, true).Status != "applied" {
		t.Fatal("review/approval failed")
	}
	f, _ = s.Get(r.ID)
	if f.Status != "approved" || f.ApprovedBy != "coo" {
		t.Fatal("approval audit lost")
	}
	if run("edit-approved", "save", 4, true).Status != "failed" {
		t.Fatal("approved snapshot editable")
	}
	rev := run("revise", "revise", 4, false)
	if rev.Status != "applied" || rev.ID == r.ID {
		t.Fatal("revision did not create new snapshot")
	}
	newf, _ := s.Get(rev.ID)
	if newf.Previous != r.ID || newf.Status != "draft" || newf.ApprovedBy != "" {
		t.Fatal("revision lineage/approval wrong")
	}
	original, _ := s.Get(r.ID)
	if original.Status != "approved" {
		t.Fatal("revision mutated approval")
	}
	if _, e = s.Apply(Command{Ref: "cross-org", Org: "other", Action: "save", Forecast: example(), Actor: "coo"}); e == nil {
		t.Fatal("cross-org command accepted")
	}
	backup := filepath.Join(dir, "backup.db")
	if e = s.Backup(backup); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(backup, "test")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.DB.Close()
	recovered, _ := restored.Get(r.ID)
	if recovered.ApprovedAt != f.ApprovedAt || recovered.Revision != 4 {
		t.Fatal("backup did not preserve approval")
	}
	if _, e = Open(backup, "other"); e == nil {
		t.Fatal("database tenancy mismatch accepted")
	}
}
func TestMoneyPrecision(t *testing.T) {
	for input, want := range map[string]int64{"0": 0, "12.3": 1230, "12.34": 1234, "-0.01": -1} {
		n, e := Money(input)
		if e != nil || n != want {
			t.Fatalf("%s => %d %v", input, n, e)
		}
	}
	for _, input := range []string{"1.234", "1e3", "NaN", "1,000", ""} {
		if _, e := Money(input); e == nil {
			t.Fatalf("invalid money accepted: %s", input)
		}
	}
}
