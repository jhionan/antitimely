// Package clickup adapts the ClickUp API behind the tracker RPC server.
package clickup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const api = "https://api.clickup.com/api/v2"

type Client struct {
	base string
	http *http.Client
}

func New() *Client {
	return &Client{base: api, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("ClickUp HTTP %d", e.Status) }

type ID string

func (id *ID) UnmarshalJSON(data []byte) error {
	var decoded string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
	} else {
		n, err := strconv.ParseInt(string(data), 10, 64)
		if err != nil {
			return errors.New("invalid ClickUp identifier")
		}
		decoded = strconv.FormatInt(n, 10)
	}
	*id = ID(decoded)
	return nil
}

type User struct {
	ID       ID     `json:"id"`
	Username string `json:"username"`
}

type Workspace struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
}

type Task struct {
	ID     ID     `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Parent ID     `json:"-"`
}

func (c *Client) User(ctx context.Context, token string) (User, error) {
	var reply struct{ User User }
	err := c.request(ctx, http.MethodGet, "/user", token, nil, &reply)
	if err == nil && reply.User.ID == "" {
		err = errors.New("ClickUp returned no user ID")
	}
	return reply.User, err
}

func (c *Client) Workspaces(ctx context.Context, token string) ([]Workspace, error) {
	var reply struct{ Teams []Workspace }
	err := c.request(ctx, http.MethodGet, "/team", token, nil, &reply)
	return reply.Teams, err
}

func (c *Client) AssignedTasks(ctx context.Context, token string, workspace, user ID, page int) ([]Task, bool, error) {
	if !decimalID(workspace) || !decimalID(user) || page < 0 {
		return nil, false, errors.New("invalid ClickUp workspace, user, or page")
	}
	query := url.Values{
		"assignees[]": {string(user)}, "subtasks": {"true"},
		"include_closed": {"false"}, "page": {strconv.Itoa(page)},
	}
	var reply struct {
		Tasks []struct {
			Task
			Parent *ID `json:"parent"`
		}
		LastPage *bool `json:"last_page"`
	}
	if err := c.request(ctx, http.MethodGet, "/team/"+string(workspace)+"/task?"+query.Encode(), token, nil, &reply); err != nil {
		return nil, false, err
	}
	tasks := make([]Task, 0, len(reply.Tasks))
	for _, item := range reply.Tasks {
		task := item.Task
		if item.Parent != nil {
			task.Parent = *item.Parent
		}
		tasks = append(tasks, task)
	}
	last := len(tasks) < 100
	if reply.LastPage != nil {
		last = *reply.LastPage || len(tasks) == 0
	}
	return tasks, last, nil
}

type TimeEntry struct {
	TaskID      string `json:"tid"`
	Start       int64  `json:"start"`
	Duration    int64  `json:"duration"`
	Description string `json:"description"`
}

func (c *Client) CreateTimeEntry(ctx context.Context, token string, workspace ID, entry TimeEntry) (ID, error) {
	if !decimalID(workspace) || entry.TaskID == "" || entry.Start <= 0 || entry.Duration <= 0 || entry.Description == "" {
		return "", errors.New("invalid ClickUp time entry")
	}
	var reply struct {
		Data struct{ ID ID }
	}
	if err := c.request(ctx, http.MethodPost, "/team/"+string(workspace)+"/time_entries", token, entry, &reply); err != nil {
		return "", err
	}
	if reply.Data.ID == "" {
		return "", errors.New("ClickUp did not return a time entry ID")
	}
	return reply.Data.ID, nil
}

// FindTimeEntry looks for the exact marker attached by Antitimely. A missing
// marker does not prove that a prior timed-out creation failed.
func (c *Client) FindTimeEntry(ctx context.Context, token string, workspace ID, entry TimeEntry) (ID, error) {
	if !decimalID(workspace) || entry.TaskID == "" || entry.Start <= 0 || entry.Duration <= 0 || entry.Description == "" {
		return "", errors.New("invalid ClickUp time entry")
	}
	query := url.Values{
		"start_date": {strconv.FormatInt(entry.Start-1, 10)},
		"end_date":   {strconv.FormatInt(entry.Start+entry.Duration+1, 10)},
		"task_id":    {entry.TaskID},
	}
	var reply struct {
		Data []struct {
			ID          ID
			Description string
		}
	}
	if err := c.request(ctx, http.MethodGet, "/team/"+string(workspace)+"/time_entries?"+query.Encode(), token, nil, &reply); err != nil {
		return "", err
	}
	var found ID
	for _, candidate := range reply.Data {
		if candidate.Description == entry.Description && candidate.ID != "" {
			if found != "" && found != candidate.ID {
				return "", errors.New("multiple ClickUp time entries carry this marker")
			}
			found = candidate.ID
		}
	}
	return found, nil
}

func decimalID(id ID) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (c *Client) request(ctx context.Context, method, path, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if strings.HasPrefix(token, "pk_") {
		req.Header.Set("Authorization", token)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{Status: res.StatusCode, RetryAfter: retryAfter(res.Header.Get("Retry-After"))}
	}
	const maxBytes = 16 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxBytes {
		return errors.New("ClickUp response is too large")
	}
	return json.Unmarshal(data, output)
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(until))
	}
	return 0
}

func (c *Client) Exchange(ctx context.Context, clientID, secret, code string) (string, error) {
	var reply struct {
		AccessToken string `json:"access_token"`
	}
	err := c.request(ctx, http.MethodPost, "/oauth/token", "", map[string]string{
		"client_id": clientID, "client_secret": secret, "code": code,
	}, &reply)
	if err == nil && reply.AccessToken == "" {
		err = errors.New("ClickUp did not return an access token")
	}
	return reply.AccessToken, err
}
