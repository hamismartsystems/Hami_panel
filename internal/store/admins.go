package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crypto/pbkdf2"
)

// Admin is one panel operator. Passwords are never stored in the clear.
type Admin struct {
	ID        int64
	Username  string
	Hash      string
	CreatedAt time.Time
	LastLogin *time.Time
}

// Session is one logged-in browser. The token lives in an HttpOnly cookie.
type Session struct {
	Token     string
	AdminID   int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

const (
	pbkdf2Rounds  = 210000
	pbkdf2KeyLen  = 32
	pbkdf2SaltLen = 16
)

var (
	// ErrNoAdmin is returned when the panel has no operator yet.
	ErrNoAdmin = errors.New("no admin account exists")
	// ErrBadCredentials is deliberately vague: the caller must not learn
	// whether the username or the password was the wrong one.
	ErrBadCredentials = errors.New("invalid username or password")
)

// HashPassword derives a PBKDF2-SHA256 hash with a fresh random salt.
// Format: pbkdf2_sha256$rounds$salt_hex$key_b64
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Rounds,
		hex.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored hash. The
// comparison is constant time.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds <= 0 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, rounds, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewSessionToken returns 32 bytes of URL-safe randomness.
func NewSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

/* ── admins ──────────────────────────────────────────────────────────── */

// CreateAdmin stores a new operator. The password is hashed here so callers
// never handle the hash themselves.
func (s *Store) CreateAdmin(username, password string) (*Admin, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO admins(username, hash, created_at) VALUES(?,?,?)`,
		username, hash, now.Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("admin %q already exists", username)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Admin{ID: id, Username: username, Hash: hash, CreatedAt: now}, nil
}

// CountAdmins reports how many operators exist.
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&n)
	return n, err
}

// GetAdminByUsername looks an operator up by name.
func (s *Store) GetAdminByUsername(username string) (*Admin, error) {
	row := s.db.QueryRow(
		`SELECT id, username, hash, created_at, last_login FROM admins WHERE username = ?`,
		strings.TrimSpace(username))
	return scanAdmin(row)
}

// GetAdmin looks an operator up by id.
func (s *Store) GetAdmin(id int64) (*Admin, error) {
	row := s.db.QueryRow(
		`SELECT id, username, hash, created_at, last_login FROM admins WHERE id = ?`, id)
	return scanAdmin(row)
}

// ListAdmins returns every operator, oldest first.
func (s *Store) ListAdmins() ([]Admin, error) {
	rows, err := s.db.Query(
		`SELECT id, username, hash, created_at, last_login FROM admins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		var created string
		var last sql.NullString
		if err := rows.Scan(&a.ID, &a.Username, &a.Hash, &created, &last); err != nil {
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339, created)
		if last.Valid && last.String != "" {
			if t, err := time.Parse(time.RFC3339, last.String); err == nil {
				a.LastLogin = &t
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAdminPassword replaces an operator's password and invalidates every
// session that operator had open.
func (s *Store) SetAdminPassword(username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE admins SET hash = ? WHERE username = ?`, hash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("admin %q not found", username)
	}
	_, err = s.db.Exec(
		`DELETE FROM sessions WHERE admin_id IN (SELECT id FROM admins WHERE username = ?)`,
		username)
	return err
}

// DeleteAdmin removes an operator. The last remaining operator cannot be
// deleted — that would lock everybody out of the panel.
func (s *Store) DeleteAdmin(username string) error {
	n, err := s.CountAdmins()
	if err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("refusing to delete the last admin")
	}
	res, err := s.db.Exec(`DELETE FROM admins WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("admin %q not found", username)
	}
	return nil
}

/* ── authentication ──────────────────────────────────────────────────── */

// Authenticate checks a username/password pair and, on success, opens a
// session valid for ttl. It always costs one PBKDF2 derivation, whether the
// user exists or not, so timing does not leak valid usernames.
func (s *Store) Authenticate(username, password string, ttl time.Duration) (*Session, *Admin, error) {
	a, err := s.GetAdminByUsername(username)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	if a == nil {
		// Burn the same work an existing user would cost.
		VerifyPassword(password, "pbkdf2_sha256$"+strconv.Itoa(pbkdf2Rounds)+
			"$00000000000000000000000000000000$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		return nil, nil, ErrBadCredentials
	}
	if !VerifyPassword(password, a.Hash) {
		return nil, nil, ErrBadCredentials
	}
	now := time.Now().UTC()
	sess := &Session{
		Token:     NewSessionToken(),
		AdminID:   a.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if _, err := s.db.Exec(
		`INSERT INTO sessions(token, admin_id, created_at, expires_at) VALUES(?,?,?,?)`,
		sess.Token, sess.AdminID, sess.CreatedAt.Format(time.RFC3339),
		sess.ExpiresAt.Format(time.RFC3339)); err != nil {
		return nil, nil, err
	}
	if _, err := s.db.Exec(`UPDATE admins SET last_login = ? WHERE id = ?`,
		now.Format(time.RFC3339), a.ID); err != nil {
		return nil, nil, err
	}
	_ = s.PurgeExpiredSessions()
	return sess, a, nil
}

// SessionAdmin resolves a session token to its operator. Expired tokens are
// rejected and removed.
func (s *Store) SessionAdmin(token string) (*Admin, error) {
	if token == "" {
		return nil, ErrBadCredentials
	}
	var adminID int64
	var expires string
	err := s.db.QueryRow(
		`SELECT admin_id, expires_at FROM sessions WHERE token = ?`, token).
		Scan(&adminID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().UTC().After(exp) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return nil, ErrBadCredentials
	}
	return s.GetAdmin(adminID)
}

// DeleteSession logs one browser out.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// PurgeExpiredSessions drops sessions that are past their expiry.
func (s *Store) PurgeExpiredSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

func scanAdmin(row *sql.Row) (*Admin, error) {
	var a Admin
	var created string
	var last sql.NullString
	err := row.Scan(&a.ID, &a.Username, &a.Hash, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	if last.Valid && last.String != "" {
		if t, err := time.Parse(time.RFC3339, last.String); err == nil {
			a.LastLogin = &t
		}
	}
	return &a, nil
}
