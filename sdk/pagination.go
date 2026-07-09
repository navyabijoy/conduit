package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Paginator is the interface for iterating through paginated resources.
type Paginator interface {
	HasNext() bool
	Next(ctx context.Context, client *HTTPClient) ([]byte, error)
}

// OffsetPaginator handles offset-based paging.
type OffsetPaginator struct {
	Path        string
	Limit       int
	OffsetParam string
	LimitParam  string
	offset      int
	hasMore     bool
	isFirst     bool
}

// NewOffsetPaginator creates an offset-based paginator.
func NewOffsetPaginator(path string, limit int, offsetParam, limitParam string) *OffsetPaginator {
	return &OffsetPaginator{
		Path:        path,
		Limit:       limit,
		OffsetParam: offsetParam,
		LimitParam:  limitParam,
		offset:      0,
		hasMore:     true,
		isFirst:     true,
	}
}

// HasNext returns true if there are potentially more pages.
func (p *OffsetPaginator) HasNext() bool {
	return p.hasMore
}

// Next fetches the next page of results.
func (p *OffsetPaginator) Next(ctx context.Context, client *HTTPClient) ([]byte, error) {
	if !p.hasMore {
		return nil, fmt.Errorf("no more pages")
	}

	reqURL, err := url.Parse(p.Path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	q := reqURL.Query()
	q.Set(p.OffsetParam, strconv.Itoa(p.offset))
	q.Set(p.LimitParam, strconv.Itoa(p.Limit))
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Try to determine if there are more items by checking the number of returned items.
	var list []interface{}
	if err := json.Unmarshal(body, &list); err == nil {
		p.offset += len(list)
		p.hasMore = len(list) >= p.Limit
	} else {
		var obj map[string]interface{}
		if err := json.Unmarshal(body, &obj); err == nil {
			foundArray := false
			for _, val := range obj {
				if subList, ok := val.([]interface{}); ok {
					p.offset += len(subList)
					p.hasMore = len(subList) >= p.Limit
					foundArray = true
					break
				}
			}
			if !foundArray {
				p.hasMore = false
			}
		} else {
			p.hasMore = false
		}
	}

	p.isFirst = false
	return body, nil
}

// CursorPaginator handles cursor-based paging.
type CursorPaginator struct {
	Path        string
	CursorParam string
	LimitParam  string
	Limit       int
	nextCursor  string
	hasMore     bool
	isFirst     bool
	extractNext func(body []byte, respHeaders http.Header) (string, bool, error)
}

// NewCursorPaginator creates a cursor-based paginator.
func NewCursorPaginator(
	path string,
	cursorParam string,
	limitParam string,
	limit int,
	extractNext func(body []byte, respHeaders http.Header) (string, bool, error),
) *CursorPaginator {
	return &CursorPaginator{
		Path:        path,
		CursorParam: cursorParam,
		LimitParam:  limitParam,
		Limit:       limit,
		nextCursor:  "",
		hasMore:     true,
		isFirst:     true,
		extractNext: extractNext,
	}
}

// HasNext returns true if there are potentially more pages.
func (p *CursorPaginator) HasNext() bool {
	return p.hasMore
}

// Next fetches the next page of results.
func (p *CursorPaginator) Next(ctx context.Context, client *HTTPClient) ([]byte, error) {
	if !p.hasMore {
		return nil, fmt.Errorf("no more pages")
	}

	reqURL, err := url.Parse(p.Path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	q := reqURL.Query()
	if !p.isFirst && p.nextCursor != "" {
		q.Set(p.CursorParam, p.nextCursor)
	}
	if p.Limit > 0 && p.LimitParam != "" {
		q.Set(p.LimitParam, strconv.Itoa(p.Limit))
	}
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	cursor, hasMore, err := p.extractNext(body, resp.Header)
	if err != nil {
		return nil, fmt.Errorf("failed to extract next cursor: %w", err)
	}

	p.nextCursor = cursor
	p.hasMore = hasMore && cursor != ""
	p.isFirst = false

	return body, nil
}
