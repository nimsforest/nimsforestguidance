package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nimsforest/nimsforestguidance/internal/guidance"
)

type sourceCredentialError struct{ Status int }

func (e sourceCredentialError) Error() string {
	return "source credential is not configured or accessible"
}

var sourceClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func setting(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
func (s *Server) sourceBase(source string) (string, string, error) {
	switch source {
	case "ledger":
		return setting("LEDGER_URL", "http://127.0.0.1:8087"), "nimsforestledger", nil
	case "odoo":
		return setting("ODOO_URL", "http://127.0.0.1:8095"), "nimsforestodoo", nil
	}
	return "", "", errors.New("unsupported source tool")
}
func (s *Server) sourceGET(ctx context.Context, source, path string, dst any) error {
	base, service, e := s.sourceBase(source)
	if e != nil {
		return e
	}
	proxy := setting("MYCELIUM_PROXY_URL", "http://127.0.0.1:8190")
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(proxy, "/")+"/api/identity/credentials/"+service, nil)
	resp, e := sourceClient.Do(req)
	if e != nil {
		return errors.New("source credential service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return sourceCredentialError{Status: resp.StatusCode}
	}
	var identity struct {
		Land    string            `json:"land"`
		Service string            `json:"service"`
		Secrets map[string]string `json:"secrets"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&identity) != nil {
		return errors.New("source credential response unavailable")
	}
	if identity.Land != "" && identity.Land != s.Store.Org || identity.Service != "" && identity.Service != service {
		return errors.New("source credential organization mismatch")
	}
	if identity.Secrets["api_token"] == "" {
		return sourceCredentialError{Status: 404}
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+path, nil)
	req.Header.Set("Authorization", "Bearer "+identity.Secrets["api_token"])
	resp2, e := sourceClient.Do(req)
	if e != nil {
		return errors.New("source tool unavailable")
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		return fmt.Errorf("%s read failed (HTTP %d)", source, resp2.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(resp2.Body, 8<<20)).Decode(dst) != nil {
		return errors.New("source returned unsupported data")
	}
	return nil
}
func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, name := range []string{"ledger", "odoo"} {
		_, service, _ := s.sourceBase(name)
		item := map[string]any{"key": name, "service": service, "name": map[string]string{"ledger": "Ledger", "odoo": "Odoo"}[name], "description": map[string]string{"ledger": "Preview monthly category forecasts. Confirm currency and expected cash dates.", "odoo": "Preview outstanding posted invoices and bills. Supply expected cash dates."}[name], "requires_admin": true, "status": "Available to configure", "manage_url": "https://admin." + s.Store.Org + ".mynimsforest.com/"}
		if s.user(r).IsAdmin {
			var resources any
			path := "/api/v1/ledgers"
			if name == "odoo" {
				path = "/api/v1/companies"
			}
			if e := s.sourceGET(r.Context(), name, path, &resources); e != nil {
				item["status"] = "Not connected"
				item["detail"] = e.Error()
			} else {
				item["status"] = "Connected"
				item["resources"] = resources
				item["checked_at"] = time.Now().UTC().Format(time.RFC3339)
			}
		} else {
			item["status"] = "Administrator setup required"
		}
		list = append(list, item)
	}
	respond(w, 200, list)
}
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	if !s.user(r).IsAdmin {
		fail(w, 403, "source reads require an organization administrator")
		return
	}
	source := r.PathValue("source")
	resource := r.URL.Query().Get("resource")
	currency := r.URL.Query().Get("currency")
	if resource == "" {
		fail(w, 400, "select a ledger or company")
		return
	}
	imported := time.Now().UTC().Format(time.RFC3339)
	moves := []guidance.Movement{}
	warnings := []string{}
	if source == "ledger" {
		var rows []struct {
			Category string `json:"category"`
			Month    string `json:"month"`
			Amount   int64  `json:"amount"`
		}
		if e := s.sourceGET(r.Context(), source, "/api/v1/ledgers/"+url.PathEscape(resource)+"/forecasts", &rows); e != nil {
			fail(w, 502, e.Error())
			return
		}
		for _, row := range rows {
			t, e := time.Parse("2006-01", row.Month)
			if e != nil || row.Amount == 0 {
				continue
			}
			amount := row.Amount
			kind := "receipt"
			if amount < 0 {
				amount = -amount
				kind = "payment"
			}
			date := t.AddDate(0, 1, -1).Format("2006-01-02")
			m := guidance.Movement{ID: guidance.NewID(), Date: date, Kind: kind, Amount: amount, Category: row.Category, Description: "Ledger monthly forecast: " + row.Category, Certainty: "estimated", Source: "ledger", SourceRef: resource + ":" + row.Month + ":" + row.Category, ImportedAt: imported, SourceDate: row.Month, Original: &guidance.SourceValue{Amount: amount, Date: date, Description: row.Category}}
			m.OriginSignature = s.originMAC(m)
			moves = append(moves, m)
		}
		warnings = append(warnings, "Ledger forecasts do not declare currency. Confirm the selected currency and replace month-end placeholder dates with expected cash dates. Opening cash must be entered separately.")
	} else if source == "odoo" {
		id, e := strconv.Atoi(resource)
		if e != nil || id <= 0 {
			fail(w, 400, "invalid company reference")
			return
		}
		var rows []struct {
			ID       int     `json:"id"`
			Name     string  `json:"name"`
			Type     string  `json:"move_type"`
			Residual float64 `json:"amount_residual"`
			Currency struct {
				Name string `json:"name"`
			} `json:"currency_id"`
			WriteDate string `json:"write_date"`
		}
		for offset := 0; offset < 5000; offset += 100 {
			rows = nil
			if e := s.sourceGET(r.Context(), source, fmt.Sprintf("/api/v1/invoices?company_id=%d&state=posted&limit=100&offset=%d", id, offset), &rows); e != nil {
				fail(w, 502, e.Error())
				return
			}
			for _, row := range rows {
				if row.Residual <= 0 {
					continue
				}
				if row.Currency.Name != currency {
					warnings = append(warnings, fmt.Sprintf("Skipped %s: currency %s differs from %s", row.Name, row.Currency.Name, currency))
					continue
				}
				kind := "receipt"
				if row.Type == "in_invoice" || row.Type == "out_refund" {
					kind = "payment"
				} else if row.Type != "out_invoice" && row.Type != "in_refund" {
					continue
				}
				amount, e := guidance.FloatMinor(row.Residual)
				if e != nil {
					continue
				}
				m := guidance.Movement{ID: guidance.NewID(), Kind: kind, Amount: amount, Category: "Invoice / bill", Description: row.Name, Certainty: "contracted", Source: "odoo", SourceRef: resource + ":" + strconv.Itoa(row.ID), ImportedAt: imported, SourceDate: row.WriteDate, Original: &guidance.SourceValue{Amount: amount, Description: row.Name}}
				m.OriginSignature = s.originMAC(m)
				moves = append(moves, m)
			}
			if len(rows) < 100 {
				break
			}
			if offset == 4900 {
				warnings = append(warnings, "Preview limited to 5,000 invoices; confirm completeness separately")
			}
		}
		warnings = append(warnings, "This connector does not expose due dates. Enter expected cash dates and timing assumptions before saving. Residual amounts are claims, not guaranteed cash. Reconcile against settled ledger items.")
	} else {
		fail(w, 400, "unsupported source")
		return
	}
	respond(w, 200, map[string]any{"movements": moves, "warnings": warnings, "imported_at": imported, "currency": currency})
}

var csvHeaders = []string{"date", "kind", "amount", "category", "description", "certainty", "growth", "evidence", "adjustment", "transfer_ref"}

func (s *Server) csvTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="guidance-cash-template.csv"`)
	writer := csv.NewWriter(w)
	writer.Write(csvHeaders)
	writer.Flush()
}
func (s *Server) csvPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if e := r.ParseMultipartForm(4 << 20); e != nil {
		fail(w, 400, "CSV file exceeds 4 MB or could not be read")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, e := r.FormFile("file")
	if e != nil {
		fail(w, 400, "select a CSV file")
		return
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	head, e := reader.Read()
	if e != nil {
		fail(w, 400, "CSV header required")
		return
	}
	mapping := map[string]int{}
	for i, h := range head {
		mapping[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	for _, h := range []string{"date", "kind", "amount"} {
		if _, ok := mapping[h]; !ok {
			fail(w, 400, "CSV needs date, kind and amount columns; download the template")
			return
		}
	}
	out := []guidance.Movement{}
	for line := 2; line <= 5002; line++ {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			fail(w, 400, fmt.Sprintf("invalid CSV at line %d", line))
			return
		}
		if line == 5002 {
			fail(w, 400, "maximum 5,000 rows per import")
			return
		}
		get := func(k string) string {
			i, ok := mapping[k]
			if ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		amount, e := guidance.Money(get("amount"))
		if e != nil || amount <= 0 || !guidance.ValidDate(get("date")) {
			fail(w, 400, fmt.Sprintf("line %d needs a positive amount and YYYY-MM-DD cash date", line))
			return
		}
		certainty := get("certainty")
		if certainty == "" {
			certainty = "estimated"
			if get("kind") == "financing" {
				certainty = "proposed"
			}
		}
		out = append(out, guidance.Movement{ID: guidance.NewID(), Date: get("date"), Kind: get("kind"), Amount: amount, Category: get("category"), Description: get("description"), Certainty: certainty, Growth: get("growth") == "true", Evidence: get("evidence"), Adjustment: get("adjustment"), TransferRef: get("transfer_ref"), Source: "csv", ImportedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	respond(w, 200, map[string]any{"movements": out, "warnings": []string{"CSV import adds rows to your draft. Check for overlaps before saving."}})
}
func (s *Server) csvExport(w http.ResponseWriter, r *http.Request) {
	f, e := s.Store.Get(r.PathValue("id"))
	if e != nil {
		fail(w, 404, "forecast not found")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="guidance-cash-schedule.csv"`)
	writer := csv.NewWriter(w)
	writer.Write(append(append([]string{}, csvHeaders...), "source", "source_ref", "imported_at", "source_date", "original_amount", "original_date"))
	for _, m := range f.Movements {
		a, d := "", ""
		if m.Original != nil {
			a = guidance.Decimal(m.Original.Amount)
			d = m.Original.Date
		}
		writer.Write([]string{m.Date, m.Kind, guidance.Decimal(m.Amount), m.Category, m.Description, m.Certainty, strconv.FormatBool(m.Growth), m.Evidence, m.Adjustment, m.TransferRef, m.Source, m.SourceRef, m.ImportedAt, m.SourceDate, a, d})
	}
	writer.Flush()
}
