package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
}

func NewClient(endpoint, token, version string) *Client {
	return &Client{
		endpoint:  strings.TrimRight(endpoint, "/"),
		token:     token,
		userAgent: "terraform-provider-mergify/" + version,
		http:      &http.Client{},
	}
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

type Repository struct {
	Name            string   `json:"name"`
	EnabledProducts []string `json:"enabled_products"`
}

type listRepositoriesResponse struct {
	Repositories []Repository `json:"repositories"`
}

func (c *Client) GetRepositoryProducts(ctx context.Context, owner, repository string) (products []string, found bool, err error) {
	var resp listRepositoriesResponse
	if err := c.do(ctx, http.MethodGet, "/repos/"+owner, nil, &resp); err != nil {
		return nil, false, err
	}
	for _, r := range resp.Repositories {
		if r.Name == repository {
			return r.EnabledProducts, true, nil
		}
	}
	return nil, false, nil
}

type setProductsRequest struct {
	Products []string `json:"products"`
}

func (c *Client) SetRepositoryProducts(ctx context.Context, owner, repository string, products []string) error {
	if products == nil {
		products = []string{}
	}
	return c.do(ctx, http.MethodPut, "/products/"+owner+"/"+repository, setProductsRequest{Products: products}, nil)
}

type defaultProductsResponse struct {
	Products []string `json:"products"`
}

func (c *Client) GetDefaultProducts(ctx context.Context, owner string) ([]string, error) {
	var resp defaultProductsResponse
	if err := c.do(ctx, http.MethodGet, "/default_products/"+owner, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Products, nil
}

func (c *Client) SetDefaultProducts(ctx context.Context, owner string, products []string) error {
	if products == nil {
		products = []string{}
	}
	return c.do(ctx, http.MethodPut, "/default_products/"+owner, setProductsRequest{Products: products}, nil)
}

type SlackChannel struct {
	ID             string  `json:"id"`
	SlackChannel   string  `json:"slack_channel"`
	SlackTeamName  *string `json:"slack_team_name"`
	DisabledAt     *string `json:"disabled_at"`
	DisabledReason *string `json:"disabled_reason"`
	// Raw configurations keyed by the response field holding a product's
	// configurations, e.g. `merge_queue_configs`. Decoded on demand, so a
	// product this provider does not manage cannot break the listing.
	Configs map[string]json.RawMessage `json:"-"`
}

func (ch *SlackChannel) UnmarshalJSON(data []byte) error {
	type plain SlackChannel
	if err := json.Unmarshal(data, (*plain)(ch)); err != nil {
		return err
	}
	return json.Unmarshal(data, &ch.Configs)
}

// Notification returns the configuration `id` held in the `responseKey`
// field of the channel, or nil when there is none.
func (ch *SlackChannel) Notification(responseKey, id string) (*SlackNotification, error) {
	raw, ok := ch.Configs[responseKey]
	if !ok {
		return nil, nil
	}
	var configs []SlackNotification
	if err := json.Unmarshal(raw, &configs); err != nil {
		return nil, fmt.Errorf("decode %s: %w", responseKey, err)
	}
	for i := range configs {
		if strings.EqualFold(configs[i].ID, id) {
			return &configs[i], nil
		}
	}
	return nil, nil
}

// SlackNotification is a notification configuration of any product: the
// fields besides `id` and `repositories` that hold a list of strings are the
// product's filters, and which ones exist depends on the product.
type SlackNotification struct {
	ID           string
	Repositories []string
	Filters      map[string][]string
}

func (n *SlackNotification) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	n.Filters = map[string][]string{}
	for key, raw := range fields {
		switch key {
		case "id":
			if err := json.Unmarshal(raw, &n.ID); err != nil {
				return fmt.Errorf("decode id: %w", err)
			}
		case "repositories":
			var repositories []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &repositories); err != nil {
				return fmt.Errorf("decode repositories: %w", err)
			}
			n.Repositories = make([]string, 0, len(repositories))
			for _, r := range repositories {
				n.Repositories = append(n.Repositories, r.Name)
			}
		default:
			// Any other shape is a field the provider does not manage.
			var values []string
			if json.Unmarshal(raw, &values) == nil {
				n.Filters[key] = values
			}
		}
	}
	return nil
}

type slackConfigurationResponse struct {
	Channels []SlackChannel `json:"channels"`
}

func slackPath(owner string, segments ...string) string {
	path := "/integrations/" + url.PathEscape(owner) + "/configuration/slack"
	for _, s := range segments {
		path += "/" + url.PathEscape(s)
	}
	return path
}

func (c *Client) ListSlackChannels(ctx context.Context, owner string) ([]SlackChannel, error) {
	var resp slackConfigurationResponse
	if err := c.do(ctx, http.MethodGet, slackPath(owner), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Channels, nil
}

// SlackNotificationBody is the request body of a notification configuration:
// `repositories` plus the product's filters, each a list of strings.
type SlackNotificationBody map[string][]string

func (c *Client) CreateSlackNotification(ctx context.Context, owner, channelID, product string, body SlackNotificationBody) (*SlackNotification, error) {
	var resp SlackNotification
	if err := c.do(ctx, http.MethodPost, slackPath(owner, channelID, product), body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) UpdateSlackNotification(ctx context.Context, owner, channelID, product, id string, body SlackNotificationBody) error {
	return c.do(ctx, http.MethodPut, slackPath(owner, channelID, product, id), body, nil)
}

func (c *Client) DeleteSlackNotification(ctx context.Context, owner, channelID, product, id string) error {
	return c.do(ctx, http.MethodDelete, slackPath(owner, channelID, product, id), nil, nil)
}
