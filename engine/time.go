package engine

import (
	"fmt"
	"time"
)

var loc *time.Location

func init() {
	var err error
	loc, err = time.LoadLocation("America/Chicago")
	if err != nil {
		panic(err)
	}
}

type LocalTime struct {
	Time time.Time
}

func (l *LocalTime) Scan(src any) error {
	epochUTC, ok := src.(int64)
	if !ok {
		return fmt.Errorf("expected int64, got %T", src)
	}

	l.Time = time.Unix(epochUTC, 0).In(loc)
	return nil
}
