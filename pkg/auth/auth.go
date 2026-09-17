package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Token types, stored as the "typ" claim so a token class can never be used in
// place of another (an admin token and a user token share the same secret).
const (
	TokenTypeAdmin = "admin"
	TokenTypeUser  = "user"
)

// AdminClaims is the JWT payload for an authenticated administrator.
type AdminClaims struct {
	Type    string    `json:"typ"`
	AdminID uuid.UUID `json:"admin_id"`
	Email   string    `json:"email"`
	jwt.RegisteredClaims
}

// UserClaims is the JWT payload for an authenticated pack submitter.
type UserClaims struct {
	Type   string    `json:"typ"`
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	Role   string    `json:"role"`
	jwt.RegisteredClaims
}

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

// CheckPassword reports whether the plaintext password matches the hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateToken creates a signed JWT for an admin.
func GenerateToken(secret string, adminID uuid.UUID, email string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := AdminClaims{
		Type:    TokenTypeAdmin,
		AdminID: adminID,
		Email:   email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseToken validates an admin JWT and returns its claims. It only accepts
// HS256 tokens of type "admin" that carry a non-zero AdminID, so a user token
// (same secret, different claims) can never authenticate as an admin.
func ParseToken(secret, tokenString string) (*AdminClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &AdminClaims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(*AdminClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}
	if claims.Type != TokenTypeAdmin || claims.AdminID == uuid.Nil {
		return nil, fmt.Errorf("not an admin token")
	}
	return claims, nil
}

// GenerateUserToken creates a signed JWT for an authenticated user.
func GenerateUserToken(secret string, userID uuid.UUID, email, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := UserClaims{
		Type:   TokenTypeUser,
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseUserToken validates a user JWT and returns its claims. It only accepts
// HS256 tokens of type "user" that carry a non-zero UserID.
func ParseUserToken(secret, tokenString string) (*UserClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &UserClaims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(*UserClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}
	if claims.Type != TokenTypeUser || claims.UserID == uuid.Nil {
		return nil, fmt.Errorf("not a user token")
	}
	return claims, nil
}

type contextKey string

const adminContextKey contextKey = "admin"

// WithAdmin stores the admin claims in the request context.
func WithAdmin(ctx context.Context, claims *AdminClaims) context.Context {
	return context.WithValue(ctx, adminContextKey, claims)
}

// AdminFromContext returns the admin claims stored in the context, if any.
func AdminFromContext(ctx context.Context) (*AdminClaims, bool) {
	claims, ok := ctx.Value(adminContextKey).(*AdminClaims)
	return claims, ok
}

// Middleware validates the Bearer JWT for admin-protected routes.
func Middleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := bearer(r)
			if tokenString == "" {
				writeAuthError(w, http.StatusUnauthorized, "Authorization header required")
				return
			}

			claims, err := ParseToken(secret, tokenString)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithAdmin(r.Context(), claims)))
		})
	}
}

// ReviewMiddleware validates a reviewer session: an admin token (from the admin
// login) or any valid user token. The role is NOT checked from the token claim
// (it can be stale: a user promoted to reviewer keeps the old role until the
// token is reissued); the review handlers re-check the current role against the
// database (RequireReviewer), so a promotion grants access immediately and a
// demotion revokes it.
func ReviewMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := bearer(r)
			if tokenString == "" {
				writeAuthError(w, http.StatusUnauthorized, "Authorization header required")
				return
			}
			if claims, err := ParseToken(secret, tokenString); err == nil {
				r = r.WithContext(WithAdmin(r.Context(), claims))
				next.ServeHTTP(w, r)
				return
			}
			claims, err := ParseUserToken(secret, tokenString)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}
			r = r.WithContext(WithUser(r.Context(), claims))
			next.ServeHTTP(w, r)
		})
	}
}

func bearer(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader {
		return ""
	}
	return tokenString
}

// writeAuthError writes a JSON error body with the correct content type.
func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, message)
}

type userContextKey string

const userContextKeyVal userContextKey = "user"

// WithUser stores the user claims in the request context.
func WithUser(ctx context.Context, claims *UserClaims) context.Context {
	return context.WithValue(ctx, userContextKeyVal, claims)
}

// UserFromContext returns the user claims stored in the context, if any.
func UserFromContext(ctx context.Context) (*UserClaims, bool) {
	claims, ok := ctx.Value(userContextKeyVal).(*UserClaims)
	return claims, ok
}

// UserMiddleware validates the Bearer JWT for user-protected routes. Account
// state (email verification, current role) is enforced by the handlers.
func UserMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := bearer(r)
			if tokenString == "" {
				writeAuthError(w, http.StatusUnauthorized, "Authorization header required")
				return
			}

			claims, err := ParseUserToken(secret, tokenString)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), claims)))
		})
	}
}
