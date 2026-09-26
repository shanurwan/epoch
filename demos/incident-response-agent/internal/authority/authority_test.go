package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
)

var (
	notBefore = time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	expiresAt = notBefore.Add(30 * time.Second)
)

func validIssueRequest() IssueRequest {
	return IssueRequest{
		Subject:   "incident-agent-01",
		Scope:     "worker.restart",
		Resource:  "payments-worker-17",
		NotBefore: notBefore,
		ExpiresAt: expiresAt,
	}
}

func issued(t *testing.T) (IssueResult, Validator) {
	t.Helper()
	result, err := IssueEphemeral(validIssueRequest())
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := DecodePublicKey(result.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return result, Validator{PublicKey: publicKey, ExpectedSubject: "incident-agent-01"}
}

func TestValidityIntervalAndSignature(t *testing.T) {
	result, validator := issued(t)
	tests := []struct {
		name string
		now  time.Time
		want api.Decision
	}{
		{"before-nbf", notBefore.Add(-time.Nanosecond), api.DenyNotYetValid},
		{"at-nbf", notBefore, api.Authorized},
		{"inside", expiresAt.Add(-time.Nanosecond), api.Authorized},
		{"at-exp", expiresAt, api.DenyExpired},
		{"after-exp", expiresAt.Add(time.Nanosecond), api.DenyExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validator.Evaluate(result.Token, test.now, "worker.restart", "payments-worker-17")
			if got.Decision != test.want {
				t.Fatalf("decision = %s, want %s", got.Decision, test.want)
			}
		})
	}
}

func TestTypedAuthorityDenials(t *testing.T) {
	result, validator := issued(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		validator Validator
		token     string
		scope     string
		resource  string
		want      api.Decision
	}{
		{"invalid-signature", Validator{PublicKey: otherPublic, ExpectedSubject: "incident-agent-01"}, result.Token, "worker.restart", "payments-worker-17", api.DenyInvalidSignature},
		{"wrong-subject", Validator{PublicKey: validator.PublicKey, ExpectedSubject: "other-agent"}, result.Token, "worker.restart", "payments-worker-17", api.DenySubjectMismatch},
		{"wrong-scope", validator, result.Token, "worker.inspect", "payments-worker-17", api.DenyScopeMismatch},
		{"wrong-resource", validator, result.Token, "worker.restart", "payments-worker-18", api.DenyResourceMismatch},
		{"malformed", validator, "not-a-jwt", "worker.restart", "payments-worker-17", api.DenyMalformedAuthority},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.validator.Evaluate(test.token, notBefore, test.scope, test.resource)
			if got.Decision != test.want {
				t.Fatalf("decision = %s, want %s", got.Decision, test.want)
			}
		})
	}
}

func TestMalformedSignedClaimsFailClosed(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := Claims{
		Scope:    "worker.restart",
		Resource: "payments-worker-17",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "incident-agent-01",
			NotBefore: jwt.NewNumericDate(notBefore),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	got := (Validator{PublicKey: publicKey, ExpectedSubject: "incident-agent-01"}).Evaluate(token, notBefore, "worker.restart", "payments-worker-17")
	if got.Decision != api.DenyMalformedAuthority {
		t.Fatalf("decision = %s, want %s", got.Decision, api.DenyMalformedAuthority)
	}
}
