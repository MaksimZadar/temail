package llm

import (
	"context"
	"errors"
	"slices"

	"github.com/openai/openai-go/v3"
)

// maxModels caps the list ListModels returns, so a hostile or huge listing
// cannot grow memory without limit.
const maxModels = 500

// ListModels returns the model IDs the endpoint lists, sorted. An endpoint that
// does not implement the models route (404, 405 or 501) gives an empty list and
// ErrModelListUnsupported, so the caller can fall back to a typed model name.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if err := c.checkEnabled(); err != nil {
		return []string{}, err
	}
	pager := c.sdk.Models.ListAutoPaging(ctx)
	ids := []string{}
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
		if len(ids) >= maxModels {
			break
		}
	}
	if err := pager.Err(); err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404, 405, 501:
				return []string{}, ErrModelListUnsupported
			}
		}
		return []string{}, wrapCall(err)
	}
	slices.Sort(ids)
	return ids, nil
}
