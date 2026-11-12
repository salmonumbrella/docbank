// Package docusealclient implements the bounded DocuSeal connection.
package docusealclient

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/docbank/document/providerhttp"
	"go.kenn.io/docbank/internal/config"
)

const maxResponse = 2 << 20

var errResponseTooLarge = errors.New("docuseal_response_too_large")

type Status struct {
	Endpoint string `json:"endpoint"`
}
type Template struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}
type TemplatePage struct {
	Templates []Template `json:"templates"`
	Next      int64      `json:"next"`
}
type Client struct {
	connection config.DocuSealConnection
	http       *http.Client
}

func New(c config.DocuSealConnection) (*Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid DocuSeal endpoint")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return nil, errors.New("invalid DocuSeal endpoint")
	}
	transport, err := providerhttp.NewTransport(providerhttp.EgressPolicy{Scheme: u.Scheme, Host: u.Hostname(), Port: uint16(n), AllowedCIDRs: c.AllowedCIDRs, ProxyMode: providerhttp.ProxyDisabled}, nil)
	if err != nil {
		return nil, errors.New("invalid DocuSeal egress policy")
	}
	return &Client{connection: c, http: providerhttp.IsolateClient(&http.Client{Transport: transport})}, nil
}

// CloseIdleConnections releases the request-owned transport.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

func (c *Client) get(ctx context.Context, suffix string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.connection.Endpoint+c.connection.APIPath+suffix, nil)
	if err != nil {
		return errors.New("docuseal_request_failed")
	}
	req.Header.Set("X-Auth-Token", c.connection.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("docuseal_request_failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("docuseal_unauthorized")
		case http.StatusNotFound:
			return errors.New("docuseal_not_found")
		default:
			return errors.New("docuseal_provider_error")
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("docuseal_response_failed")
	}
	if len(data) > maxResponse {
		return errResponseTooLarge
	}

	if json.Unmarshal(data, dst) != nil {
		return errors.New("docuseal_invalid_response")
	}
	return nil
}
func (c *Client) Check(ctx context.Context) (Status, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := c.get(ctx, "/user", &user); err != nil {
		return Status{}, err
	}
	if user.ID <= 0 {
		return Status{}, errors.New("docuseal_invalid_response")
	}
	endpoint := c.connection.Endpoint
	if strings.Contains(endpoint, c.connection.APIKey) {
		endpoint = "*"
		if c.connection.APIKey == "*" {
			endpoint = "?"
		}
	}
	return Status{Endpoint: endpoint}, nil
}
func (c *Client) Templates(ctx context.Context, cursor int64) (TemplatePage, error) {
	if cursor < 0 {
		return TemplatePage{}, errors.New("invalid cursor")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var wire struct {
		Data *[]struct {
			ID         int64   `json:"id"`
			Name       *string `json:"name"`
			Submitters []struct {
				Name string `json:"name"`
			} `json:"submitters"`
		} `json:"data"`
		Pagination struct {
			Next *int64 `json:"next"`
		} `json:"pagination"`
	}
	limit := 100
	for {
		suffix := fmt.Sprintf("/templates?limit=%d", limit)
		if cursor > 0 {
			suffix += fmt.Sprintf("&after=%d", cursor)
		}
		err := c.get(ctx, suffix, &wire)
		if errors.Is(err, errResponseTooLarge) && limit > 1 {
			limit /= 2
			continue
		}
		if err != nil {
			return TemplatePage{}, err
		}
		break
	}
	if wire.Data == nil || len(*wire.Data) > limit {
		return TemplatePage{}, errors.New("docuseal_invalid_response")
	}
	page := TemplatePage{Templates: make([]Template, 0, len(*wire.Data))}
	if len(*wire.Data) == limit {
		last := (*wire.Data)[len(*wire.Data)-1].ID
		if wire.Pagination.Next == nil || *wire.Pagination.Next != last || last <= 0 || cursor > 0 && last >= cursor {
			return TemplatePage{}, errors.New("docuseal_invalid_response")
		}
		page.Next = last
	}
	var previous int64
	for _, t := range *wire.Data {
		if t.ID <= 0 || cursor > 0 && t.ID >= cursor || previous > 0 && t.ID >= previous {
			return TemplatePage{}, errors.New("docuseal_invalid_response")
		}
		previous = t.ID
		if t.Name == nil || t.Submitters == nil {
			return TemplatePage{}, errors.New("docuseal_invalid_response")
		}
		name := displayText(*t.Name)
		if strings.Contains(*t.Name, c.connection.APIKey) || strings.Contains(name, c.connection.APIKey) {
			return TemplatePage{}, errors.New("docuseal_invalid_response")
		}
		item := Template{ID: t.ID, Name: name, Roles: make([]string, 0, len(t.Submitters))}
		for _, role := range t.Submitters {
			if strings.Contains(role.Name, c.connection.APIKey) {
				return TemplatePage{}, errors.New("docuseal_invalid_response")
			}
			item.Roles = append(item.Roles, role.Name)
		}
		page.Templates = append(page.Templates, item)
	}
	if !ValidTemplatePage(page) {
		return TemplatePage{}, errors.New("docuseal_invalid_response")
	}
	return page, nil
}

// ValidTemplatePage validates the projection at each trust boundary.
func ValidTemplatePage(page TemplatePage) bool {
	if page.Templates == nil || len(page.Templates) > 100 || page.Next < 0 {
		return false
	}
	for _, item := range page.Templates {
		if item.ID <= 0 || !safeText(item.Name, 512) || item.Roles == nil || len(item.Roles) > 1024 {
			return false
		}
		for _, role := range item.Roles {
			if !safeText(role, 4096) {
				return false
			}
		}
	}
	return true
}

func displayText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		if b.Len()+utf8.RuneLen(r) > 512 {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "(unnamed)"
	}
	return b.String()
}

func safeText(s string, limit int) bool {
	if s == "" || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type SubmitterInput struct {
	Role       string `json:"role"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	ExternalID string `json:"external_id"`
}
type SubmissionReceipt struct {
	SubmissionID string
	SubmitterIDs []string
}

var ErrCreationUnknown = errors.New("docuseal_creation_unknown")
var ErrCreationFailed = errors.New("docuseal_creation_failed")

type templateWire struct {
	ID         int64   `json:"id"`
	Name       *string `json:"name"`
	Submitters []struct {
		Name string `json:"name"`
	} `json:"submitters"`
}

func (c *Client) Template(ctx context.Context, id int64) (Template, error) {
	if id <= 0 {
		return Template{}, errors.New("invalid template ID")
	}
	var wire templateWire
	if err := c.get(ctx, fmt.Sprintf("/templates/%d", id), &wire); err != nil {
		return Template{}, err
	}
	if wire.ID != id || wire.Name == nil || wire.Submitters == nil {
		return Template{}, errors.New("docuseal_invalid_response")
	}
	name := displayText(*wire.Name)
	if strings.Contains(*wire.Name, c.connection.APIKey) || strings.Contains(name, c.connection.APIKey) {
		return Template{}, errors.New("docuseal_invalid_response")
	}
	item := Template{ID: id, Name: name, Roles: make([]string, 0, len(wire.Submitters))}
	for _, r := range wire.Submitters {
		if strings.Contains(r.Name, c.connection.APIKey) {
			return Template{}, errors.New("docuseal_invalid_response")
		}
		item.Roles = append(item.Roles, r.Name)
	}
	if !ValidTemplatePage(TemplatePage{Templates: []Template{item}}) {
		return Template{}, errors.New("docuseal_invalid_response")
	}
	return item, nil
}
func (c *Client) CreateSubmission(ctx context.Context, id int64, signers []SubmitterInput) (SubmissionReceipt, error) {
	body, err := json.Marshal(struct {
		TemplateID int64            `json:"template_id"`
		SendEmail  bool             `json:"send_email"`
		Submitters []SubmitterInput `json:"submitters"`
	}{id, true, signers})
	if err != nil {
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.connection.Endpoint+c.connection.APIPath+"/submissions", bytes.NewReader(body))
	if err != nil {
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	req.Header.Set("X-Auth-Token", c.connection.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout {
			return SubmissionReceipt{}, ErrCreationFailed
		}
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	var wire []struct {
		ID           *int64  `json:"id"`
		SubmissionID *int64  `json:"submission_id"`
		Role         *string `json:"role"`
		ExternalID   *string `json:"external_id"`
	}
	if json.Unmarshal(data, &wire) != nil || len(wire) != len(signers) {
		return SubmissionReceipt{}, ErrCreationUnknown
	}
	expected := map[string]int{}
	for i, s := range signers {
		if _, ok := expected[s.ExternalID]; ok {
			return SubmissionReceipt{}, ErrCreationUnknown
		}
		expected[s.ExternalID] = i
	}
	result := SubmissionReceipt{SubmitterIDs: make([]string, len(signers))}
	seen := map[int64]bool{}
	var submission int64
	for _, s := range wire {
		if s.ID == nil || *s.ID <= 0 || s.SubmissionID == nil || *s.SubmissionID <= 0 || s.Role == nil || s.ExternalID == nil || seen[*s.ID] {
			return SubmissionReceipt{}, ErrCreationUnknown
		}
		i, ok := expected[*s.ExternalID]
		if !ok || result.SubmitterIDs[i] != "" || *s.Role != signers[i].Role || (submission != 0 && submission != *s.SubmissionID) {
			return SubmissionReceipt{}, ErrCreationUnknown
		}
		submission = *s.SubmissionID
		seen[*s.ID] = true
		result.SubmitterIDs[i] = strconv.FormatInt(*s.ID, 10)
	}
	result.SubmissionID = strconv.FormatInt(submission, 10)
	return result, nil
}
