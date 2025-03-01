package engine

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

type TokenIssuer struct {
	Key *rsa.PrivateKey
}

func NewTokenIssuer(keyFile string) *TokenIssuer {
	t := &TokenIssuer{}
	t.loadOrGenerateKey(keyFile)
	return t
}

func (t *TokenIssuer) loadOrGenerateKey(file string) {
read:
	keyPEM, err := os.ReadFile(file)
	if err == nil {
		block, _ := pem.Decode(keyPEM)
		t.Key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			panic(err)
		}
		return
	}
	if !os.IsNotExist(err) {
		panic(err)
	}

	slog.Info("generating RSA key", "file", file)
	pkey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(pkey),
	}), 0600)
	if err != nil {
		panic(err)
	}

	goto read
}

func (t *TokenIssuer) Sign(claims *jwt.RegisteredClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(t.Key)
}

func (t *TokenIssuer) Verify(tok string) (*jwt.RegisteredClaims, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(tok, claims, func(token *jwt.Token) (any, error) {
		return t.Key.Public(), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

type TokenClaimsFunc func() *jwt.RegisteredClaims

func (t *TokenIssuer) OAuth2(tcf TokenClaimsFunc) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, &tokenSrc{parent: t, claims: tcf})
}

type tokenSrc struct {
	parent *TokenIssuer
	claims TokenClaimsFunc
}

func (t *tokenSrc) Token() (*oauth2.Token, error) {
	claims := t.claims()
	accessToken, err := t.parent.Sign(claims)
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: accessToken, Expiry: claims.ExpiresAt.Time}, nil
}
