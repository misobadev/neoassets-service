package r2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BucketUsage holds the R2 bucket storage metrics returned by the Cloudflare
// GraphQL Analytics API.
type BucketUsage struct {
	ObjectCount int64
	PayloadSize int64
	MetadataSize int64
}

type cfGraphQLResponse struct {
	Data struct {
		Viewer struct {
			Accounts []struct {
				R2Storage []struct {
					Max struct {
						ObjectCount  float64 `json:"objectCount"`
						PayloadSize  float64 `json:"payloadSize"`
						MetadataSize float64 `json:"metadataSize"`
					} `json:"max"`
				} `json:"r2StorageAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const cfGraphQLQuery = `query($accountTag: String!, $bucketName: String!, $startDate: Time!, $endDate: Time!) {
  viewer {
    accounts(filter: { accountTag: $accountTag }) {
      r2StorageAdaptiveGroups(
        limit: 1
        filter: {
          datetime_geq: $startDate
          datetime_leq: $endDate
          bucketName: $bucketName
        }
      ) {
        max {
          objectCount
          payloadSize
          metadataSize
        }
      }
    }
  }
}`

// GetBucketUsage fetches R2 bucket storage metrics from the Cloudflare
// GraphQL Analytics API. This is a single HTTP request that returns the
// total object count and payload size without listing objects.
func GetBucketUsage(ctx context.Context, accountID, apiToken, bucketName string) (*BucketUsage, error) {
	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	payload := map[string]interface{}{
		"query": cfGraphQLQuery,
		"variables": map[string]string{
			"accountTag": accountID,
			"bucketName": bucketName,
			"startDate":  yesterday.Format(time.RFC3339),
			"endDate":    now.Format(time.RFC3339),
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal graphql payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.cloudflare.com/client/v4/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cf graphql request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cf graphql status %d: %s", resp.StatusCode, string(respBody))
	}

	var gqlResp cfGraphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("cf graphql error: %s", gqlResp.Errors[0].Message)
	}

	accounts := gqlResp.Data.Viewer.Accounts
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no accounts found for %s", accountID)
	}

	buckets := accounts[0].R2Storage
	if len(buckets) == 0 {
		return nil, fmt.Errorf("no storage metrics found for bucket %s", bucketName)
	}

	max := buckets[0].Max
	return &BucketUsage{
		ObjectCount:  int64(max.ObjectCount),
		PayloadSize:  int64(max.PayloadSize),
		MetadataSize: int64(max.MetadataSize),
	}, nil
}
