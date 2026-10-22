package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/config"
	"github.com/bitflipp/whitegopher/internal/store"
	"golang.org/x/term"
)

func useradd(cfg config.Config, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("useradd", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "make the user an administrator")
	email := fs.String("email", "", "email address (used to match invitation attendees)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: useradd [-admin] [-email ADDRESS] NAME (password is prompted for, or read from stdin)")
	}
	name := fs.Arg(0)
	if !auth.ValidUsername(name) {
		return fmt.Errorf("invalid username %q: use letters, digits and . _ @ - (max 64)", name)
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = st.CreateUser(store.User{
		Username:     name,
		Email:        *email,
		PasswordHash: hash,
		IsAdmin:      *admin,
	})
	if errors.Is(err, store.ErrExists) {
		return fmt.Errorf("user %q already exists", name)
	}
	return err
}

// readPassword prompts (without echo, twice) on a terminal, or reads one
// line from a pipe.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	var pw string
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Repeat password: ")
		b2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", errors.New("passwords do not match")
		}
		pw = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	return pw, nil
}
