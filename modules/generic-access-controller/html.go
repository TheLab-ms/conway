package gac

import (
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var doorFromStatusRegex = regexp.MustCompile(`\[[^]]+\]`)

type swipeBuilder struct {
	current *CardSwipe
	set     []*CardSwipe
}

func parseSwipesList(r io.Reader) ([]*CardSwipe, error) {
	builder := &swipeBuilder{}
	if err := parseTable(r, builder); err != nil {
		return nil, err
	}
	return builder.set, nil
}

func (s *swipeBuilder) Pop() {
	if s.current == nil || s.current.ID == 0 {
		s.current = nil
		return
	}
	s.set = append(s.set, s.current)
	s.current = nil
}

func (s *swipeBuilder) ProcessCell(col int, val string) {
	if s.current == nil {
		s.current = &CardSwipe{}
	}

	switch col {
	case 0:
		s.current.ID, _ = strconv.Atoi(val)
	case 1:
		s.current.CardID, _ = strconv.Atoi(val)
	case 2:
		s.current.Name = val
	case 3:
		if strings.Contains(val, "Reboot") {
			s.current.ID = 0 // discard
		}
		if strings.Contains(val, "Allow IN") {
			s.current.DoorID = doorFromStatusRegex.FindString(val)
			if s.current.DoorID != "" {
				// remove square braces
				s.current.DoorID = s.current.DoorID[1 : len(s.current.DoorID)-1]
			}
		}
	case 4:
		s.current.Time, _ = time.Parse("2006-01-02 15:04:05", val)
	}
}

type cardBuilder struct {
	current *Card
	set     []*Card
}

func parseCardsList(r io.Reader) ([]*Card, error) {
	builder := &cardBuilder{}
	if err := parseTable(r, builder); err != nil {
		return nil, err
	}
	return builder.set, nil
}

func (c *cardBuilder) Pop() {
	if c.current == nil || c.current.ID == 0 {
		c.current = nil
		return
	}
	c.set = append(c.set, c.current)
	c.current = nil
}

func (c *cardBuilder) ProcessCell(col int, val string) {
	if c.current == nil {
		c.current = &Card{}
	}

	switch col {
	case 0:
		c.current.ID, _ = strconv.Atoi(val)
	case 1:
		c.current.Number, _ = strconv.Atoi(val)
	case 2:
		c.current.Name = val
	}
}

type tableBuilder interface {
	Pop()
	ProcessCell(col int, val string)
}

func parseTable(r io.Reader, builder tableBuilder) error {
	doc, err := html.Parse(r)
	if err != nil {
		return err
	}

	var foundTable bool
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			foundTable = true
		}
		if n.Type == html.ElementNode && n.Data == "tr" {
			col := 0
			for td := n.FirstChild; td != nil; td = td.NextSibling {
				if td.Type != html.ElementNode || td.Data != "td" {
					continue
				}
				builder.ProcessCell(col, td.FirstChild.Data)
				col++
			}

			builder.Pop()
			col = 0
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}

	traverse(doc)
	if !foundTable {
		return errors.New("no table found in access controller response")
	}
	return nil
}
