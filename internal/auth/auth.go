// Package auth is the OSS single-admin AuthProvider: bcrypt password,
// HS256 JWT (24h). On first boot it creates the "admin" account with a
// random password printed to the log exactly once, and persists a random
// JWT secret in settings(grp='internal') so tokens survive restarts.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"mynah/core"
	"mynah/internal/store"
)

const tokenTTL = 24 * time.Hour

var (
	ErrBadCredentials = errors.New("invalid username or password")
	ErrBadToken       = errors.New("invalid or expired token")
)

type Provider struct {
	db     *store.Store
	secret []byte
}

// New bootstraps the admin account and JWT secret if absent.
func New(ctx context.Context, db *store.Store) (*Provider, error) {
	p := &Provider{db: db}

	var secretHex string
	err := db.Pool.QueryRow(ctx,
		`SELECT value->>'jwt_secret' FROM settings WHERE grp = 'internal'`).Scan(&secretHex)
	if err != nil || secretHex == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		secretHex = hex.EncodeToString(buf)
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO settings (grp, value) VALUES ('internal', jsonb_build_object('jwt_secret', $1::text))
			ON CONFLICT (grp) DO UPDATE
			SET value = settings.value || jsonb_build_object('jwt_secret', $1::text), updated_at = now()`,
			secretHex); err != nil {
			return nil, fmt.Errorf("persist jwt secret: %w", err)
		}
	}
	p.secret, _ = hex.DecodeString(secretHex)

	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		pwBuf := make([]byte, 12)
		if _, err := rand.Read(pwBuf); err != nil {
			return nil, err
		}
		pw := hex.EncodeToString(pwBuf)
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO admins (username, password_hash) VALUES ('admin', $1)`, string(hash)); err != nil {
			return nil, err
		}
		// The only place this password ever appears; user must change it.
		log.Printf("auth: created initial admin account — username=admin password=%s (change it via POST /api/v1/auth/password)", pw)
	}
	return p, nil
}

func (p *Provider) Login(ctx context.Context, username, password string) (string, bool, error) {
	var (
		id         int64
		hash       string
		mustChange bool
	)
	err := p.db.Pool.QueryRow(ctx,
		`SELECT id, password_hash, must_change_password FROM admins WHERE username = $1`,
		username).Scan(&id, &hash, &mustChange)
	if err != nil {
		return "", false, ErrBadCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", false, ErrBadCredentials
	}
	claims := jwt.MapClaims{
		"sub": fmt.Sprint(id),
		"usr": username,
		"exp": time.Now().Add(tokenTTL).Unix(),
		"iat": time.Now().Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(p.secret)
	if err != nil {
		return "", false, err
	}
	return token, mustChange, nil
}

func (p *Provider) Verify(ctx context.Context, token string) (*core.Identity, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrBadToken
		}
		return p.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return nil, ErrBadToken
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrBadToken
	}
	var id int64
	fmt.Sscan(fmt.Sprint(claims["sub"]), &id)
	usr, _ := claims["usr"].(string)
	if usr == "" {
		return nil, ErrBadToken
	}
	return &core.Identity{ID: id, Username: usr}, nil
}

func (p *Provider) ChangePassword(ctx context.Context, ident *core.Identity, oldPw, newPw string) error {
	if len(newPw) < 8 {
		return errors.New("new password must be at least 8 characters")
	}
	var hash string
	if err := p.db.Pool.QueryRow(ctx,
		`SELECT password_hash FROM admins WHERE id = $1`, ident.ID).Scan(&hash); err != nil {
		return ErrBadCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPw)) != nil {
		return ErrBadCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = p.db.Pool.Exec(ctx,
		`UPDATE admins SET password_hash = $1, must_change_password = false, updated_at = now() WHERE id = $2`,
		string(newHash), ident.ID)
	return err
}
