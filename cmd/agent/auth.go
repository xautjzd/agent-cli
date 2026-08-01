package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
	"github.com/xautjzd/agent-cli/internal/config"
)

type shellLoginUI struct{ in *bufio.Scanner }

func (ui *shellLoginUI) Select(_ context.Context, label string, methods []providerAuth.LoginMethod) (string, error) {
	if len(methods) == 0 {
		return "", fmt.Errorf("no choices available")
	}
	if len(methods) == 1 {
		return methods[0].ID, nil
	}
	if !isTTY(os.Stdin) {
		return "", fmt.Errorf("%s requires --method <%s> when stdin is not interactive", label, joinMethodIDs(methods))
	}
	fmt.Println(label + ":")
	for i, method := range methods {
		fmt.Printf("  %d. %s", i+1, method.Label)
		if method.Description != "" {
			fmt.Printf(" — %s", method.Description)
		}
		fmt.Println()
	}
	for {
		answer, ok := prompt(ui.in, fmt.Sprintf("Choice [1-%d]", len(methods)))
		if !ok {
			return "", fmt.Errorf("cancelled")
		}
		for i := range methods {
			if answer == fmt.Sprint(i+1) || answer == methods[i].ID {
				return methods[i].ID, nil
			}
		}
		fmt.Println("Choose a listed number or method ID.")
	}
}

func joinMethodIDs(methods []providerAuth.LoginMethod) string {
	ids := make([]string, 0, len(methods))
	for _, method := range methods {
		ids = append(ids, method.ID)
	}
	return strings.Join(ids, "|")
}

func (ui *shellLoginUI) OpenURL(ctx context.Context, url string) error {
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

func (ui *shellLoginUI) Prompt(_ context.Context, label string) (string, error) {
	answer, ok := prompt(ui.in, label)
	if !ok {
		return "", fmt.Errorf("cancelled")
	}
	return answer, nil
}

func (ui *shellLoginUI) PromptSecret(_ context.Context, label string) (string, error) {
	value := strings.TrimSpace(promptSecret(ui.in, label))
	if value == "" {
		return "", fmt.Errorf("cancelled")
	}
	return value, nil
}

func (*shellLoginUI) Notify(message string) { fmt.Println(message) }

func authProvider(service *providerAuth.Service, requested string, storedOnly bool) (string, error) {
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
	if len(ids) == 1 {
		return ids[0], nil
	}
	if !isTTY(os.Stdin) {
		return "", fmt.Errorf("provider is required; choose one of: %s", strings.Join(ids, ", "))
	}
	methods := make([]providerAuth.LoginMethod, 0, len(ids))
	for _, id := range ids {
		label := id
		if adapter, ok := service.Registry().Get(id); ok {
			label = adapter.DisplayName()
		}
		methods = append(methods, providerAuth.LoginMethod{ID: id, Label: label})
	}
	return (&shellLoginUI{in: bufio.NewScanner(os.Stdin)}).Select(context.Background(), "Select provider", methods)
}

func parseAuthTarget(args []string, allowMethod bool) (providerID, method string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--method":
			if !allowMethod || i+1 >= len(args) {
				return "", "", fmt.Errorf("--method requires a value and is valid only for login")
			}
			i++
			method = args[i]
		default:
			if strings.HasPrefix(args[i], "-") || providerID != "" {
				return "", "", fmt.Errorf("unexpected auth argument %q", args[i])
			}
			providerID = args[i]
		}
	}
	return providerID, method, nil
}

func runAuth(args []string) error {
	if helpRequested(args) {
		printSubcommandUsage(os.Stdout, "auth")
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: agent auth login|logout|list|status|usage (try: agent auth -h)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	service := cfg.AuthService
	action := args[0]
	providerID, method, err := parseAuthTarget(args[1:], action == "login")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch action {
	case "list":
		if providerID != "" || method != "" {
			return fmt.Errorf("usage: agent auth list")
		}
		for _, adapter := range service.Registry().List() {
			loggedIn, err := service.HasCredential(adapter.ID())
			if err != nil {
				return err
			}
			state := "not logged in"
			if loggedIn {
				state = "logged in"
			}
			fmt.Printf("%-10s %-38s %s\n", adapter.ID(), adapter.DisplayName(), state)
			for _, loginMethod := range adapter.Methods() {
				fmt.Printf("  %-12s %s\n", loginMethod.ID, loginMethod.Description)
			}
		}
		return nil
	case "login":
		providerID, err = authProvider(service, providerID, false)
		if err != nil {
			return err
		}
		status, err := service.Login(ctx, providerID, providerAuth.LoginRequest{Method: method}, &shellLoginUI{in: bufio.NewScanner(os.Stdin)})
		if err != nil {
			return err
		}
		fmt.Printf("Logged in to %s", status.Name)
		if status.Account != "" {
			fmt.Printf(" as %s", status.Account)
		}
		fmt.Println()
		return nil
	case "logout":
		providerID, err = authProvider(service, providerID, true)
		if err != nil {
			return err
		}
		if err := service.Logout(ctx, providerID); err != nil {
			return err
		}
		fmt.Printf("Logged out of %s\n", providerID)
		return nil
	case "status":
		ids := []string{providerID}
		if providerID == "" {
			ids = nil
			for _, adapter := range service.Registry().List() {
				ids = append(ids, adapter.ID())
			}
		}
		for _, id := range ids {
			status, ok, err := service.Status(id)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Printf("%s: not logged in\n", id)
				continue
			}
			fmt.Printf("%s: logged in", id)
			if status.Account != "" {
				fmt.Printf(" as %s", status.Account)
			}
			if status.Plan != "" {
				fmt.Printf(" (%s)", status.Plan)
			}
			if status.ExpiresAt != nil {
				fmt.Printf(", token expires %s", status.ExpiresAt.Local().Format(time.RFC3339))
			}
			fmt.Println()
		}
		return nil
	case "usage":
		if providerID == "" && cfg.Provider != "" {
			if ok, _ := service.HasCredential(cfg.Provider); ok {
				providerID = cfg.Provider
			}
		}
		providerID, err = authProvider(service, providerID, true)
		if err != nil {
			return err
		}
		snapshot, err := service.Usage(ctx, providerID)
		if err != nil {
			return err
		}
		fmt.Printf("%s usage", providerID)
		if snapshot.Plan != "" {
			fmt.Printf(" (%s)", snapshot.Plan)
		}
		fmt.Println()
		for _, limit := range snapshot.Limits {
			fmt.Printf("  %s:", limit.Name)
			if limit.UsedPercent != nil {
				fmt.Printf(" %d%% used", *limit.UsedPercent)
			} else if limit.Used != "" || limit.Limit != "" {
				fmt.Printf(" %s / %s", limit.Used, limit.Limit)
			} else if limit.Remaining != "" {
				fmt.Printf(" %s remaining", limit.Remaining)
			}
			if limit.Window > 0 {
				fmt.Printf(" over %s", limit.Window)
			}
			if limit.ResetsAt != nil {
				fmt.Printf("; resets %s", limit.ResetsAt.Local().Format(time.RFC3339))
			}
			fmt.Println()
		}
		return nil
	default:
		return fmt.Errorf("unknown auth command %q (try: agent auth -h)", action)
	}
}
