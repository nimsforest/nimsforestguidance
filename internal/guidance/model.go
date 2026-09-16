package guidance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ModelVersion = "cash-v1"

type Movement struct {
	ID              string       `json:"id"`
	Date            string       `json:"date"`
	Kind            string       `json:"kind"`   // receipt, payment, financing
	Amount          int64        `json:"amount"` // two-decimal currency minor units
	Category        string       `json:"category"`
	Description     string       `json:"description"`
	Certainty       string       `json:"certainty"` // estimated, contracted, committed, conditional, proposed
	Growth          bool         `json:"growth"`
	Evidence        string       `json:"evidence"`
	Source          string       `json:"source"`
	SourceRef       string       `json:"source_ref"`
	ImportedAt      string       `json:"imported_at"`
	SourceDate      string       `json:"source_date"`
	OriginSignature string       `json:"origin_signature,omitempty"`
	Original        *SourceValue `json:"original,omitempty"`
	Adjustment      string       `json:"adjustment"`
	TransferRef     string       `json:"transfer_ref"`
}
type SourceValue struct {
	Amount      int64  `json:"amount"`
	Date        string `json:"date"`
	Description string `json:"description"`
}

type Forecast struct {
	ID              string     `json:"id"`
	Org             string     `json:"org"`
	Entity          string     `json:"entity"`
	EntityRef       string     `json:"entity_ref"`
	Brand           string     `json:"brand"`
	Operator        string     `json:"operator"`
	Location        string     `json:"location"`
	Owner           string     `json:"owner"`
	Scenario        string     `json:"scenario"`
	AsOf            string     `json:"as_of"`
	Horizon         string     `json:"horizon"`
	Currency        string     `json:"currency"`
	Opening         *int64     `json:"opening"`
	Restricted      int64      `json:"restricted"`
	OpeningStatus   string     `json:"opening_status"`
	OpeningEvidence string     `json:"opening_evidence"`
	Buffer          int64      `json:"buffer"`
	Requested       *int64     `json:"requested"`
	Contribution    *int64     `json:"contribution"`
	RequiredBy      string     `json:"required_by"`
	Purpose         string     `json:"purpose"`
	Milestone       string     `json:"milestone"`
	DelayImpact     string     `json:"delay_impact"`
	Assumptions     string     `json:"assumptions"`
	NextUpdate      string     `json:"next_update"`
	Complete        bool       `json:"complete"`
	Movements       []Movement `json:"movements"`
	Status          string     `json:"status"`
	Revision        int        `json:"revision"`
	Previous        string     `json:"previous"`
	UpdatedAt       string     `json:"updated_at"`
	UpdatedBy       string     `json:"updated_by"`
	ApprovedBy      string     `json:"approved_by"`
	ApprovedAt      string     `json:"approved_at"`
	Model           string     `json:"model"`
}

type Point struct {
	Date string `json:"date"`
	Cash int64  `json:"cash"`
	Gap  int64  `json:"gap"`
}
type View struct {
	Known     bool     `json:"known"`
	Need      int64    `json:"need"`
	Breach    string   `json:"breach"`
	Negative  string   `json:"negative"`
	CashEnd   int64    `json:"cash_end"`
	Payments  int64    `json:"payments"`
	Growth    int64    `json:"growth"`
	Financing int64    `json:"financing"`
	Proposed  int64    `json:"proposed"`
	Points    []Point  `json:"points"`
	Warnings  []string `json:"warnings"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

var moneyRE = regexp.MustCompile(`^-?[0-9]{1,12}(\.[0-9]{1,2})?$`)

func Money(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !moneyRE.MatchString(s) {
		return 0, errors.New("amount must have at most two decimal places")
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.Split(s, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	cents := int64(0)
	if len(parts) == 2 {
		cents, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
	}
	n := whole*100 + cents
	if negative {
		n = -n
	}
	return n, nil
}
func Decimal(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}
func ValidDate(s string) bool { _, e := time.Parse("2006-01-02", s); return e == nil }
func Validate(f Forecast, approving bool) error {
	if strings.TrimSpace(f.Entity) == "" || strings.TrimSpace(f.EntityRef) == "" || strings.TrimSpace(f.Owner) == "" {
		return errors.New("legal entity, entity reference and owner are required")
	}
	if !ValidDate(f.AsOf) || !ValidDate(f.Horizon) || f.Horizon < f.AsOf {
		return errors.New("valid as-of date and later horizon are required")
	}
	if f.Scenario != "base" && f.Scenario != "downside" && f.Scenario != "upside" {
		return errors.New("select a base, downside or upside scenario")
	}
	if !strings.Contains("|EUR|USD|GBP|CHF|CAD|AUD|SGD|", "|"+f.Currency+"|") {
		return errors.New("unsupported currency; select a two-decimal currency")
	}
	amounts := []int64{f.Restricted, f.Buffer}
	if f.Opening != nil {
		amounts = append(amounts, *f.Opening)
	}
	if f.Requested != nil {
		amounts = append(amounts, *f.Requested)
	}
	if f.Contribution != nil {
		amounts = append(amounts, *f.Contribution)
	}
	for _, n := range amounts {
		if n < 0 || n > 100000000000000 {
			return errors.New("cash and funding amounts must be positive and within supported limits")
		}
	}
	if f.Opening != nil && f.Restricted > *f.Opening {
		return errors.New("restricted cash cannot exceed opening cash")
	}
	if f.OpeningStatus != "estimated" && f.OpeningStatus != "verified" {
		return errors.New("opening cash must be labeled estimated or verified")
	}
	if f.RequiredBy != "" && !ValidDate(f.RequiredBy) || f.NextUpdate != "" && !ValidDate(f.NextUpdate) {
		return errors.New("invalid funding or next-update date")
	}
	if len(f.Movements) > 5000 {
		return errors.New("maximum 5,000 cash movements per forecast")
	}
	seen := map[string]bool{}
	refs := map[string]bool{}
	for i, m := range f.Movements {
		if m.ID == "" || seen[m.ID] {
			return fmt.Errorf("cash movement %d has a missing or duplicate ID", i+1)
		}
		seen[m.ID] = true
		if !ValidDate(m.Date) || m.Date < f.AsOf || m.Date > f.Horizon {
			return fmt.Errorf("cash movement %d needs a cash date inside the forecast horizon", i+1)
		}
		if m.Amount <= 0 || m.Amount > 100000000000000 {
			return fmt.Errorf("cash movement %d needs a positive amount", i+1)
		}
		if m.Kind != "receipt" && m.Kind != "payment" && m.Kind != "financing" {
			return errors.New("cash movement kind must be receipt, payment or financing")
		}
		if m.Kind == "financing" {
			if m.Certainty != "committed" && m.Certainty != "conditional" && m.Certainty != "proposed" {
				return errors.New("financing needs committed, conditional or proposed status")
			}
		} else if m.Certainty != "contracted" && m.Certainty != "estimated" {
			return errors.New("receipts/payments need contracted or estimated status")
		}
		if m.SourceRef != "" {
			key := m.Source + ":" + m.SourceRef
			if refs[key] {
				return errors.New("duplicate source record; merge it instead of importing it twice")
			}
			refs[key] = true
		}
		if m.Original != nil && (m.Amount != m.Original.Amount || m.Date != m.Original.Date) && strings.TrimSpace(m.Adjustment) == "" {
			return errors.New("adjusted imported amounts/dates require a reason")
		}
		if approving && m.Kind == "financing" && m.Certainty == "committed" && strings.TrimSpace(m.Evidence) == "" {
			return errors.New("committed financing requires evidence before approval")
		}
	}
	if approving && (f.Opening == nil || !f.Complete || strings.TrimSpace(f.OpeningEvidence) == "" || strings.TrimSpace(f.Assumptions) == "") {
		return errors.New("approval requires opening cash, opening evidence, assumptions and a confirmed complete schedule")
	}
	return nil
}

func Calculate(f Forecast, end string) View {
	v := View{Points: []Point{}, Warnings: []string{}}
	if !f.Complete {
		v.Warnings = append(v.Warnings, "Schedule not confirmed complete")
	}
	if f.Opening == nil {
		v.Warnings = append(v.Warnings, "Opening cash unknown; funding need cannot be calculated")
		return v
	}
	v.Known = true
	if f.OpeningStatus != "verified" {
		v.Warnings = append(v.Warnings, "Opening cash is a management estimate")
	}
	if end == "" || end > f.Horizon {
		end = f.Horizon
	}
	if end < f.AsOf {
		end = f.AsOf
	}
	cash := *f.Opening - f.Restricted
	net := map[string]int64{f.AsOf: 0, end: 0}
	for _, m := range f.Movements {
		if m.Date > end {
			continue
		}
		switch m.Kind {
		case "payment":
			net[m.Date] -= m.Amount
			v.Payments += m.Amount
			if m.Growth {
				v.Growth += m.Amount
			}
		case "receipt":
			net[m.Date] += m.Amount
		case "financing":
			if m.Certainty == "committed" && strings.TrimSpace(m.Evidence) != "" {
				net[m.Date] += m.Amount
				v.Financing += m.Amount
			} else {
				v.Proposed += m.Amount
			}
		}
	}
	dates := make([]string, 0, len(net))
	for d := range net {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	// Include liquidity at the start of the as-of day before new receipts.
	gap := max(int64(0), f.Buffer-cash)
	v.Need = gap
	if gap > 0 {
		v.Breach = f.AsOf
	}
	if cash < 0 {
		v.Negative = f.AsOf
	}
	v.Points = append(v.Points, Point{Date: f.AsOf, Cash: cash, Gap: gap})
	for _, d := range dates {
		cash += net[d]
		gap = max(int64(0), f.Buffer-cash)
		v.Need = max(v.Need, gap)
		if gap > 0 && v.Breach == "" {
			v.Breach = d
		}
		if cash < 0 && v.Negative == "" {
			v.Negative = d
		}
		v.Points = append(v.Points, Point{Date: d, Cash: cash, Gap: gap})
	}
	v.CashEnd = cash
	if v.Proposed > 0 {
		v.Warnings = append(v.Warnings, "Conditional, proposed or unevidenced financing excluded from available cash")
	}
	if len(f.Movements) == 0 {
		v.Warnings = append(v.Warnings, "No cash movements entered")
	}
	return v
}
func FloatMinor(n float64) (int64, error) {
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e12 {
		return 0, errors.New("invalid source amount")
	}
	return int64(math.Round(n * 100)), nil
}
