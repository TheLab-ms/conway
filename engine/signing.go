package engine

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type ValueSigner[T any] struct {
	key []byte
}

func NewValueSigner[T any]() *ValueSigner[T] {
	v := &ValueSigner[T]{}
	v.initSigningKey()
	return v
}

func (v *ValueSigner[T]) initSigningKey() {
	v.key = make([]byte, 32)
	if _, err := rand.Read(v.key); err != nil {
		panic(err)
	}
}

func (v *ValueSigner[T]) Sign(val T, ttl time.Duration) string {
	js, err := json.Marshal(&signedValue[T]{Value: val, Exp: time.Now().Add(ttl).Unix()})
	if err != nil {
		panic(err)
	}
	h := hmac.New(sha256.New, v.key)
	h.Write(js)
	return fmt.Sprintf("%s.%s", js, base64.StdEncoding.EncodeToString(h.Sum(nil)))
}

func (v *ValueSigner[T]) Verify(str string) (val T, valid bool) {
	parts := strings.Split(str, ".")
	if len(parts) != 2 {
		return
	}

	sig, _ := base64.StdEncoding.DecodeString(parts[1])
	h := hmac.New(sha256.New, v.key)
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
