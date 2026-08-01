package repl

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
	"github.com/xautjzd/agent-cli/internal/catalog"
)

type replLoginUI struct {
	repl        *Repl
	forceChoice bool
}

func (ui replLoginUI) Select(_ context.Context, title string, methods []providerAuth.LoginMethod) (string, error) {
	if len(methods) == 0 {
		return "", fmt.Errorf("no choices available")
	}
	if len(methods) == 1 && !ui.forceChoice {
		return methods[0].ID, nil
	}
	items := make([]pickerItem, len(methods))
	for i, method := range methods {
		items[i] = pickerItem{label: method.Label + "  " + method.Description, filterText: method.ID + " " + method.Label}
	}
	if ui.repl.tuiSelect != nil {
		choice, ok := ui.repl.tuiSelect(title, items)
		if !ok {
			return "", fmt.Errorf("cancelled")
		}
		return methods[choice].ID, nil
	}
	fmt.Fprintln(ui.repl.Out, title+":")
	for i, method := range methods {
		fmt.Fprintf(ui.repl.Out, "  %d. %s — %s\n", i+1, method.Label, method.Description)
	}
	for {
		answer, ok := ui.repl.readInput(fmt.Sprintf("Choice [1-%d]: ", len(methods)))
		if !ok {
			return "", fmt.Errorf("cancelled")
		}
		for i, method := range methods {
			if strings.TrimSpace(answer) == fmt.Sprint(i+1) || strings.TrimSpace(answer) == method.ID {
				return method.ID, nil
			}
		}
		fmt.Fprintln(ui.repl.Out, "Choose a listed number or method ID.")
	}
}

func (ui replLoginUI) OpenURL(ctx context.Context, url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (ui replLoginUI) Prompt(_ context.Context, label string) (string, error) {
	answer, ok := ui.repl.readInput(label + ": ")
	if !ok {
		return "", fmt.Errorf("cancelled")
	}
	return strings.TrimSpace(answer), nil
}

func (ui replLoginUI) PromptSecret(_ context.Context, label string) (string, error) {
	answer, ok := ui.repl.readSecret(label + ": ")
	if !ok {
		return "", fmt.Errorf("cancelled")
	}
	return strings.TrimSpace(answer), nil
}

func (ui replLoginUI) Notify(message string) { fmt.Fprintln(ui.repl.Out, message) }

func (r *Repl) authService() (*providerAuth.Service, error) {
	if r.Cfg == nil || r.Cfg.AuthService == nil {
		return nil, fmt.Errorf("managed provider login is not configured")
	}
	return r.Cfg.AuthService, nil
}

func (r *Repl) selectAuthProvider(service *providerAuth.Service, requested string, storedOnly bool) (string, error) {
	if requested != "" {
		if _, ok := service.Registry().Get(requested); !ok {
			return "", fmt.Errorf("provider %q does not support managed login", requested)
		}
		return requested, nil
	}
	var ids []string
	if storedOnly {
		var err error
		ids, err = service.Store().List()
		if err != nil {
			return "", err
		}
	} else {
		for _, adapter := range service.Registry().List() {
			ids = append(ids, adapter.ID())
		}
	}
	if len(ids) == 0 {
		if storedOnly {
			return "", fmt.Errorf("no provider logins are stored")
		}
		return "", fmt.Errorf("no managed login providers are available")
	}
	if len(ids) == 1 && storedOnly {
		return ids[0], nil
	}
	methods := make([]providerAuth.LoginMethod, 0, len(ids))
	for _, id := range ids {
		label := id
		if adapter, ok := service.Registry().Get(id); ok {
			label = adapter.DisplayName()
		}
		methods = append(methods, providerAuth.LoginMethod{ID: id, Label: label})
	}
	return (replLoginUI{repl: r, forceChoice: true}).Select(context.Background(), "Select provider", methods)
}

func parseLoginArgs(args string) (providerID, method string, err error) {
	fields := strings.Fields(args)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--method" {
			if i+1 >= len(fields) {
				return "", "", fmt.Errorf("--method requires a value")
			}
			i++
			method = fields[i]
			continue
		}
		if providerID == "" {
			providerID = fields[i]
		} else if method == "" {
			method = fields[i]
		} else {
			return "", "", fmt.Errorf("usage: /login [provider] [method]")
		}
	}
	return providerID, method, nil
}

func (r *Repl) cmdLogin(ctx context.Context, args string) error {
	service, err := r.authService()
	if err != nil {
		return err
	}
	providerID, method, err := parseLoginArgs(args)
	if err != nil {
		return err
	}
	providerID, err = r.selectAuthProvider(service, providerID, false)
	if err != nil {
		return err
	}
	status, err := service.Login(ctx, providerID, providerAuth.LoginRequest{Method: method}, replLoginUI{repl: r})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Logged in to %s", status.Name)
	if status.Account != "" {
		fmt.Fprintf(r.Out, " as %s", status.Account)
	}
	fmt.Fprintln(r.Out)
	if _, builtIn := catalog.Lookup(providerID); builtIn {
		// Rebuild even when this provider is already active: a re-login may
		// have changed accounts or rotated the provider's credential source.
		if err := r.cmdProvider(ctx, providerID); err != nil {
			return fmt.Errorf("logged in, but could not activate %s: %w", providerID, err)
		}
	} else if r.Cfg.Provider != providerID {
		fmt.Fprintf(r.Out, "Switch to it with /provider %s\n", providerID)
	}
	return nil
}

func (r *Repl) cmdLogout(ctx context.Context, args string) error {
	service, err := r.authService()
	if err != nil {
		return err
	}
	fields := strings.Fields(args)
	if len(fields) > 1 {
		return fmt.Errorf("usage: /logout [provider]")
	}
	requested := ""
	if len(fields) == 1 {
		requested = fields[0]
	}
	providerID, err := r.selectAuthProvider(service, requested, true)
	if err != nil {
		return err
	}
	if err := service.Logout(ctx, providerID); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Logged out of %s\n", providerID)
	return nil
}

func (r *Repl) cmdAuth(_ context.Context, args string) error {
	service, err := r.authService()
	if err != nil {
		return err
	}
	fields := strings.Fields(args)
	if len(fields) > 1 {
		return fmt.Errorf("usage: /auth [provider]")
	}
	requested := ""
	if len(fields) == 1 {
		requested = fields[0]
	}
	for _, adapter := range service.Registry().List() {
		if requested != "" && adapter.ID() != requested {
			continue
		}
		status, loggedIn, err := service.Status(adapter.ID())
		if err != nil {
			return err
		}
		state := "not logged in"
		if loggedIn {
			state = "logged in"
			if status.Account != "" {
				state += " as " + status.Account
			}
			if status.Plan != "" {
				state += " (" + status.Plan + ")"
			}
		}
		fmt.Fprintf(r.Out, "%s — %s\n", adapter.DisplayName(), state)
		for _, method := range adapter.Methods() {
			fmt.Fprintf(r.Out, "  %-12s %s\n", method.ID, method.Description)
		}
	}
	if requested != "" {
		if _, ok := service.Registry().Get(requested); !ok {
			return fmt.Errorf("provider %q does not support managed login", requested)
		}
	}
	return nil
}

func (r *Repl) printSubscriptionUsage(ctx context.Context, requested string) error {
	service, err := r.authService()
	if err != nil {
		if requested == "" {
			return nil
		}
		return err
	}
	providerID := requested
	if providerID == "" && r.Cfg != nil {
		providerID = r.Cfg.Provider
	}
	if providerID == "" {
		return nil
	}
	loggedIn, err := service.HasCredential(providerID)
	if err != nil {
		return err
	}
	if !loggedIn {
		if requested == "" {
			return nil
		}
		return fmt.Errorf("provider %s is not logged in", providerID)
	}
	snapshot, err := service.Usage(ctx, providerID)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "\nSubscription · %s", providerID)
	if snapshot.Plan != "" {
		fmt.Fprintf(r.Out, " · %s", snapshot.Plan)
	}
	fmt.Fprintln(r.Out)
	limits := append([]providerAuth.UsageLimit(nil), snapshot.Limits...)
	sort.SliceStable(limits, func(i, j int) bool { return limits[i].Name < limits[j].Name })
	for _, limit := range limits {
		fmt.Fprintf(r.Out, "  %s:", limit.Name)
		if limit.UsedPercent != nil {
			fmt.Fprintf(r.Out, " %d%% used", *limit.UsedPercent)
		} else if limit.Used != "" || limit.Limit != "" {
			fmt.Fprintf(r.Out, " %s / %s", limit.Used, limit.Limit)
		} else if limit.Remaining != "" {
			fmt.Fprintf(r.Out, " %s remaining", limit.Remaining)
		}
		if limit.Window > 0 {
			fmt.Fprintf(r.Out, " over %s", limit.Window)
		}
		if limit.ResetsAt != nil {
			fmt.Fprintf(r.Out, "; resets %s", limit.ResetsAt.Local().Format(time.RFC3339))
		}
		fmt.Fprintln(r.Out)
	}
	return nil
}
