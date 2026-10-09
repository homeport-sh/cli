package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
)

// login signs the CLI in through the dashboard (a device code): it shows a
// code, opens the approval page, and waits for a click there. The token it
// gets is kept in the credentials file, its owner's alone, and never shown.
func (a *app) login(ctx context.Context, args []string) error {
	fs := a.flags("login")
	name := fs.String("name", "", "what to call this device in Account → CLI sessions (default: its hostname)")
	noBrowser := fs.Bool("no-browser", false, "don't open a browser: print the address to open instead")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		h, err := a.hostname()
		if err != nil || h == "" {
			h = "unnamed device"
		}
		*name = h
	}
	base, err := apiBase()
	if err != nil {
		return err
	}
	old, _ := config.LoadCredentials()
	c := &cloud.Client{Base: base, UserAgent: userAgent()}
	d, err := c.StartDevice(ctx, *name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Your code: %s\n", d.UserCode)
	opened := false
	if a.tty && !*noBrowser {
		opened = a.open(d.VerificationURIComplete) == nil
	}
	if opened {
		fmt.Fprintf(a.out, "Opened %s - approve the sign-in there if the codes match.\n", d.VerificationURIComplete)
	} else {
		fmt.Fprintf(a.out, "Open %s in a browser, check the code matches, and approve the sign-in.\n", d.VerificationURIComplete)
	}
	fmt.Fprintln(a.out, "Waiting for approval…")
	interval := time.Duration(max(d.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(d.ExpiresIn, 60)) * time.Second)
	for {
		if err := a.sleep(ctx, interval); err != nil {
			return err
		}
		tok, err := c.PollDevice(ctx, d.DeviceCode)
		var e *cloud.Error
		if errors.As(err, &e) {
			switch e.Message {
			case cloud.Pending:
				if time.Now().After(deadline) {
					return errors.New("the code expired before it was approved: run `homeport login` again")
				}
				continue
			case cloud.SlowDown:
				interval += 5 * time.Second
				continue
			case cloud.Denied:
				return errors.New("the sign-in was denied on the dashboard: nothing was signed in")
			case cloud.Expired:
				return errors.New("the code expired before it was approved: run `homeport login` again")
			}
			return fmt.Errorf("signing in: %s", e.Message)
		}
		if err != nil {
			return err
		}
		if err := config.SaveCredentials(config.Credentials{API: c.Base, Token: tok.Token, TokenID: tok.TokenID,
			Name: tok.Name, Email: tok.User.Email}); err != nil {
			return fmt.Errorf("saving the sign-in: %w", err)
		}
		// the sign-in this one replaces is ended where it was given, not left
		// behind - and never sent anywhere else
		if old != nil && old.Token != tok.Token && checkBase(old.API) == nil {
			_ = (&cloud.Client{Base: old.API, Token: old.Token, UserAgent: userAgent()}).Logout(ctx)
		}
		fmt.Fprintf(a.out, "Signed in as %s (%s) on %s.\n", orLogin(tok.User), tok.User.Email, tok.Name)
		return nil
	}
}

func orLogin(u cloud.User) string {
	if u.Name != "" {
		return u.Name
	}
	return "@" + u.Login
}

// logout ends the CLI's sign-in: revoked on homeport.sh, and forgotten here.
func (a *app) logout(ctx context.Context, args []string) error {
	if err := parse(a.flags("logout"), args); err != nil {
		return err
	}
	c, err := a.client()
	if errors.Is(err, config.ErrNoCredentials) {
		fmt.Fprintln(a.out, "You're not signed in.")
		return nil
	}
	if err != nil {
		return err
	}
	if err := c.Logout(ctx); err != nil && !errors.Is(err, cloud.ErrSignedOut) {
		return fmt.Errorf("revoking the sign-in: %w (it's kept here; try again)", err)
	}
	if err := config.RemoveCredentials(); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Signed out: this device's CLI session is revoked.")
	return nil
}

// whoami says who the CLI is signed in as, where, and in which teams.
func (a *app) whoami(ctx context.Context, args []string) error {
	if err := parse(a.flags("whoami"), args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	w, err := c.WhoAmI(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s <%s>", orLogin(w.User), w.User.Email)
	if w.User.Login != "" && w.User.Name != "" {
		fmt.Fprintf(a.out, " (@%s)", w.User.Login)
	}
	fmt.Fprintln(a.out)
	fmt.Fprintf(a.out, "CLI session: %s, ends %s if unused\n", w.Token.Name, w.Token.ExpiresAt.Local().Format("2 Jan 2006"))
	var teams []string
	for _, t := range w.Teams {
		teams = append(teams, fmt.Sprintf("%s (%s)", t.Slug, t.Role))
	}
	fmt.Fprintf(a.out, "Teams: %s\n", strings.Join(teams, ", "))
	return nil
}

// token makes a token for CI: `homeport token create --name <where>`. It's
// printed once, to stdout alone, for a CI secret; it's listed in Account →
// CLI sessions by its name and revoked there.
func (a *app) token(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return usageErr("usage: homeport token create --name <where it's used>")
	}
	fs := a.flags("token create")
	name := fs.String("name", "", "where it's used (github-actions, say): its name in Account → CLI sessions")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return usageErr("name the token: --name github-actions, say")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	made, err := c.CreateToken(ctx, *name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.err, "A token for %s: set it as HOMEPORT_TOKEN in your CI's secrets now - it isn't shown again.\n", made.Session.Name)
	fmt.Fprintln(a.out, made.Token)
	return nil
}
