package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

type usageWindow struct {
	UsedPercent       int   `json:"used_percent"`
	LimitWindowSecond int64 `json:"limit_window_seconds"`
	ResetAt           int64 `json:"reset_at"`
}

type rateLimitDetails struct {
	Primary   *usageWindow `json:"primary_window"`
	Secondary *usageWindow `json:"secondary_window"`
}

type additionalLimit struct {
	Name           string            `json:"limit_name"`
	MeteredFeature string            `json:"metered_feature"`
	RateLimit      *rateLimitDetails `json:"rate_limit"`
}

type usageResponse struct {
	PlanType             string            `json:"plan_type"`
	RateLimit            *rateLimitDetails `json:"rate_limit"`
	AdditionalRateLimits []additionalLimit `json:"additional_rate_limits"`
	Credits              *struct {
		HasCredits bool            `json:"has_credits"`
		Unlimited  bool            `json:"unlimited"`
		Balance    json.RawMessage `json:"balance"`
	} `json:"credits"`
	SpendControl *struct {
		IndividualLimit *struct {
			Limit     string `json:"limit"`
			Used      string `json:"used"`
			Remaining string `json:"remaining"`
			ResetAt   int64  `json:"reset_at"`
		} `json:"individual_limit"`
	} `json:"spend_control"`
}

func (a *Adapter) Usage(ctx context.Context, resolved providerAuth.ResolvedAuth) (providerAuth.UsageSnapshot, error) {
	a.defaults()
	accountID := resolved.Properties["account_id"]
	if resolved.Secret == "" || accountID == "" {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("OpenAI usage requires a resolved subscription credential")
	}
	endpoint := strings.TrimRight(a.ChatBaseURL, "/") + "/wham/usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerAuth.UsageSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+resolved.Secret)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-cli")
	resp, err := a.Client.Do(req)
	if err != nil {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("fetch OpenAI subscription usage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return providerAuth.UsageSnapshot{}, fmt.Errorf("fetch OpenAI subscription usage: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("read OpenAI subscription usage")
	}
	if len(data) > maxResponseBytes {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("OpenAI subscription usage response is too large")
	}
	var raw usageResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("decode OpenAI subscription usage")
	}
	if raw.PlanType == "" {
		return providerAuth.UsageSnapshot{}, fmt.Errorf("OpenAI subscription usage has no plan type")
	}
	snapshot := providerAuth.UsageSnapshot{Provider: providerID, Plan: raw.PlanType, FetchedAt: a.Now()}
	appendWindows := func(prefix string, details *rateLimitDetails) error {
		if details == nil {
			return nil
		}
		for _, item := range []struct {
			name   string
			window *usageWindow
		}{{"primary", details.Primary}, {"secondary", details.Secondary}} {
			if item.window == nil {
				continue
			}
			if item.window.UsedPercent < 0 || item.window.UsedPercent > 100 || item.window.LimitWindowSecond < 0 || item.window.ResetAt < 0 {
				return fmt.Errorf("OpenAI subscription usage contains an invalid %s window", item.name)
			}
			used := item.window.UsedPercent
			limit := providerAuth.UsageLimit{Name: strings.TrimSpace(prefix + " " + item.name), UsedPercent: &used}
			if item.window.LimitWindowSecond > 0 {
				limit.Window = time.Duration(item.window.LimitWindowSecond) * time.Second
			}
			if item.window.ResetAt > 0 {
				reset := time.Unix(item.window.ResetAt, 0)
				limit.ResetsAt = &reset
			}
			snapshot.Limits = append(snapshot.Limits, limit)
		}
		return nil
	}
	if err := appendWindows("Codex", raw.RateLimit); err != nil {
		return providerAuth.UsageSnapshot{}, err
	}
	for _, extra := range raw.AdditionalRateLimits {
		name := extra.Name
		if name == "" {
			name = extra.MeteredFeature
		}
		if err := appendWindows(name, extra.RateLimit); err != nil {
			return providerAuth.UsageSnapshot{}, err
		}
	}
	if raw.Credits != nil {
		remaining := "none"
		if raw.Credits.Unlimited {
			remaining = "unlimited"
		} else if len(raw.Credits.Balance) > 0 && string(raw.Credits.Balance) != "null" {
			var text string
			if json.Unmarshal(raw.Credits.Balance, &text) != nil {
				var number json.Number
				if json.Unmarshal(raw.Credits.Balance, &number) == nil {
					text = number.String()
				}
			}
			if text != "" {
				remaining = text
			}
		} else if raw.Credits.HasCredits {
			remaining = "available"
		}
		snapshot.Limits = append(snapshot.Limits, providerAuth.UsageLimit{Name: "Credits", Remaining: remaining})
	}
	if raw.SpendControl != nil && raw.SpendControl.IndividualLimit != nil {
		individual := raw.SpendControl.IndividualLimit
		limit := providerAuth.UsageLimit{Name: "Spend control", Used: individual.Used, Limit: individual.Limit, Remaining: individual.Remaining}
		if individual.ResetAt > 0 {
			reset := time.Unix(individual.ResetAt, 0)
			limit.ResetsAt = &reset
		}
		if individual.Limit != "" && individual.Used != "" {
			if total, err1 := strconv.ParseFloat(individual.Limit, 64); err1 == nil && total > 0 {
				if used, err2 := strconv.ParseFloat(individual.Used, 64); err2 == nil {
					percent := int(used / total * 100)
					limit.UsedPercent = &percent
				}
			}
		}
		snapshot.Limits = append(snapshot.Limits, limit)
	}
	return snapshot, nil
}
