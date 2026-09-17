package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/nimsforest/nimsforestguidance/internal/guidance"
)

var workspaceSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var portfolioClient = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type portfolioEntry struct {
	Forecast guidance.Forecast `json:"forecast"`
	View     guidance.View     `json:"view"`
}

// Aggregate only authorized, deployed workspaces. Each destination rechecks the
// user's live membership. Never proxy source inputs or accept a browser URL.
func (s *Server) portfolio(w http.ResponseWriter, r *http.Request) {
	entries := []portfolioEntry{}
	unavailable := []string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for org := range s.workspaces(s.user(r)) {
		if !workspaceSlug.MatchString(org) {
			continue
		}
		if org == s.Store.Org {
			fs, err := s.Store.List()
			if err != nil {
				mu.Lock()
				unavailable = append(unavailable, org)
				mu.Unlock()
				continue
			}
			mu.Lock()
			for _, f := range fs {
				entries = append(entries, portfolioEntry{f, guidance.Calculate(f, portfolioEnd(f, r.URL.Query().Get("horizon")))})
			}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(org string) {
			defer wg.Done()
			items, err := s.workspaceForecasts(r, org)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unavailable = append(unavailable, org)
			} else {
				entries = append(entries, items...)
			}
		}(org)
	}
	wg.Wait()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Forecast.Org+entries[i].Forecast.ID < entries[j].Forecast.Org+entries[j].Forecast.ID
	})
	sort.Strings(unavailable)
	respond(w, 200, map[string]any{"forecasts": entries, "unavailable": unavailable})
}

func portfolioEnd(f guidance.Forecast, horizon string) string {
	start, _ := time.Parse("2006-01-02", f.AsOf)
	switch horizon {
	case "13w":
		return start.AddDate(0, 0, 91).Format("2006-01-02")
	case "12m":
		return start.AddDate(0, 12, 0).Format("2006-01-02")
	case "3y":
		return start.AddDate(3, 0, 0).Format("2006-01-02")
	}
	return ""
}

func (s *Server) workspaceForecasts(r *http.Request, org string) ([]portfolioEntry, error) {
	target := "https://guidance." + org + ".mynimsforest.com/api/forecasts?horizon=" + url.QueryEscape(r.URL.Query().Get("horizon"))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if cookie, e := r.Cookie("guidance_session"); e == nil {
		req.AddCookie(cookie)
	}
	resp, err := portfolioClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, authError(resp.StatusCode)
	}
	var entries []portfolioEntry
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&entries); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Forecast.Org != org {
			return nil, authError(http.StatusForbidden)
		}
	}
	return entries, nil
}
