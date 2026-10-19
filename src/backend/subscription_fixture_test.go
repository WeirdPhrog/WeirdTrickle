package magitrickle

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Application transaction/backoff tests inject their local fixture downloader.
// The production downloader rejects localhost; its address, redirect, interface
// and fallback policies are tested in subscriptions and isolated kernel tests.
func fetchSubscriptionFixture(ctx context.Context, rawURL, _ string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fixture status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	return string(body), err
}
