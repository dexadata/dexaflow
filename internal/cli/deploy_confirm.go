package cli

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// isLoopback reports whether the control-plane URL points at the local machine
// (a Lite/dev target). Deploys to a non-loopback server are the ones worth a
// confirmation; loopback ones are not.
func isLoopback(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// shouldConfirm decides whether to prompt before a deploy: only when not already
// confirmed (--yes), the session is interactive, and the target is a real
// (non-loopback) control plane. CI (non-interactive) and Lite (loopback) never
// prompt.
func shouldConfirm(serverURL string, yes, interactive bool) bool {
	return !yes && interactive && !isLoopback(serverURL)
}

// cmdInteractive reports whether the command's stdin is a terminal, so an
// automated/piped invocation is never blocked on a prompt. It reuses the
// x/term-based isInteractive (which, unlike a ModeCharDevice check, tells a TTY
// from a pipe).
func cmdInteractive(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && isInteractive(f)
}

// confirmDeploy prints the prompt and reads a yes/no answer from in, returning
// true only on an explicit y/yes.
func confirmDeploy(in io.Reader, out io.Writer, target, serverURL string) bool {
	return confirmPrompt(in, out, fmt.Sprintf("Deploy %s -> %s?", target, serverURL))
}

// confirmPrompt prints question with a [y/N] hint and reads the answer from
// in, returning true only on an explicit y/yes.
func confirmPrompt(in io.Reader, out io.Writer, question string) bool {
	devPrintf(out, "%s [y/N] ", question)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF { // EOF still yields the typed answer
		return false
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes"
}
