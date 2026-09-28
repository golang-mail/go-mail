package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/golang-mail/go-mail/server"
)

// hashPasswordCommand reads a password from stdin and prints a bcrypt hash
// for server.yml, so the clear text never has to live on disk.
func hashPasswordCommand() {
	fmt.Fprint(os.Stderr, "Password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "no password read from stdin")
		os.Exit(1)
	}
	hash, err := server.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hashing failed:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
