package gac

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrCardIDConflict = errors.New("badge ID already in use")

type CardSwipe struct {
	ID     int    // increments for each log entry
	Name   string // name associated with the CardID
	CardID int
	DoorID string
	Time   time.Time
}

type Card struct {
	ID     int    // assigned when adding
	Number int    // encoded on the fob
	Name   string // 32 char opaque(?) string
}

type Client struct {
	Addr    string
	Timeout time.Duration

	mut  sync.Mutex
	conn net.Conn
}

func (c *Client) AddCard(num int, name string) error {
	c.mut.Lock()
	defer c.mut.Unlock()

	if err := c.login(); err != nil {
		return fmt.Errorf("logging in: %w", err)
	}

	// we cannot use url.Values here because order is important to the server for some reason
	q := fmt.Sprintf("AD21=%d&AD22=%s&25=Add", num, name)

	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_312", strings.NewReader(q))
	if err != nil {
		return err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	switch {
	case bytes.Contains(body, []byte("Add Successfully")):
		return nil
	case bytes.Contains(body, []byte("already used!")):
		return ErrCardIDConflict
	default:
		return fmt.Errorf("unknown error response: %s", body)
	}
}

func (c *Client) RemoveCard(id int) error {
	c.mut.Lock()
	defer c.mut.Unlock()

	if err := c.login(); err != nil {
		return fmt.Errorf("logging in: %w", err)
	}
	if err := c.reset(); err != nil {
		return fmt.Errorf("resetting: %w", err)
	}
	if err := c.startRemoving(id); err != nil {
		return fmt.Errorf("starting removal: %w", err)
	}
	if err := c.confirmRemoving(id); err != nil {
		return fmt.Errorf("confirming removal: %w", err)
	}

	return nil
}

func (c *Client) startRemoving(id int) error {
	q := fmt.Sprintf("D%d=Delete", id-1)
	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_324", strings.NewReader(q))
	if err != nil {
		return err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if !bytes.Contains(body, []byte("[User]->[Delete]")) {
		return fmt.Errorf("unknown error response: %s", body)
	}

	return nil
}

func (c *Client) confirmRemoving(id int) error {
	q := fmt.Sprintf("X%d=OK", id-1)
	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_324", strings.NewReader(q))
	if err != nil {
		return err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if !bytes.Contains(body, []byte("user is deleted")) {
		return errors.New("unknown error response")
	}

	return nil
}

func (c *Client) ListCards() ([]*Card, error) {
	c.mut.Lock()
	defer c.mut.Unlock()

	if err := c.login(); err != nil {
		return nil, fmt.Errorf("logging in: %w", err)
	}
	if err := c.reset(); err != nil {
		return nil, fmt.Errorf("resetting: %w", err)
	}

	startID := -19
	all := []*Card{}
	for {
		cards, err := c.listCardPage(startID)
		if err != nil {
			return nil, err
		}
		if len(cards) == 0 {
			return all, nil
		}
		all = append(all, cards...)
		startID += 20
	}
}

func (c *Client) listCardPage(startID int) ([]*Card, error) {
	form := url.Values{}

	if startID == -19 {
		form.Add("PC", "00061")
		form.Add("PE", "00080")
		form.Add("PF", "First")
	} else {
		form.Add("PC", fmt.Sprintf("%05d", startID))
		form.Add("PE", fmt.Sprintf("%05d", startID+19))
		form.Add("PN", "Next")
	}

	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_325", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseCardsList(resp.Body)
}

// ListSwipes lists all card swipes going back to a particular swipe ID.
// To travel all the way back to the beginning of the log, set earliestID to -1.
func (c *Client) ListSwipes(earliestID int, fn func(*CardSwipe) error) error {
	c.mut.Lock()
	defer c.mut.Unlock()

	i := 0
	latestID := -1
	for {
		page, err := c.listSwipePage(latestID)
		if err != nil {
			return err
		}

		for i, item := range page {
			if i == 0 {
				latestID = item.ID - len(page)
			}
			if item.ID <= earliestID {
				return nil
			}
			if err := fn(item); err != nil {
				return err
			}
		}
		i++
	}
}

func (c *Client) listSwipePage(earliestID int) ([]*CardSwipe, error) {
	req, err := c.newListSwipePageRequest(earliestID)
	if err != nil {
		return nil, err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseSwipesList(resp.Body)
}

func (c *Client) newListSwipePageRequest(latestID int) (*http.Request, error) {
	if latestID == -1 {
		form := url.Values{}
		form.Add("s4", "Swipe")
		return http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_21", strings.NewReader(form.Encode()))
	}

	form := url.Values{}
	form.Add("PC", strconv.Itoa(latestID+19))
	form.Add("PE", "0")
	form.Add("PN", "Next")
	return http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_345", strings.NewReader(form.Encode()))
}

func (c *Client) login() error {
	// this controller is so insecure I have no concern about committing its "password" here.
	// most endpoints don't even require it, those that do are open for a window of time after password is sent - no cookie/token/etc needed.
	q := "username=abc&pwd=654321&logId=20101222"

	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_1", strings.NewReader(q))
	if err != nil {
		return err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if bytes.Contains(body, []byte("Remote Open")) {
		return nil // returned the home page
	}

	return errors.New("unknown error")
}

func (c *Client) reset() error {
	req, err := http.NewRequest("POST", "http://"+c.Addr+"/ACT_ID_21", strings.NewReader("s2=Users"))
	if err != nil {
		return err
	}

	resp, err := c.doHTTP(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

func (c *Client) doHTTP(req *http.Request) (resp *http.Response, err error) {
	if c.conn == nil {
		log.Printf("establishing new connection to the access control server")
		c.conn, err = net.DialTimeout("tcp", c.Addr, c.Timeout)
		if err != nil {
			return nil, err
		}
	}

	defer c.conn.SetDeadline(time.Time{}) // remove timeout
	c.conn.SetDeadline(time.Now().Add(c.Timeout))

	if err := req.Write(c.conn); err != nil {
		c.conn = nil
		return nil, err
	}

	resp, err = http.ReadResponse(bufio.NewReader(c.conn), req)
	if err != nil {
		c.conn = nil
		return nil, err
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected response status: %d with body: %s", resp.StatusCode, body)
	}

	return resp, nil
}
