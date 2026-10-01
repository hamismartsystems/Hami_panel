// Package store owns the panel's data: inbounds, clients, traffic samples,
// remote nodes and the audit log.
//
// SQLite by default (single file, zero setup); PostgreSQL is planned for
// larger deployments. Schema changes go through migrations below — never
// through an ad-hoc ALTER hidden in application code.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// Store is the panel's database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path. Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; keep a single connection for writes
	// and let reads use the pool.
	db.SetMaxOpenConns(8)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

/* ── migrations ──────────────────────────────────────────────────────── */

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS inbounds (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		remark       TEXT NOT NULL DEFAULT '',
		protocol     TEXT NOT NULL,
		port         INTEGER NOT NULL,
		host         TEXT NOT NULL,
		transport    TEXT NOT NULL DEFAULT 'tcp',
		security     TEXT NOT NULL DEFAULT 'none',
		sni          TEXT NOT NULL DEFAULT '',
		public_key   TEXT NOT NULL DEFAULT '',
		short_id     TEXT NOT NULL DEFAULT '',
		spider_x     TEXT NOT NULL DEFAULT '',
		fingerprint  TEXT NOT NULL DEFAULT 'chrome',
		path         TEXT NOT NULL DEFAULT '',
		xhttp_mode   TEXT NOT NULL DEFAULT 'auto',
		header_type  TEXT NOT NULL DEFAULT 'none',
		flow         TEXT NOT NULL DEFAULT '',
		enable       INTEGER NOT NULL DEFAULT 1,
		created_at   TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS clients (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		inbound_id    INTEGER NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
		uuid          TEXT NOT NULL DEFAULT '',
		password      TEXT NOT NULL DEFAULT '',
		method        TEXT NOT NULL DEFAULT '',
		ss_password   TEXT NOT NULL DEFAULT '',
		email         TEXT NOT NULL DEFAULT '',
		enable        INTEGER NOT NULL DEFAULT 1,
		total_bytes   INTEGER NOT NULL DEFAULT 0,
		up_bytes      INTEGER NOT NULL DEFAULT 0,
		down_bytes    INTEGER NOT NULL DEFAULT 0,
		expire_at     TEXT,
		ip_limit      INTEGER NOT NULL DEFAULT 0,
		speed_limit   INTEGER NOT NULL DEFAULT 0,
		created_at    TEXT NOT NULL
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_clients_inbound_uuid
		ON clients(inbound_id, uuid);`,
	`CREATE TABLE IF NOT EXISTS traffic (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		client_id  INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
		ts         TEXT NOT NULL,
		up_bytes   INTEGER NOT NULL DEFAULT 0,
		down_bytes INTEGER NOT NULL DEFAULT 0
	);`,
	`CREATE INDEX IF NOT EXISTS idx_traffic_client_ts ON traffic(client_id, ts);`,
	`CREATE TABLE IF NOT EXISTS nodes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		address    TEXT NOT NULL,
		api_key    TEXT NOT NULL,
		last_seen  TEXT,
		status     TEXT NOT NULL DEFAULT 'unknown'
	);`,
	`CREATE TABLE IF NOT EXISTS events (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts      TEXT NOT NULL,
		level   TEXT NOT NULL,
		actor   TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL,
		meta    TEXT NOT NULL DEFAULT ''
	);`,
	`CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);`,
	`CREATE TABLE IF NOT EXISTS inbound_secrets (
		inbound_id  INTEGER PRIMARY KEY REFERENCES inbounds(id) ON DELETE CASCADE,
		private_key TEXT NOT NULL DEFAULT '',
		dest        TEXT NOT NULL DEFAULT '',
		listen      TEXT NOT NULL DEFAULT '0.0.0.0',
		cert_file   TEXT NOT NULL DEFAULT '',
		key_file    TEXT NOT NULL DEFAULT ''
	);`,
	// Stage 2: one unguessable subscription token per client. Empty string
	// means "no subscription was issued yet".
	`ALTER TABLE clients ADD COLUMN sub_token TEXT NOT NULL DEFAULT '';`,
	// Tokens must be unique — but many rows may share the empty default,
	// so the index covers only real tokens.
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_clients_sub_token
		ON clients(sub_token) WHERE sub_token <> '';`,
	// Stage 3: every inbound lives on exactly one node (0 = this server).
	`ALTER TABLE inbounds ADD COLUMN node_id INTEGER NOT NULL DEFAULT 0;`,
	// Stage 4: sing-box fields for Hysteria2/TUIC/AnyTLS
	`ALTER TABLE inbounds ADD COLUMN obfs_type TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE inbounds ADD COLUMN obfs_password TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE inbounds ADD COLUMN alpn TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE inbounds ADD COLUMN congestion_control TEXT NOT NULL DEFAULT '';`,
	// Stage 4b: private/dedicated inbounds (admin-only)
	`ALTER TABLE inbounds ADD COLUMN is_private INTEGER NOT NULL DEFAULT 0;`,
	// Stage 4.5: panel operators and browser sessions for HP-UI.
	`CREATE TABLE IF NOT EXISTS admins (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT NOT NULL UNIQUE,
		hash       TEXT NOT NULL,
		created_at TEXT NOT NULL,
		last_login TEXT NOT NULL DEFAULT ''
	);`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);`,
	// Per-client flow. Storing one flow per inbound cannot represent a
	// real 3x-ui inbound where one customer runs xtls-rprx-vision and the
	// rest run none, and importing one used to silently change somebody's
	// connection. Appended last: the list is index-based.
	`ALTER TABLE clients ADD COLUMN flow TEXT NOT NULL DEFAULT '';`,
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return err
		}
	}
	return nil
}

/* ── models ──────────────────────────────────────────────────────────── */

// Inbound is one listening endpoint of the core.
type Inbound struct {
	ID       int64
	NodeID   int64
	Remark   string
	Protocol string
	Port     int
	Host     string

	Transport string
	Security  string

	SNI         string
	PublicKey   string
	ShortID     string
	SpiderX     string
	Fingerprint string

	Path       string
	XHTTPMode  string
	HeaderType string
	Flow       string

	// sing-box: Hysteria2/TUIC/AnyTLS
	ObfsType          string
	ObfsPassword      string
	Alpn              string
	CongestionControl string

	// private/dedicated (admin-only)
	IsPrivate bool

	Enable    bool
	CreatedAt time.Time
}

// Client is one subscriber attached to exactly one inbound.
type Client struct {
	ID        int64
	InboundID int64

	UUID       string
	Password   string
	Method     string
	SSPassword string
	Email      string

	Enable     bool
	TotalBytes int64
	UpBytes    int64
	DownBytes  int64
	ExpireAt   *time.Time
	IPLimit    int
	SpeedLimit int64
	SubToken   string
	// Flow overrides the inbound's flow for this one client. Empty means
	// "whatever the inbound says".
	Flow string

	CreatedAt time.Time
}

// Event is one line of the audit log.
type Event struct {
	ID      int64
	TS      time.Time
	Level   string
	Actor   string
	Message string
	Meta    string
}

/* ── inbounds ────────────────────────────────────────────────────────── */

func (s *Store) CreateInbound(in *Inbound) error {
	if in.Protocol == "" || in.Port == 0 || in.Host == "" {
		return errors.New("protocol, port and host are required")
	}
	if in.Transport == "" {
		in.Transport = "tcp"
	}
	if in.Fingerprint == "" {
		in.Fingerprint = "chrome"
	}
	in.CreatedAt = time.Now().UTC()
	in.Enable = true
	res, err := s.db.Exec(
		`INSERT INTO inbounds (node_id, remark, protocol, port, host, transport, security,
		 sni, public_key, short_id, spider_x, fingerprint, path, xhttp_mode,
		 header_type, flow, obfs_type, obfs_password, alpn, congestion_control, is_private, enable, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.NodeID, in.Remark, in.Protocol, in.Port, in.Host, in.Transport, orNone(in.Security),
		in.SNI, in.PublicKey, in.ShortID, in.SpiderX, in.Fingerprint,
		in.Path, orAuto(in.XHTTPMode), orNone(in.HeaderType), in.Flow,
		in.ObfsType, in.ObfsPassword, in.Alpn, in.CongestionControl,
		boolInt(in.IsPrivate), boolInt(in.Enable), in.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return err
	}
	in.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) GetInbound(id int64) (*Inbound, error) {
	row := s.db.QueryRow(`SELECT id, node_id, remark, protocol, port, host, transport, security,
		sni, public_key, short_id, spider_x, fingerprint, path, xhttp_mode,
		header_type, flow, obfs_type, obfs_password, alpn, congestion_control, is_private, enable, created_at FROM inbounds WHERE id = ?`, id)
	return scanInbound(row)
}

func (s *Store) ListInbounds() ([]Inbound, error) {
	rows, err := s.db.Query(`SELECT id, node_id, remark, protocol, port, host, transport, security,
		sni, public_key, short_id, spider_x, fingerprint, path, xhttp_mode,
		header_type, flow, obfs_type, obfs_password, alpn, congestion_control, is_private, enable, created_at FROM inbounds ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Inbound{}
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInbound(id int64) error {
	_, err := s.db.Exec(`DELETE FROM inbounds WHERE id = ?`, id)
	return err
}

// SetInboundEnabled turns an inbound on or off. A disabled inbound is left
// out of the generated Xray config.
func (s *Store) SetInboundEnabled(id int64, enable bool) error {
	_, err := s.db.Exec(`UPDATE inbounds SET enable = ? WHERE id = ?`, boolInt(enable), id)
	return err
}

func (s *Store) UpdateInboundReality(id int64, publicKey, shortID, sni, fingerprint, spiderX string) error {
	_, err := s.db.Exec(`UPDATE inbounds SET public_key = ?, short_id = ?, sni = ?, fingerprint = ?, spider_x = ? WHERE id = ?`,
		publicKey, shortID, sni, fingerprint, spiderX, id)
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanInbound(row scanner) (*Inbound, error) {
	var (
		in        Inbound
		isPrivate int
		enable    int
		ts        string
	)
	err := row.Scan(&in.ID, &in.NodeID, &in.Remark, &in.Protocol, &in.Port, &in.Host,
		&in.Transport, &in.Security, &in.SNI, &in.PublicKey, &in.ShortID,
		&in.SpiderX, &in.Fingerprint, &in.Path, &in.XHTTPMode, &in.HeaderType,
		&in.Flow, &in.ObfsType, &in.ObfsPassword, &in.Alpn, &in.CongestionControl, &isPrivate, &enable, &ts)
	if err != nil {
		return nil, err
	}
	in.IsPrivate = isPrivate != 0
	in.Enable = enable != 0
	in.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return &in, nil
}

/* ── clients ─────────────────────────────────────────────────────────── */

func (s *Store) CreateClient(c *Client) error {
	if c.InboundID == 0 {
		return errors.New("client must be attached to an inbound")
	}
	if c.UUID == "" && c.Password == "" && c.SSPassword == "" {
		return errors.New("client needs a uuid or a password")
	}
	// A brand new client starts enabled; callers that need otherwise say
	// so afterwards with SetClientEnabled, which keeps this the one place
	// that decides what "new" means.
	c.Enable = true
	// CreatedAt is the caller's when it is set, so an import can keep the
	// date the customer actually signed up.
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	var expire interface{}
	if c.ExpireAt != nil {
		expire = c.ExpireAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.Exec(`INSERT INTO clients (inbound_id, uuid, password, method,
		ss_password, email, enable, total_bytes, up_bytes, down_bytes, expire_at,
		ip_limit, speed_limit, sub_token, created_at, flow)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.InboundID, c.UUID, c.Password, c.Method, c.SSPassword, c.Email,
		boolInt(c.Enable), c.TotalBytes, c.UpBytes, c.DownBytes, expire,
		c.IPLimit, c.SpeedLimit, c.SubToken, c.CreatedAt.Format(time.RFC3339), c.Flow)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) GetClient(id int64) (*Client, error) {
	return scanClient(s.db.QueryRow(`SELECT id, inbound_id, uuid, password, method,
		ss_password, email, enable, total_bytes, up_bytes, down_bytes, expire_at,
		ip_limit, speed_limit, sub_token, created_at, flow FROM clients WHERE id = ?`, id))
}

func (s *Store) ListClientsOf(inboundID int64) ([]Client, error) {
	rows, err := s.db.Query(`SELECT id, inbound_id, uuid, password, method,
		ss_password, email, enable, total_bytes, up_bytes, down_bytes, expire_at,
		ip_limit, speed_limit, sub_token, created_at, flow FROM clients WHERE inbound_id = ? ORDER BY id`,
		inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Client{}
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) SetClientEnabled(id int64, enable bool) error {
	_, err := s.db.Exec(`UPDATE clients SET enable = ? WHERE id = ?`, boolInt(enable), id)
	return err
}

func (s *Store) DeleteClient(id int64) error {
	_, err := s.db.Exec(`DELETE FROM clients WHERE id = ?`, id)
	return err
}

// AddTraffic records usage for a client and updates its counters atomically.
func (s *Store) AddTraffic(clientID int64, up, down int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE clients SET up_bytes = up_bytes + ?,
		down_bytes = down_bytes + ? WHERE id = ?`, up, down, clientID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO traffic (client_id, ts, up_bytes, down_bytes)
		VALUES (?,?,?,?)`, clientID, time.Now().UTC().Format(time.RFC3339), up, down); err != nil {
		return err
	}
	return tx.Commit()
}

func scanClient(row scanner) (*Client, error) {
	var (
		c      Client
		enable int
		expire sql.NullString
		ts     string
	)
	err := row.Scan(&c.ID, &c.InboundID, &c.UUID, &c.Password, &c.Method,
		&c.SSPassword, &c.Email, &enable, &c.TotalBytes, &c.UpBytes, &c.DownBytes,
		&expire, &c.IPLimit, &c.SpeedLimit, &c.SubToken, &ts, &c.Flow)
	if err != nil {
		return nil, err
	}
	c.Enable = enable != 0
	if expire.Valid {
		t, err := time.Parse(time.RFC3339, expire.String)
		if err == nil {
			c.ExpireAt = &t
		}
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return &c, nil
}

/* ── audit log ───────────────────────────────────────────────────────── */

func (s *Store) AddEvent(level, actor, message, meta string) error {
	_, err := s.db.Exec(`INSERT INTO events (ts, level, actor, message, meta)
		VALUES (?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339),
		level, actor, message, meta)
	return err
}

func (s *Store) RecentEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, ts, level, actor, message, meta
		FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Actor, &e.Message, &e.Meta); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

/* ── helpers ─────────────────────────────────────────────────────────── */

/* ── server secrets ──────────────────────────────────────────────────── */

// InboundSecret is what the core needs and the client link must not contain.
type InboundSecret struct {
	InboundID  int64
	PrivateKey string
	Dest       string
	Listen     string
	CertFile   string
	KeyFile    string
}

// SetInboundSecret stores or replaces the server-only fields of one inbound.
func (s *Store) SetInboundSecret(sec InboundSecret) error {
	if sec.InboundID == 0 {
		return errors.New("secret needs an inbound")
	}
	if sec.Listen == "" {
		sec.Listen = "0.0.0.0"
	}
	_, err := s.db.Exec(`INSERT INTO inbound_secrets
		(inbound_id, private_key, dest, listen, cert_file, key_file)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(inbound_id) DO UPDATE SET
			private_key = excluded.private_key,
			dest = excluded.dest,
			listen = excluded.listen,
			cert_file = excluded.cert_file,
			key_file = excluded.key_file`,
		sec.InboundID, sec.PrivateKey, sec.Dest, sec.Listen, sec.CertFile, sec.KeyFile)
	return err
}

// GetInboundSecret returns the server-only fields. A missing row is an empty
// secret, not an error — the config builder fails later if Reality needs it.
func (s *Store) GetInboundSecret(inboundID int64) (InboundSecret, error) {
	sec := InboundSecret{InboundID: inboundID, Listen: "0.0.0.0"}
	err := s.db.QueryRow(`SELECT private_key, dest, listen, cert_file, key_file
		FROM inbound_secrets WHERE inbound_id = ?`, inboundID).
		Scan(&sec.PrivateKey, &sec.Dest, &sec.Listen, &sec.CertFile, &sec.KeyFile)
	if errors.Is(err, sql.ErrNoRows) {
		return sec, nil
	}
	return sec, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func orAuto(v string) string {
	if v == "" {
		return "auto"
	}
	return v
}
