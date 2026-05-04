package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
