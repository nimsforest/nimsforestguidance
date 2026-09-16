package guidance

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB  *sql.DB
	Org string
}
type Command struct {
	Ref        string   `json:"ref"`
	Org        string   `json:"org"`
	Action     string   `json:"action"`
	Forecast   Forecast `json:"forecast"`
	Expected   int      `json:"expected_revision"`
	Actor      string   `json:"actor"`
	CanApprove bool     `json:"can_approve"`
	Signature  string   `json:"signature,omitempty"`
}
type Result struct {
	Ref      string `json:"ref"`
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}
type Audit struct {
	Ref      string `json:"ref"`
	Action   string `json:"action"`
	Actor    string `json:"actor"`
	At       string `json:"at"`
	Revision int    `json:"revision"`
}

func Open(path, org string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS forecasts(id TEXT PRIMARY KEY, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS revisions(id TEXT, revision INTEGER, data TEXT NOT NULL, PRIMARY KEY(id,revision));
 CREATE TABLE IF NOT EXISTS commands(ref TEXT PRIMARY KEY, result TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit(ref TEXT PRIMARY KEY,id TEXT NOT NULL,revision INTEGER NOT NULL,action TEXT NOT NULL,actor TEXT NOT NULL,at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS outbox(ref TEXT PRIMARY KEY,data TEXT NOT NULL,humus_sent INTEGER NOT NULL DEFAULT 0,river_sent INTEGER NOT NULL DEFAULT 0);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(`INSERT OR IGNORE INTO metadata VALUES('org',?)`, org)
	if e != nil {
		db.Close()
		return nil, e
	}
	var bound string
	e = db.QueryRow(`SELECT value FROM metadata WHERE key='org'`).Scan(&bound)
	if e != nil || bound != org {
		db.Close()
		return nil, errors.New("stored organization does not match ORG_SLUG")
	}
	return &Store{DB: db, Org: org}, nil
}
func (s *Store) List() ([]Forecast, error) {
	rows, e := s.DB.Query(`SELECT data FROM forecasts ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Forecast{}
	for rows.Next() {
		var b string
		var f Forecast
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &f); e != nil {
			return nil, e
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Store) Get(id string) (Forecast, error) {
	var b string
	e := s.DB.QueryRow(`SELECT data FROM forecasts WHERE id=?`, id).Scan(&b)
	var f Forecast
	if e == nil {
		e = json.Unmarshal([]byte(b), &f)
	}
	return f, e
}
func (s *Store) Result(ref string) (Result, error) {
	var b string
	e := s.DB.QueryRow(`SELECT result FROM commands WHERE ref=?`, ref).Scan(&b)
	var v Result
	if e == nil {
		e = json.Unmarshal([]byte(b), &v)
	}
	return v, e
}
func (s *Store) History(id string) ([]Audit, error) {
	rows, e := s.DB.Query(`SELECT ref,action,actor,at,revision FROM audit WHERE id=? ORDER BY revision`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	v := []Audit{}
	for rows.Next() {
		var a Audit
		if e = rows.Scan(&a.Ref, &a.Action, &a.Actor, &a.At, &a.Revision); e != nil {
			return nil, e
		}
		v = append(v, a)
	}
	return v, rows.Err()
}

func (s *Store) Apply(c Command) (Result, error) {
	if c.Ref == "" || len(c.Ref) > 100 || c.Org != s.Org || c.Actor == "" {
		return Result{}, errors.New("command requires a ref, actor and matching organization")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return Result{}, e
	}
	defer tx.Rollback()
	var b string
	e = tx.QueryRow(`SELECT result FROM commands WHERE ref=?`, c.Ref).Scan(&b)
	if e == nil {
		var r Result
		e = json.Unmarshal([]byte(b), &r)
		return r, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Result{}, e
	}
	r := Result{Ref: c.Ref, ID: c.Forecast.ID, Status: "failed"}
	f := c.Forecast
	var old Forecast
	exists := false
	if f.ID != "" {
		e = tx.QueryRow(`SELECT data FROM forecasts WHERE id=?`, f.ID).Scan(&b)
		if e == nil {
			e = json.Unmarshal([]byte(b), &old)
			if e != nil {
				return Result{}, e
			}
			exists = true
		} else if !errors.Is(e, sql.ErrNoRows) {
			return Result{}, e
		}
	}
	business := func() error {
		if c.Action == "revise" {
			if !exists || old.Status != "approved" {
				return errors.New("only an approved snapshot can be revised")
			}
			if c.Expected != old.Revision {
				return errors.New("forecast changed; refresh before revising")
			}
			f = old
			f.Previous = old.ID
			f.ID = NewID()
			f.Revision = 0
			f.Status = "draft"
			f.ApprovedAt = ""
			f.ApprovedBy = ""
			exists = false
		} else if c.Action == "save" {
			if exists {
				if old.Status != "draft" {
					return errors.New("only drafts are editable; return to draft or revise an approved version")
				}
				if c.Expected != old.Revision {
					return errors.New("forecast changed; refresh before saving")
				}
				f.Previous = old.Previous
				f.Status = old.Status
				f.Revision = old.Revision
			} else {
				if f.ID != "" {
					return errors.New("forecast does not exist")
				}
				f.ID = NewID()
				f.Status = "draft"
				f.Revision = 0
				f.Previous = ""
			}
			f.ApprovedAt = ""
			f.ApprovedBy = ""
		} else {
			if !exists || c.Expected != old.Revision {
				return errors.New("forecast missing or changed; refresh and try again")
			}
			f = old
			switch c.Action {
			case "submit":
				if f.Status != "draft" {
					return errors.New("submit requires a draft")
				}
				f.Status = "submitted"
			case "review":
				if !c.CanApprove || f.Status != "submitted" {
					return errors.New("review requires an organization administrator and a submitted forecast")
				}
				f.Status = "reviewed"
			case "approve":
				if !c.CanApprove || f.Status != "reviewed" {
					return errors.New("approval requires an organization administrator and a reviewed forecast")
				}
				f.Status = "approved"
				f.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
				f.ApprovedBy = c.Actor
			case "return":
				if !c.CanApprove || f.Status == "approved" {
					return errors.New("only an administrator can return an unapproved forecast to draft")
				}
				f.Status = "draft"
			default:
				return errors.New("unsupported guidance action")
			}
		}
		f.Org = s.Org
		f.Model = ModelVersion
		if e := Validate(f, f.Status == "approved"); e != nil {
			return e
		}
		return nil
	}
	if err := business(); err != nil {
		r.Error = err.Error()
	} else {
		f.Revision++
		f.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		f.UpdatedBy = c.Actor
		data, e := json.Marshal(f)
		if e != nil {
			return r, e
		}
		if _, e = tx.Exec(`INSERT INTO forecasts(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, f.ID, string(data)); e != nil {
			return r, e
		}
		if _, e = tx.Exec(`INSERT INTO revisions VALUES(?,?,?)`, f.ID, f.Revision, string(data)); e != nil {
			return r, e
		}
		if _, e = tx.Exec(`INSERT INTO audit VALUES(?,?,?,?,?,?)`, c.Ref, f.ID, f.Revision, c.Action, c.Actor, f.UpdatedAt); e != nil {
			return r, e
		}
		r = Result{Ref: c.Ref, ID: f.ID, Revision: f.Revision, Status: "applied"}
	}
	data, _ := json.Marshal(r)
	if _, e = tx.Exec(`INSERT INTO commands VALUES(?,?)`, c.Ref, string(data)); e != nil {
		return r, e
	}
	event, _ := json.Marshal(map[string]any{"result": r, "org": s.Org, "action": c.Action, "actor": c.Actor, "at": time.Now().UTC().Format(time.RFC3339)})
	if _, e = tx.Exec(`INSERT INTO outbox(ref,data,river_sent) VALUES(?,?,?)`, c.Ref, string(event), r.Status != "applied"); e != nil {
		return r, e
	}
	return r, tx.Commit()
}

func (s *Store) Backup(path string) error {
	if _, e := os.Stat(path); e == nil {
		return errors.New("backup destination already exists")
	}
	_, e := s.DB.Exec(`VACUUM INTO ?`, path)
	if e != nil {
		return fmt.Errorf("backup: %w", e)
	}
	return os.Chmod(path, 0600)
}
