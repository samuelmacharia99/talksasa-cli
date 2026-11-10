package cli

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/config"
)

func (a *App) loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Sign in with a token from the portal's Developers page",
		Long: `Sign in with an API token made under Developers in the portal.

The token is asked for without echo, or read from standard input:
  talksasa login < token.txt

It is checked with the platform, then kept in the system keychain, or in a
file only you can read when there is no keychain. In CI, set TALKSASA_TOKEN
instead of signing in.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := a.apiURL()
			if err != nil {
				return err
			}

			token, err := a.readSecret(fmt.Sprintf("Paste a token from %s/my/developers (input hidden): ", api.Origin(base)), false)
			if err != nil {
				return err
			}
			token = strings.TrimSpace(token)
			if err := checkTokenShape(token); err != nil {
				return &usageError{msg: err.Error()}
			}

			resp, me, err := api.New(base, token, a.userAgent()).Me(cmd.Context())
			if err != nil {
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.Code == "unauthenticated" {
					return withExit(ExitAuth, fmt.Errorf("%s did not accept that token: it is wrong, expired or revoked", api.Origin(base)))
				}
				return err
			}
			if me.Token == nil || !me.Token.CustomerToken {
				return withExit(ExitPermission, fmt.Errorf("that is not a customer API token. Make one under Developers in the portal"))
			}

			source, path, err := config.StoreToken(base, token)
			if err != nil {
				return fmt.Errorf("the token works, but could not be stored: %w", err)
			}

			// Remember a chosen address, so later commands need no --api-url.
			if a.flags.apiURL != "" {
				settings, err := config.Load()
				if err != nil {
					return err
				}
				if settings.APIURL != base {
					settings.APIURL = base
					if err := config.Save(settings); err != nil {
						return err
					}
				}
			}

			if a.flags.json {
				return a.printBody(resp)
			}
			a.out("Signed in to %s as %s <%s>.", api.Origin(base), me.Name, me.Email)
			a.printToken(me)
			if source == config.SourceFile {
				a.note("The token is in %s (no system keychain was available).", path)
			}
			if config.TokenFromEnv() != "" {
				a.note("TALKSASA_TOKEN is set in this shell, and is used instead of the stored token.")
			}
			return nil
		},
	}
}

// checkTokenShape catches a paste that cannot be a token before it is sent.
func checkTokenShape(token string) error {
	if token == "" {
		return errors.New("no token given")
	}
	if len(token) > 512 {
		return errors.New("that is too long to be a token")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) || r > unicode.MaxASCII {
			return errors.New("that does not look like a token: it has spaces or unusual characters")
		}
	}
	return nil
}

func (a *App) printToken(me api.Me) {
	if me.Token == nil {
		return
	}
	expires := "never"
	if me.Token.ExpiresAt != nil {
		expires = when(me.Token.ExpiresAt)
	}
	a.out("Token %q: %s. Expires %s.", me.Token.Name, strings.Join(me.Token.Abilities, ", "), expires)
}

func (a *App) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored token from this computer",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			base, err := a.apiURL()
			if err != nil {
				return err
			}
			removed, err := config.DeleteToken(base)
			if err != nil {
				return err
			}
			if !removed {
				a.out("No token was stored for %s.", api.Origin(base))
			} else {
				a.out("Removed the token for %s from this computer.", api.Origin(base))
				a.out("It still works until it expires: revoke it under Developers in the portal if it may have leaked.")
			}
			if config.TokenFromEnv() != "" {
				a.note("TALKSASA_TOKEN is still set in this shell.")
			}
			return nil
		},
	}
}

func (a *App) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the account and what this token may do",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := a.apiURL()
			if err != nil {
				return err
			}
			token, source, err := a.token(base)
			if err != nil {
				return err
			}
			resp, me, err := api.New(base, token, a.userAgent()).Me(cmd.Context())
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			a.out("%s <%s>", me.Name, me.Email)
			if me.Company != nil && *me.Company != "" {
				a.out("Company: %s", *me.Company)
			}
			a.printToken(me)
			a.out("API: %s (token from %s)", base, source)
			return nil
		},
	}
}
