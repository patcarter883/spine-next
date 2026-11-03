// Package auth holds dashboard users, their roles and login sessions.
//
// Users live in a YAML file (Home()/users.yaml) with SHA-256 hashes of
// their tokens; a token is shown once, when the user is added. A request
// authenticates with "Authorization: Bearer <token>" (scripts) or a session
// cookie obtained by POST /api/login (the browser). Roles are ordered:
// viewer (read), operator (task actions), admin (repositories, settings).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Role orders what a user may do.
type Role string

const (
	Viewer   Role = "viewer"
	Operator Role = "operator"
	Admin    Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case Viewer:
		return 1
	case Operator:
		return 2
	case Admin:
		return 3
	}
	return 0
}

// AtLeast reports whether r includes need.
func (r Role) AtLeast(need Role) bool { return r.rank() >= need.rank() && r.rank() > 0 }

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	if r.rank() == 0 {
		return "", fmt.Errorf("role must be viewer, operator or admin, not %q", s)
	}
	return r, nil
}

// User is one dashboard user.
type User struct {
	Name      string    `yaml:"name" json:"name"`
	Role      Role      `yaml:"role" json:"role"`
	TokenHash string    `yaml:"token_sha256" json:"-"`
	Created   time.Time `yaml:"created" json:"created"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// Store is the users file.
type Store struct{ Path string }

type usersFile struct {
	Users []User `yaml:"users"`
}

// Load reads the users (absent file = none).
func (s Store) Load() ([]User, error) {
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f usersFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	for _, u := range f.Users {
		if _, err := ParseRole(string(u.Role)); err != nil {
			return nil, fmt.Errorf("%s: user %s: %w", s.Path, u.Name, err)
		}
	}
	return f.Users, nil
}

func (s Store) save(us []User) error {
	sort.Slice(us, func(i, j int) bool { return us[i].Name < us[j].Name })
	b, err := yaml.Marshal(usersFile{Users: us})
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Add creates a user (or replaces its token and role) and returns the new
// token. The token is not stored, only its hash.
func (s Store) Add(name string, role Role) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("user name must match %s", nameRe)
	}
	if _, err := ParseRole(string(role)); err != nil {
		return "", err
	}
	us, err := s.Load()
	if err != nil {
		return "", err
	}
	tok := NewToken()
	out := us[:0]
	for _, u := range us {
		if u.Name != name {
			out = append(out, u)
		}
	}
	out = append(out, User{Name: name, Role: role, TokenHash: HashToken(tok), Created: time.Now().UTC()})
	return tok, s.save(out)
}

// Remove deletes a user.
func (s Store) Remove(name string) error {
	us, err := s.Load()
	if err != nil {
		return err
	}
	out := us[:0]
	found := false
	for _, u := range us {
		if u.Name == name {
			found = true
			continue
		}
		out = append(out, u)
	}
	if !found {
		return fmt.Errorf("no user %q", name)
	}
	return s.save(out)
}

// NewToken returns a random token.
func NewToken() string {
	var b [24]byte
	rand.Read(b[:])
	return "spn_" + hex.EncodeToString(b[:])
}

// HashToken hashes a token for storage.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Authenticator checks requests against the users file (re-read when it
// changes) and keeps browser sessions in memory.
type Authenticator struct {
	Store Store
	// SessionTTL bounds a session (default 12h).
	SessionTTL time.Duration
	// Secure marks the session cookie Secure (set when serving TLS).
	Secure bool

	mu       sync.Mutex
	users    []User
	mtime    time.Time
	sessions map[string]session
}

type session struct {
	user    string
	expires time.Time
}

// CookieName is the session cookie.
const CookieName = "spine_session"

func (a *Authenticator) reload() []User {
	a.mu.Lock()
	defer a.mu.Unlock()
	fi, err := os.Stat(a.Store.Path)
	if err != nil {
		a.users = nil
		return nil
	}
	if !fi.ModTime().Equal(a.mtime) {
		if us, err := a.Store.Load(); err == nil {
			a.users, a.mtime = us, fi.ModTime()
		}
	}
	return a.users
}

// Enabled reports whether any user exists.
func (a *Authenticator) Enabled() bool { return len(a.reload()) > 0 }

func (a *Authenticator) byToken(tok string) (User, bool) {
	h := HashToken(tok)
	for _, u := range a.reload() {
		if subtle.ConstantTimeCompare([]byte(h), []byte(u.TokenHash)) == 1 {
			return u, true
		}
	}
	return User{}, false
}

func (a *Authenticator) byName(name string) (User, bool) {
	for _, u := range a.reload() {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// Check authenticates a request.
func (a *Authenticator) Check(r *http.Request) (User, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return a.byToken(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return User{}, false
	}
	a.mu.Lock()
	s, ok := a.sessions[c.Value]
	if ok && time.Now().After(s.expires) {
		delete(a.sessions, c.Value)
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		return User{}, false
	}
	return a.byName(s.user) // a removed user loses their sessions
}

// Login exchanges a token for a session cookie.
func (a *Authenticator) Login(w http.ResponseWriter, tok string) (User, bool) {
	u, ok := a.byToken(strings.TrimSpace(tok))
	if !ok {
		return User{}, false
	}
	var b [32]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	ttl := a.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]session{}
	}
	for k, s := range a.sessions {
		if time.Now().After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[id] = session{user: u.Name, expires: time.Now().Add(ttl)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: id, Path: "/", HttpOnly: true, Secure: a.Secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds())})
	return u, true
}

// Logout ends the request's session.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.Secure,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
