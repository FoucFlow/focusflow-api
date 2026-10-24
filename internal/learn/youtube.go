package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// VideoDuration returns a YouTube video's length in seconds (0 if unknown).
type VideoDuration func(ctx context.Context, youtubeID string) (int, error)

// YouTubeDuration asks the YouTube Data API (1 quota unit per call).
func YouTubeDuration(apiKey string) VideoDuration {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, id string) (int, error) {
		q := url.Values{"part": {"contentDetails"}, "id": {id}, "key": {apiKey}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/youtube/v3/videos?"+q.Encode(), nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("youtube videos: %s", resp.Status)
		}
		var out struct {
			Items []struct {
				ContentDetails struct {
					Duration string `json:"duration"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return 0, err
		}
		if len(out.Items) == 0 {
			return 0, fmt.Errorf("youtube video %s not found", id)
		}
		return parseISODuration(out.Items[0].ContentDetails.Duration), nil
	}
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// parseISODuration reads YouTube's "PT1H17M3S" (0 if unreadable, e.g. live).
func parseISODuration(s string) int {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return n(1)*86400 + n(2)*3600 + n(3)*60 + n(4)
}
