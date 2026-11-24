package clickup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAssignedTasksParametersAndPagination(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "pk_test" || r.URL.Path != "/team/99/task" ||
			q.Get("assignees[]") != "12" || q.Get("subtasks") != "true" || q.Get("page") != "3" {
			t.Errorf("unexpected ClickUp request %v", r.URL)
		}
		fmt.Fprint(w, `{"tasks":[{"id":"a", "name":"First", "parent":null},{"id":"b", "name":"Second", "parent":"a"}],"last_page":false}`)
	}))
	defer host.Close()
	c := &Client{base: host.URL, http: host.Client()}
	tasks, last, err := c.AssignedTasks(context.Background(), "pk_test", "99", "12", 3)
	if err != nil || last || len(tasks) != 2 || tasks[1].Parent != "a" {
		t.Fatalf("unexpected tasks %+v, last %t, error %v", tasks, last, err)
	}
}

func TestTimeEntryMarkerAndRateLimit(t *testing.T) {
	entry := TimeEntry{TaskID: "task", Start: 10000, Duration: 5000, Description: "Antitimely ref 123"}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/99/time_entries" {
			t.Errorf("unexpected path %s", r.URL)
		}
		if r.Method == http.MethodPost {
			var input TimeEntry
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input != entry {
				t.Errorf("unexpected entry %+v, %v", input, err)
			}
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Get("task_id") != "task" {
			t.Errorf("wrong reconciliation query %s", r.URL)
		}
		fmt.Fprint(w, `{"data":[{"id":"other","description":"Another entry"},{"id":456,"description":"Antitimely ref 123"}]}`)
	}))
	defer host.Close()
	c := &Client{base: host.URL, http: host.Client()}
	_, err := c.CreateTimeEntry(context.Background(), "pk_test", "99", entry)
	var problem *HTTPError
	if !errors.As(err, &problem) || problem.Status != 429 || problem.RetryAfter != 3*time.Second {
		t.Fatalf("unexpected rate limit %v", err)
	}
	id, err := c.FindTimeEntry(context.Background(), "pk_test", "99", entry)
	if err != nil || id != "456" {
		t.Fatalf("unexpected reconciliation %q, %v", id, err)
	}
}

func TestTaskDetailsOnlyReadsCommentsForAssignedTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing provider authorization")
		}
		switch r.URL.Path {
		case "/task/assigned":
			fmt.Fprint(w, `{"name":"Review imports","assignees":[{"id":52}],"status":{"status":"in progress"},"priority":{"priority":"high"},"due_date":"1795478400000","attachments":[{"title":"error.PNG","url":"https://attachments.clickup.com/error.PNG"}]}`)
		case "/task/assigned/comment":
			fmt.Fprint(w, `{"comments":[{"comment_text":"Missing a column","user":{"username":"Ada"}}]}`)
		case "/task/unassigned":
			fmt.Fprint(w, `{"name":"Other work","assignees":[{"id":53}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := &Client{base: server.URL, http: server.Client()}
	details, err := c.TaskDetails(context.Background(), "fixture", "52", "assigned")
	if err != nil || details.Status != "in progress" || details.DueUnixMillis != 1795478400000 ||
		len(details.Attachments) != 1 || !details.Attachments[0].Image || len(details.Comments) != 1 || details.Comments[0].Author != "Ada" {
		t.Fatalf("unexpected details %+v: %v", details, err)
	}
	_, err = c.TaskDetails(context.Background(), "fixture", "52", "unassigned")
	var denied *HTTPError
	if !errors.As(err, &denied) || denied.Status != http.StatusForbidden {
		t.Fatalf("unassigned task was not denied: %v", err)
	}
}
