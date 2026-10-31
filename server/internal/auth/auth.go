// Package auth handles password hashing and JWT session tokens.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// dummyHash is compared against when a login email does not exist, so that
// response time does not reveal whether an account exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcryptCost)

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

// CheckPassword returns true if pw matches hash. Pass hash=="" for a
// non-existent user; it still spends the same CPU time and returns false.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, errors.New("jwt secret must be at least 32 characters")
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

func (i *Issuer) Issue(userID int64) (string, time.Time, error) {
	exp := time.Now().Add(i.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(userID, 10),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
	s, err := tok.SignedString(i.secret)
	return s, exp, err
}

// IssueAdmin returns a short-lived token for the admin panel (8h).
func (i *Issuer) IssueAdmin() (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "admin",
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(8 * time.Hour)),
	})
	return tok.SignedString(i.secret)
}

// VerifyAdmin reports whether token is a valid admin token.
func (i *Issuer) VerifyAdmin(token string) bool {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired())
	return err == nil && claims.Subject == "admin"
}

// Verify returns the user ID in a valid token.
func (i *Issuer) Verify(token string) (int64, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired())
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(claims.Subject, 10, 64)
}

// ---- session-bound tokens ----
//
// A session token carries the client's session key (negotiated by X25519 at
// sign-in, never sent in the clear), wrapped with AES-GCM under a key derived
// from the JWT secret. The server recovers it on every request to check the
// request signature and to encrypt the response; a stolen token alone can
// neither sign requests nor read responses.

type sessionClaims struct {
	jwt.RegisteredClaims
	SK string `json:"sk"`
}

func (i *Issuer) wrapAEAD() (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, i.secret, nil, "huanyou-token-wrap-v1", 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// IssueSession is Issue with a session key bound to the token.
func (i *Issuer) IssueSession(userID int64, key []byte) (string, time.Time, error) {
	aead, err := i.wrapAEAD()
	if err != nil {
		return "", time.Time{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	sub := strconv.FormatInt(userID, 10)
	sealed := aead.Seal(nonce, nonce, key, []byte(sub))
	exp := time.Now().Add(i.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		SK: base64.RawURLEncoding.EncodeToString(sealed),
	})
	s, err := tok.SignedString(i.secret)
	return s, exp, err
}

// VerifySession returns the user ID and, for a session token, its session
// key (nil for a token issued before session keys).
func (i *Issuer) VerifySession(token string) (int64, []byte, error) {
	var claims sessionClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired())
	if err != nil {
		return 0, nil, err
	}
	uid, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || claims.Subject == "admin" {
		return 0, nil, errors.New("not a user token")
	}
	if claims.SK == "" {
		return uid, nil, nil
	}
	sealed, err := base64.RawURLEncoding.DecodeString(claims.SK)
	aead, aerr := i.wrapAEAD()
	if err != nil || aerr != nil || len(sealed) < aead.NonceSize() {
		return 0, nil, errors.New("bad session key")
	}
	key, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(claims.Subject))
	if err != nil {
		return 0, nil, errors.New("bad session key")
	}
	return uid, key, nil
}
