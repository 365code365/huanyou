// Package auth handles password hashing and JWT session tokens.
package auth

import (
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
