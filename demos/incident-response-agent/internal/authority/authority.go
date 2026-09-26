package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
)

const MaxTokenBytes = 16 << 10

type Claims struct {
	Scope    string `json:"scope"`
	Resource string `json:"resource"`
	jwt.RegisteredClaims
}

type IssueRequest struct {
	Subject   string    `json:"subject"`
	Scope     string    `json:"scope"`
	Resource  string    `json:"resource"`
	NotBefore time.Time `json:"not_before"`
	ExpiresAt time.Time `json:"expires_at"`
}

type IssueResult struct {
	PublicKey string `json:"public_key"`
	Token     string `json:"token"`
}

type Evaluation struct {
	Decision api.Decision
	Claims   Claims
}

type Validator struct {
	PublicKey       ed25519.PublicKey
	ExpectedSubject string
}

func IssueEphemeral(req IssueRequest) (IssueResult, error) {
	if req.Subject == "" || req.Scope == "" || req.Resource == "" ||
		len(req.Subject) > 256 || len(req.Scope) > 128 || len(req.Resource) > 256 ||
		!req.NotBefore.Before(req.ExpiresAt) {
		return IssueResult{}, errors.New("invalid authority request")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return IssueResult{}, err
	}
	jtiBytes := make([]byte, 16)
	if _, err = rand.Read(jtiBytes); err != nil {
		return IssueResult{}, err
	}
	claims := Claims{
		Scope:    req.Scope,
		Resource: req.Resource,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   req.Subject,
			ID:        hex.EncodeToString(jtiBytes),
			NotBefore: jwt.NewNumericDate(req.NotBefore.UTC()),
			ExpiresAt: jwt.NewNumericDate(req.ExpiresAt.UTC()),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(privateKey)
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{
		PublicKey: base64.RawStdEncoding.EncodeToString(publicKey),
		Token:     token,
	}, nil
}

func DecodePublicKey(encoded string) (ed25519.PublicKey, error) {
	b, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

func (v Validator) Evaluate(tokenString string, now time.Time, scope, resource string) Evaluation {
	malformed := Evaluation{Decision: api.DenyMalformedAuthority}
	if len(v.PublicKey) != ed25519.PublicKeySize || tokenString == "" || len(tokenString) > MaxTokenBytes || strings.Count(tokenString, ".") != 2 {
		return malformed
	}
	claims := Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithoutClaimsValidation(),
	)
	parsed, err := parser.ParseWithClaims(tokenString, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return v.PublicKey, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return Evaluation{Decision: api.DenyInvalidSignature}
		}
		return malformed
	}
	if parsed == nil || !parsed.Valid || claims.Subject == "" || claims.ID == "" ||
		claims.Scope == "" || claims.Resource == "" || claims.NotBefore == nil ||
		claims.ExpiresAt == nil || !claims.NotBefore.Time.Before(claims.ExpiresAt.Time) {
		return malformed
	}
	result := Evaluation{Decision: api.Authorized, Claims: claims}
	if claims.Subject != v.ExpectedSubject {
		result.Decision = api.DenySubjectMismatch
		return result
	}
	if claims.Scope != scope {
		result.Decision = api.DenyScopeMismatch
		return result
	}
	if claims.Resource != resource {
		result.Decision = api.DenyResourceMismatch
		return result
	}
	now = now.UTC()
	if now.Before(claims.NotBefore.Time) {
		result.Decision = api.DenyNotYetValid
		return result
	}
	if !now.Before(claims.ExpiresAt.Time) {
		result.Decision = api.DenyExpired
	}
	return result
}
