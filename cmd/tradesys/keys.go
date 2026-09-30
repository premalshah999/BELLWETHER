package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/tradesys/dashboard/internal/auth"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// Key management lives on the command line, not on the API.
//
// That is the access control. Issuing a key is the one operation that grants
// access to everything else, so the ability to do it is tied to shell access
// on the host rather than to a role inside the application. There is no
// endpoint to find, no permission to misconfigure, and no path from a stolen
// key to minting more of them.

// runIssueKey mints a key and prints it once.
func runIssueKey(ctx context.Context, databaseURL, name, roleName, note string) error {
	role := auth.Role(roleName)
	if !role.Valid() {
		return fmt.Errorf("unknown role %q: use owner, operator or viewer", roleName)
	}
	if name == "" {
		return fmt.Errorf("a key needs a name: -name \"alice\"")
	}

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	key, err := auth.Generate()
	if err != nil {
		return err
	}
	profile, err := store.IssueKey(ctx, key, name, role, note)
	if err != nil {
		return err
	}

	fmt.Printf(`
  Key issued for %s (%s)

    %s

  This is the only time it will be shown. Nothing stores the key itself, only
  a digest of it, so it cannot be recovered — if it is lost, revoke this one
  and issue another.

    revoke:  tradesys -revoke-key %s
    list:    tradesys -list-keys

`, profile.Name, profile.Role, key.Secret, profile.Prefix)
	return nil
}

// runListKeys prints every profile.
func runListKeys(ctx context.Context, databaseURL string) error {
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	keys, err := store.ListKeys(ctx)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		fmt.Print("\n  No keys issued. Create one with:\n" +
			"    tradesys -issue-key -name \"Your name\" -role owner\n\n")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\n  PREFIX\tNAME\tROLE\tCREATED\tLAST USED\tSTATUS")
	for _, k := range keys {
		last := "never"
		if k.LastUsedAt != nil {
			last = humanSince(*k.LastUsedAt)
		}
		status := "active"
		if k.RevokedAt != nil {
			status = "revoked " + humanSince(*k.RevokedAt)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			k.Prefix, k.Name, k.Role, k.CreatedAt.Format("2006-01-02"), last, status)
	}
	fmt.Fprintln(w)
	return w.Flush()
}

// runRevokeKey withdraws a key.
func runRevokeKey(ctx context.Context, databaseURL, prefix string) error {
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	ok, err := store.RevokeKey(ctx, prefix)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no active key with prefix %q", prefix)
	}
	fmt.Printf("\n  Revoked %s. Any session started with it stops working on its next request.\n\n", prefix)
	return nil
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
