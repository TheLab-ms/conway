package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	IntSigner    = &ValueSigner[int64]{}
	StringSigner = &ValueSigner[string]{}
)

type ValueSigner[T any] struct{}

func (v *ValueSigner[T]) Sign(val T, key []byte, ttl time.Duration) string {
	js, err := json.Marshal(&signedValue[T]{Value: val, Exp: time.Now().Add(ttl).Unix()})
	if err != nil {
		panic(err)
	}
	h := hmac.New(sha256.New, key)
	h.Write(js)
	return fmt.Sprintf("%s.%s", js, base64.StdEncoding.EncodeToString(h.Sum(nil)))
}

func (v *ValueSigner[T]) Verify(str string, key []byte) (val T, valid bool) {
	parts := strings.Split(str, ".")
	if len(parts) != 2 {
		return
	}

	sig, _ := base64.StdEncoding.DecodeString(parts[1])
	h := hmac.New(sha256.New, key)
	io.WriteString(h, parts[0])
	if !hmac.Equal(sig, h.Sum(nil)) {
		return
	}

	sv := &signedValue[T]{}
	err := json.Unmarshal([]byte(parts[0]), sv)
	if err != nil {
		return
	}
	if time.Now().Unix() > sv.Exp {
		return
	}
	return sv.Value, true
}

type signedValue[T any] struct {
	Value T     `json:"v"`
	Exp   int64 `json:"e"`
}
