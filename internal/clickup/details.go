package clickup

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type TaskDetails struct {
	Name, Description, Status, Priority, URL string
	DueUnixMillis                            int64
	Attachments                              []Attachment
	Comments                                 []Comment
}

type Attachment struct {
	Name, URL string
	Image     bool
}

type Comment struct {
	Author, Text string
}

func (c *Client) TaskDetails(ctx context.Context, token string, user ID, id string) (TaskDetails, error) {
	endpoint := "/task/" + url.PathEscape(id)
	var task struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Due         string `json:"due_date"`
		Status      struct {
			Text string `json:"status"`
		} `json:"status"`
		Priority struct {
			Text string `json:"priority"`
		} `json:"priority"`
		Assignees []struct {
			ID ID `json:"id"`
		} `json:"assignees"`
		Attachments []struct {
			URL       string `json:"url"`
			Name      string `json:"title"`
			Type      string `json:"mimetype"`
			Extension string `json:"extension"`
		} `json:"attachments"`
	}
	if err := c.request(ctx, http.MethodGet, endpoint, token, nil, &task); err != nil {
		return TaskDetails{}, err
	}
	assigned := false
	for _, assignee := range task.Assignees {
		assigned = assigned || (user != "" && assignee.ID == user)
	}
	if !assigned {
		return TaskDetails{}, &HTTPError{Status: http.StatusForbidden}
	}

	due, _ := strconv.ParseInt(task.Due, 10, 64)
	out := TaskDetails{Name: task.Name, Description: task.Description, Status: task.Status.Text, Priority: task.Priority.Text, DueUnixMillis: due, URL: task.URL}
	for _, a := range task.Attachments {
		if a.URL == "" {
			continue
		}
		ext := strings.ToLower(a.Extension)
		if ext == "" {
			ext = strings.TrimPrefix(strings.ToLower(path.Ext(a.Name)), ".")
		}
		image := strings.HasPrefix(a.Type, "image/") || ext == "png" || ext == "jpg" || ext == "jpeg" || ext == "gif" || ext == "webp"
		out.Attachments = append(out.Attachments, Attachment{Name: a.Name, URL: a.URL, Image: image})
	}
	var reply struct {
		Comments []struct {
			Text string `json:"comment_text"`
			User struct {
				Name string `json:"username"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := c.request(ctx, http.MethodGet, endpoint+"/comment", token, nil, &reply); err != nil {
		return TaskDetails{}, err
	}
	for _, comment := range reply.Comments {
		out.Comments = append(out.Comments, Comment{Author: comment.User.Name, Text: comment.Text})
	}
	return out, nil
}
