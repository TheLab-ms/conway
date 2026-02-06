// Devlogin generates a login URL for the dev@localhost user.
// Run from the .dev directory (where auth.pem and conway.sqlite3 live).
package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	db, err := engine.OpenDB("conway.sqlite3")
	if err != nil {
		panic(err)
	}

	var memberID int64
	err = db.QueryRow("SELECT id FROM members WHERE email = 'dev@localhost'").Scan(&memberID)
	if err != nil {
		panic(fmt.Sprintf("dev@localhost not found - run make seed first: %s", err))
	}

	tokens := engine.NewTokenIssuer("auth.pem")
	tok, err := tokens.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("http://localhost:8080/login?t=%s\n", tok)
}
