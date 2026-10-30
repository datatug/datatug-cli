package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cloudPlan struct {
	V             int    `json:"v"`
	Payer         string `json:"payer"`
	AccountID     string `json:"accountId"`
	AccountTitle  string `json:"accountTitle"`
	EffectivePlan string `json:"effectivePlan"`
	CanUpgrade    bool   `json:"canUpgrade"`
	UpgradeURL    string `json:"upgradeUrl"`
	ManageURL     string `json:"manageUrl"`
	OwnKeyWorks   bool   `json:"ownKeyWorks"`
	AI            struct {
		Unit     string  `json:"unit"`
		Enforced bool    `json:"enforced"`
		PeriodID string  `json:"periodId"`
		Used     *int64  `json:"used"`
		Limit    *int64  `json:"limit"`
		Left     *int64  `json:"left"`
		ResetsAt string  `json:"resetsAt"`
		Blocked  *string `json:"blocked"`
		Models   []struct {
			ID      string `json:"id"`
			Class   string `json:"class"`
			Weight  int64  `json:"weight"`
			Default bool   `json:"default"`
		} `json:"models"`
	} `json:"ai"`
}

type planHTTPError struct{ status int }

func (e planHTTPError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "DataTug sign-in expired; run 'datatug auth login'"
	case http.StatusForbidden:
		return "this account is unavailable for your sign-in"
	case http.StatusServiceUnavailable:
		return "DataTug usage is temporarily unavailable; no allowance can be shown"
	default:
		return fmt.Sprintf("DataTug plan is unavailable (HTTP %d)", e.status)
	}
}

func (s cloudSession) getPlan(ctx context.Context, project string) (cloudPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"datatug/plan", nil)
	if err != nil {
		return cloudPlan{}, err
	}
	q := req.URL.Query()
	if project != "" {
		q.Set("project", project)
	}
	req.URL.RawQuery = q.Encode()
	token, err := s.token(ctx)
	if err != nil {
		return cloudPlan{}, fmt.Errorf("DataTug sign-in unavailable: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return cloudPlan{}, fmt.Errorf("DataTug plan request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return cloudPlan{}, planHTTPError{status: resp.StatusCode}
	}
	var plan cloudPlan
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&plan); err != nil {
		return cloudPlan{}, fmt.Errorf("DataTug plan response is unreadable: %w", err)
	}
	if plan.V == 1 && (plan.AI.Used == nil || plan.AI.Limit == nil || plan.AI.Left == nil || *plan.AI.Used < 0 || *plan.AI.Limit < 0 || *plan.AI.Left < 0) {
		return cloudPlan{}, errors.New("DataTug plan response has no valid usage; no allowance can be shown")
	}
	return plan, nil
}

// tokenPreferenceScope scopes a local preference to the Firebase issuer and
// subject. The JWT payload is only a namespace hint, never authorization; the
// server validates the bearer and every payer request. A non-JWT test token is
// scoped to its digest and may lose the preference when refreshed.
func tokenPreferenceScope(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err == nil {
			var claims struct {
				Issuer  string `json:"iss"`
				Subject string `json:"sub"`
			}
			if json.Unmarshal(payload, &claims) == nil && claims.Issuer != "" && claims.Subject != "" {
				token = claims.Issuer + "\x00" + claims.Subject
			}
		}
	}
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (p cloudPlan) hasModel(id string) bool {
	for _, model := range p.AI.Models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func (p cloudPlan) requirePersonalFreeOrPro() error {
	if p.V != 1 {
		return errors.New("this DataTug client cannot verify the current plan version for hosted AI; update the client or use your own AI key")
	}
	if p.Payer != "personal" || (p.EffectivePlan != "free" && p.EffectivePlan != "pro") {
		return errors.New("this DataTug client supports hosted AI for personal Free and Pro plans only; use your own AI key for another account")
	}
	return nil
}

// resolveCloudSelection revalidates saved choices against this login and local
// project before the plan request. The plan then validates the actual payer and
// model. A local project ID is never used as a cloud project hint.
func resolveCloudSelection(ctx context.Context, cmd *cobra.Command, options *chatOptions, session cloudSession) (cloudPlan, error) {
	if options.cloudIdentity != "" && (options.cloudIdentity != session.identity || options.cloudScope != options.project || options.cloudAPI != session.baseURL) {
		if !cmd.Flags().Changed("cloud-model") {
			options.cloudModel = ""
		}
		if !cmd.Flags().Changed("cloud-project") {
			options.cloudProject = ""
		}
	}
	options.cloudIdentity, options.cloudScope, options.cloudAPI = session.identity, options.project, session.baseURL
	plan, err := session.getPlan(ctx, options.cloudProject)
	if err != nil {
		return cloudPlan{}, err
	}
	if err := plan.requirePersonalFreeOrPro(); err != nil {
		return cloudPlan{}, err
	}
	if options.cloudModel != "" && !plan.hasModel(options.cloudModel) {
		return cloudPlan{}, fmt.Errorf("hosted model %q is unavailable for %s; choose a model shown by 'datatug plan' or clear the choice", options.cloudModel, plan.AccountTitle)
	}
	return plan, nil
}

func writePlan(w io.Writer, plan cloudPlan) error {
	if plan.V != 1 {
		_, err := fmt.Fprintln(w, "This DataTug client is out of date; usage is unavailable. Your own AI key still works.")
		return err
	}
	if plan.AI.Used == nil || plan.AI.Limit == nil || plan.AI.Left == nil {
		return errors.New("DataTug plan response has no valid usage; no allowance can be shown")
	}
	if _, err := fmt.Fprintf(w, "%s (%s): %s\n", plan.AccountTitle, plan.AccountID, plan.EffectivePlan); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "AI questions: %d left of %d; %d used in %s; resets %s\n", *plan.AI.Left, *plan.AI.Limit, *plan.AI.Used, plan.AI.PeriodID, plan.AI.ResetsAt); err != nil {
		return err
	}
	if !plan.AI.Enforced {
		if _, err := fmt.Fprintln(w, "AI allowance is currently observed, not enforced."); err != nil {
			return err
		}
	}
	for _, model := range plan.AI.Models {
		if _, err := fmt.Fprintf(w, "  %s (%s, weight %d)%s\n", model.ID, model.Class, model.Weight, map[bool]string{true: " [default]"}[model.Default]); err != nil {
			return err
		}
	}
	if plan.AI.Blocked != nil {
		if _, err := fmt.Fprintf(w, "AI temporarily blocked: %s\n", *plan.AI.Blocked); err != nil {
			return err
		}
	}
	if plan.EffectivePlan == "free" && plan.CanUpgrade && plan.UpgradeURL != "" {
		if _, err := fmt.Fprintf(w, "Get Pro: %s\n", plan.UpgradeURL); err != nil {
			return err
		}
	} else if plan.EffectivePlan == "pro" && plan.ManageURL != "" {
		if _, err := fmt.Fprintf(w, "Manage Pro: %s\n", plan.ManageURL); err != nil {
			return err
		}
	}
	if plan.OwnKeyWorks {
		if _, err := fmt.Fprintln(w, "Your own AI key still works independently of included AI."); err != nil {
			return err
		}
	}
	return nil
}

func planCommand() *cobra.Command {
	var options chatOptions
	cmd := &cobra.Command{Use: "plan", Short: "Show the current DataTug plan and AI usage", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		session, err := loadCloudSession(cmd.Context(), options)
		if err != nil {
			return err
		}
		plan, err := session.getPlan(cmd.Context(), options.cloudProject)
		if err != nil {
			return err
		}
		if plan.V == 1 {
			if err := plan.requirePersonalFreeOrPro(); err != nil {
				return err
			}
		}
		return writePlan(cmd.OutOrStdout(), plan)
	}}
	cmd.Flags().StringVar(&options.baseURL, "base-url", "", "DataTug API /v0/ URL")
	cmd.Flags().BoolVar(&options.insecureStorage, "insecure-storage", false, "use the plaintext DataTug auth session")
	cmd.Flags().StringVar(&options.cloudProject, "cloud-project", "", "cloud-registered project ID hint, verified by the server")
	return cmd
}
