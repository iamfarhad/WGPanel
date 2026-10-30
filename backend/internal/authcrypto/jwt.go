package authcrypto

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 7 * 24 * time.Hour
)

type Claims struct {
	AdminID  string `json:"admin_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAccessToken signs a short-lived JWT identifying the admin.
func IssueAccessToken(secret, adminID, username, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		AdminID:  adminID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseAccessToken validates signature and expiry and returns the claims. Callers
// (httpapi.requireAdmin) treat any error as "reject with 401" without distinguishing
// why - there's no security benefit to telling a caller whether their token was
// expired vs malformed vs wrongly signed.
func ParseAccessToken(secret, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// IssueRefreshToken starts a new token family for a login (password, MFA or passkey):
// an opaque random token stored in Redis, mapped to the admin id with a TTL. Each use
// rotates it - see refresh.go. Revoking a session is deleting the family's live key.
func IssueRefreshToken(ctx context.Context, rdb *redis.Client, adminID string) (string, error) {
	return issueFamilyToken(ctx, rdb, adminID)
}

func refreshTokenKey(token string) string {
	return "refresh_token:" + token
}

// ErrInvalidRefreshToken covers both "never existed" and "expired" - Redis's own
// TTL eviction makes those indistinguishable, which is fine: either way the caller
// must log in again.
var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

// ValidateRefreshToken looks up the admin id a refresh token maps to without
// consuming it. The refresh endpoint uses RotateRefreshToken instead.
func ValidateRefreshToken(ctx context.Context, rdb *redis.Client, token string) (string, error) {
	adminID, err := rdb.Get(ctx, refreshTokenKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrInvalidRefreshToken
	}
	if err != nil {
		return "", fmt.Errorf("look up refresh token: %w", err)
	}
	return adminOfRefreshValue(adminID), nil
}
