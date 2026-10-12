package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloud"
)

// cloudUserProvider keeps the shared protocol error intact while giving the
// chat UI the current DataTug allowance and an actionable next step.
type cloudUserProvider struct{ *cloud.Client }

func (p cloudUserProvider) Stream(ctx context.Context, req ai.ChatRequest) iter.Seq2[ai.Event, error] {
	return func(yield func(ai.Event, error) bool) {
		for event, err := range p.Client.Stream(ctx, req) {
			if err != nil {
				var aiErr *ai.Error
				if errors.As(err, &aiErr) {
					display := formatCloudRefusal(aiErr)
					event.Error = display
					yield(event, display)
					return
				}
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

type cloudLimit struct {
	V            int    `json:"v"`
	Reason       string `json:"reason"`
	AccountTitle string `json:"accountTitle"`
	Used         *int64 `json:"used"`
	Limit        *int64 `json:"limit"`
	Left         *int64 `json:"left"`
	ResetsAt     string `json:"resetsAt"`
	OwnKeyWorks  bool   `json:"ownKeyWorks"`
	CanUpgrade   bool   `json:"canUpgrade"`
	UpgradeURL   string `json:"upgradeUrl"`
}

func formatCloudRefusal(source *ai.Error) *ai.Error {
	copy := *source
	if source.Code == ai.ErrCodeAuth {
		copy.Message = "DataTug sign-in expired; run 'datatug auth login'"
		return &copy
	}
	if source.Code == ai.ErrCodeContextChanged {
		copy.Message = source.Message + " Start a new question for the changed account with a fresh UUID, or use your own AI key."
		return &copy
	}
	var limit cloudLimit
	if json.Unmarshal(source.Details, &limit) != nil || limit.V != 1 || limit.Reason == "" {
		return &copy // unknown versions rely on the server's own sentence
	}
	switch limit.Reason {
	case "monthly", "daily", "free_budget", "unverified", "model_class", "too_large":
		if limit.Used == nil || limit.Limit == nil || limit.Left == nil || *limit.Used < 0 || *limit.Limit < 0 || *limit.Left < 0 {
			return &copy
		}
	default:
		return &copy // Unknown reasons keep the server's sentence unchanged.
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d of %d questions left (%d used)", *limit.Left, *limit.Limit, *limit.Used))
	if limit.ResetsAt != "" {
		parts = append(parts, "resets at "+limit.ResetsAt)
	}
	if limit.Reason == "model_class" {
		parts = append(parts, "choose an allowed hosted model shown by 'datatug plan'")
	}
	if limit.CanUpgrade {
		parts = append(parts, "get a plan at "+limit.UpgradeURL)
	} else if limit.AccountTitle != "" {
		parts = append(parts, "ask an admin of "+limit.AccountTitle)
	}
	if limit.OwnKeyWorks {
		parts = append(parts, "your own AI key still works")
	}
	if len(parts) > 0 {
		copy.Message = source.Message + " " + strings.Join(parts, "; ") + "."
	}
	return &copy
}
