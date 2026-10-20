package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
)

// Studio SSH from the operator's/collaborator's machine: a local ed25519 key,
// a short-lived Jam-issued cert per studio, and a ProxyCommand that tunnels
// ssh's stdio through Jam's jam-tunnel upgrade. No token, key or cert is ever
// put on argv or printed.

const (
	sshUser           = "agent"          // the cert principal Jam issues
	certRenewBefore   = time.Hour        // renew a cert with less than this left
	tunnelHandshake   = 30 * time.Second // dial + 101 (Jam waits up to 15 s for the dial-back)
	tunnelDialTimeout = 10 * time.Second
)

// Seams for tests.
var (
	sshNow                = time.Now
	sshStdin    io.Reader = os.Stdin
	sshExec               = syscall.Exec
	sshLookPath           = exec.LookPath
)

// studioIDRe constrains a studio id to what is safe as a file name, an
// ssh_config Host and a word in a ProxyCommand shell line.
var studioIDRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

func sshDir() string     { return filepath.Join(configDir(), "ssh") }
func sshKeyPath() string { return filepath.Join(sshDir(), "id_ed25519") }

// sshCertPath is the studio's cert file; a non-default app's certs live in a
// subdirectory, so two Jams with the same studio id never share one.
func sshCertPath(app, studio string) string {
	if app == defaultApp {
		return filepath.Join(sshDir(), studio+"-cert.pub")
	}
	return filepath.Join(sshDir(), app, studio+"-cert.pub")
}
func sshHostAlias(studio string) string { return "jam-" + studio }

// ensureSSHKey returns the local SSH public key, generating the private key
// (ed25519, 0600) on first use. The key is written to a temp file and linked
// into place, so a concurrent run sees either no key or a whole one; an
// existing key readable by others is tightened to 0600.
func ensureSSHKey() (ssh.PublicKey, error) {
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		return nil, err
	}
	path := sshKeyPath()
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := createSSHKey(path); err != nil {
			return nil, err
		}
		fi, err = os.Stat(path)
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("%s is readable by other users and could not be made private: %w", path, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable SSH key (remove it to generate a new one)", path)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("%s is not an ed25519 key (remove it to generate a new one)", path)
	}
	return signer.PublicKey(), nil
}

// createSSHKey writes a new key to path unless one already exists there
// (another run won the race, which is fine).
func createSSHKey(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "at-jam")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-key-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, werr := f.Write(pem.EncodeToMemory(blk)) // CreateTemp files are 0600
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// certValidUntil reports when the cert line in data stops being valid, if it
// is a cert for key.
func certValidUntil(data []byte, key ssh.PublicKey) (time.Time, bool) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return time.Time{}, false
	}
	c, ok := pk.(*ssh.Certificate)
	if !ok || !bytes.Equal(c.Key.Marshal(), key.Marshal()) {
		return time.Time{}, false
	}
	if c.ValidBefore == ssh.CertTimeInfinity {
		return time.Unix(1<<62, 0), true
	}
	return time.Unix(int64(c.ValidBefore), 0), true
}

// sshTarget is one studio on one plane of one Jam.
type sshTarget struct {
	app          string
	adminURL     string
	adminURLFlag string // as given on the command line, for the ProxyCommand
	studio       string
	participant  bool
	token        string
}

func (t sshTarget) prefix() string {
	if t.participant {
		return "/me"
	}
	return "/admin"
}

// ensureCert makes sure the studio's cert file holds a cert for key with at
// least certRenewBefore left, asking Jam for a new one otherwise. On failure it
// also reports whether the existing cert is still valid for now.
func ensureCert(t sshTarget, key ssh.PublicKey) (stillValid bool, err error) {
	now := sshNow()
	if data, rerr := os.ReadFile(sshCertPath(t.app, t.studio)); rerr == nil {
		if until, ok := certValidUntil(data, key); ok {
			if until.Sub(now) >= certRenewBefore {
				return true, nil
			}
			stillValid = now.Before(until)
		}
	}
	cert, _, err := adminclient.New(t.adminURL, t.token).IssueSSHCert(t.prefix(), t.studio, ssh.MarshalAuthorizedKey(key))
	if err != nil {
		return stillValid, err
	}
	if _, ok := certValidUntil(cert, key); !ok {
		return stillValid, errors.New("Jam returned a certificate that is not for this key")
	}
	if err := os.MkdirAll(filepath.Dir(sshCertPath(t.app, t.studio)), 0o700); err != nil {
		return stillValid, err
	}
	return stillValid, writeFileReplace(sshCertPath(t.app, t.studio), cert, 0o644)
}

// writeFileReplace replaces path via a uniquely named temp file + rename, so
// no concurrent reader (ssh, or another renewal: an IDE opens several
// connections) sees a half-written file.
func writeFileReplace(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, perm)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// tunnelStatusError is a non-101 answer to the tunnel upgrade.
type tunnelStatusError struct {
	Code int
	Body string
}

func (e *tunnelStatusError) Error() string {
	return fmt.Sprintf("tunnel refused: %d %s", e.Code, e.Body)
}

// sshErrMessage turns a cert or tunnel failure into one line for a person.
func sshErrMessage(err error, t sshTarget) string {
	code, body := 0, ""
	if se, ok := errors.AsType[*adminclient.StatusError](err); ok {
		code, body = se.Code, se.Body
	} else if te, ok := errors.AsType[*tunnelStatusError](err); ok {
		code, body = te.Code, te.Body
	} else {
		return err.Error()
	}
	appFlag := ""
	if t.app != defaultApp {
		appFlag = " --app " + t.app
	}
	switch code {
	case http.StatusUnauthorized:
		if t.participant {
			return "your participant sign-in is missing or expired; run: at-jam login --participant" + appFlag
		}
		return "Jam needs an operator sign-in; run: at-jam login" + appFlag
	case http.StatusForbidden:
		if strings.Contains(body, "not linked to a Jam user") {
			return "your login is not linked to a Jam user; ask an operator to link it"
		}
		return "you don't have SSH access to this studio (ask an operator: at-jam studio share …)"
	case http.StatusNotFound:
		return "no studio " + t.studio + " on this Jam (or SSH is not enabled on it)"
	case http.StatusConflict:
		return "studio " + t.studio + " is not running"
	case http.StatusTooManyRequests:
		return "too many SSH tunnels are open to studio " + t.studio + "; close one and retry"
	case http.StatusServiceUnavailable:
		return "studio " + t.studio + " is not connected to Jam right now; retry shortly"
	case http.StatusGatewayTimeout:
		return "studio " + t.studio + " did not open the tunnel in time; retry (if it persists, the studio's image may predate SSH support)"
	}
	return fmt.Sprintf("Jam refused (%d): %s", code, body)
}

// sshFlags are the flags ssh-config, ssh-proxy and ssh share.
type sshFlags struct {
	app, adminURL         *string
	participant, operator *bool
}

func addSSHFlags(fs *flag.FlagSet) *sshFlags {
	return &sshFlags{
		app:         fs.String("app", defaultApp, "settings/token profile"),
		adminURL:    fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)"),
		participant: fs.Bool("participant", false, "use your participant login (at-jam login --participant; the /me routes)"),
		operator:    fs.Bool("operator", false, "use the operator token (at-jam login, or AT_JAM_ADMIN_TOKEN; the /admin routes)"),
	}
}

// resolve validates the flags and studio and picks the plane: as flagged, else
// the participant login when one is cached and unexpired, else the operator.
// usage=true means a bad invocation (exit 2) rather than a missing login.
func (f *sshFlags) resolve(studio string, stderr io.Writer) (t sshTarget, usage bool, err error) {
	if err := validateApp(*f.app); err != nil {
		return t, true, err
	}
	if !studioIDRe.MatchString(studio) {
		return t, true, fmt.Errorf("invalid studio id %q", studio)
	}
	if *f.participant && *f.operator {
		return t, true, errors.New("--participant and --operator are mutually exclusive")
	}
	if *f.adminURL != "" {
		if u, perr := url.Parse(*f.adminURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return t, true, fmt.Errorf("invalid --admin-url %q", *f.adminURL)
		}
	}
	t = sshTarget{
		app: *f.app, studio: studio, adminURLFlag: *f.adminURL,
		adminURL: firstNonEmpty(*f.adminURL, loadSettings(*f.app).AdminURL, defaultAdminURL),
	}
	t.participant = *f.participant
	if !*f.operator {
		tok, perr := resolveParticipantToken(*f.app)
		switch {
		case perr == nil:
			t.participant, t.token = true, tok
			return t, false, nil
		case t.participant:
			return t, false, perr
		}
	}
	t.token = resolveToken(*f.app, adminTokenEnv(stderr), stderr)
	return t, false, nil
}

// proxyCommand is the ssh_config ProxyCommand for t: it pins the app, an
// explicit admin URL and the plane, so later runs are deterministic.
func (t sshTarget) proxyCommand() string {
	parts := []string{"at-jam", "ssh-proxy"}
	if t.app != defaultApp {
		parts = append(parts, "--app", t.app)
	}
	if t.adminURLFlag != "" {
		parts = append(parts, "--admin-url", shellQuote(t.adminURLFlag))
	}
	if t.participant {
		parts = append(parts, "--participant")
	} else {
		parts = append(parts, "--operator")
	}
	parts = append(parts, t.studio)
	return sshConfigEscape(strings.Join(parts, " "))
}

// shellQuote single-quotes s for the shell OpenSSH runs a ProxyCommand with.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// sshConfigEscape escapes ssh_config's %-tokens.
func sshConfigEscape(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// sshConfigPath renders a path as an ssh_config value.
func sshConfigPath(p string) string {
	p = sshConfigEscape(p)
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// sshOptions are the Host options for t, in order.
func (t sshTarget) sshOptions() [][2]string {
	return [][2]string{
		{"User", sshUser},
		{"ProxyCommand", t.proxyCommand()},
		{"IdentityFile", sshConfigPath(sshKeyPath())},
		{"CertificateFile", sshConfigPath(sshCertPath(t.app, t.studio))},
		{"IdentitiesOnly", "yes"},
	}
}

func sshBlockMarkers(studio string) (begin, end string) {
	return "# >>> at-jam " + studio + " >>>", "# <<< at-jam " + studio + " <<<"
}

// sshConfigBlock is t's Host block, between marker comments. It ends with
// `Match all`, so any top-level options that follow it in a config file still
// apply to every host.
func (t sshTarget) sshConfigBlock() string {
	begin, end := sshBlockMarkers(t.studio)
	var b strings.Builder
	b.WriteString(begin + "\n")
	b.WriteString("Host " + sshHostAlias(t.studio) + "\n")
	for _, o := range t.sshOptions() {
		b.WriteString("  " + o[0] + " " + o[1] + "\n")
	}
	b.WriteString("Match all\n")
	b.WriteString(end + "\n")
	return b.String()
}

// installSSHConfigBlock writes block into ~/.ssh/config: in place of an
// earlier block for the same studio, else at the top (after leading comments),
// because OpenSSH takes the first value it finds and an earlier `Host *` would
// otherwise override ours. The file (a symlink's target) is replaced atomically
// and keeps its line endings.
func installSSHConfigBlock(studio, block string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	perm := os.FileMode(0o600)
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if fi, serr := os.Stat(path); serr == nil {
			perm = fi.Mode().Perm()
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	crlf := bytes.Contains(old, []byte("\r\n"))
	s := strings.ReplaceAll(string(old), "\r\n", "\n")
	s, err = spliceSSHConfigBlock(s, studio, block)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return path, writeFileReplace(path, []byte(s), perm)
}

// spliceSSHConfigBlock returns config (LF line endings) with block in place
// of the studio's marked block, or inserted before the first line that is not
// blank or a comment.
func spliceSSHConfigBlock(config, studio, block string) (string, error) {
	begin, end := sshBlockMarkers(studio)
	lines := strings.SplitAfter(config, "\n")
	bi, ei := -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case begin:
			if bi >= 0 {
				return "", fmt.Errorf("two %q markers; fix the file by hand", begin)
			}
			bi = i
		case end:
			if bi >= 0 && ei < 0 {
				ei = i
			}
		}
	}
	if bi >= 0 {
		if ei < 0 {
			return "", fmt.Errorf("found %q without %q; fix the file by hand", begin, end)
		}
		return strings.Join(lines[:bi], "") + block + strings.Join(lines[ei+1:], ""), nil
	}
	at := len(lines)
	for i, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			at = i
			break
		}
	}
	head := strings.Join(lines[:at], "")
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	tail := strings.Join(lines[at:], "")
	if tail != "" {
		block += "\n"
	}
	return head + block + tail, nil
}

// sshPrepare resolves the target, ensures the key and refreshes the cert.
// strict: a failed renewal with no still-valid cert is fatal. It returns the
// exit code to use on failure (0 = carry on).
func sshPrepare(cmd string, f *sshFlags, studio string, strict bool, stderr io.Writer) (sshTarget, int) {
	t, usage, err := f.resolve(studio, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "at-jam %s: %v\n", cmd, err)
		if usage {
			return t, 2
		}
		return t, 1
	}
	key, err := ensureSSHKey()
	if err != nil {
		fmt.Fprintf(stderr, "at-jam %s: %v\n", cmd, err)
		return t, 1
	}
	stillValid, err := ensureCert(t, key)
	switch {
	case err == nil:
	case strict && !stillValid:
		fmt.Fprintf(stderr, "at-jam %s: %s\n", cmd, sshErrMessage(err, t))
		return t, 1
	default:
		fmt.Fprintf(stderr, "at-jam %s: could not get a fresh SSH certificate (ssh-proxy retries when you connect): %s\n", cmd, sshErrMessage(err, t))
	}
	return t, 0
}

func cmdSSHConfig(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ssh-config", flag.ContinueOnError)
	f := addSSHFlags(fs)
	write := fs.Bool("write", false, "install the block in ~/.ssh/config (replacing an earlier at-jam block for the studio) instead of printing it")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: at-jam ssh-config [--write] [--participant|--operator] <studio>")
		return 2
	}
	t, code := sshPrepare("ssh-config", f, pos[0], false, stderr)
	if code != 0 {
		return code
	}
	block := t.sshConfigBlock()
	if !*write {
		fmt.Fprint(stdout, block)
		fmt.Fprintf(stderr, "at-jam ssh-config: put this above any `Host *` block in ~/.ssh/config (or rerun with --write), then: ssh %s\n", sshHostAlias(t.studio))
		return 0
	}
	path, err := installSSHConfigBlock(t.studio, block)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam ssh-config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote Host %s to %s; connect with: ssh %s\n", sshHostAlias(t.studio), path, sshHostAlias(t.studio))
	return 0
}

func cmdSSHProxy(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ssh-proxy", flag.ContinueOnError)
	f := addSSHFlags(fs)
	pos, code, ok := cli.ParseFlags(fs, args, stderr, stderr) // stdout is the ssh stream
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: at-jam ssh-proxy [--participant|--operator] <studio>")
		return 2
	}
	t, code := sshPrepare("ssh-proxy", f, pos[0], true, stderr)
	if code != 0 {
		return code
	}
	err := proxyOnce(context.Background(), proxyOpts{
		BaseURL: t.adminURL, Prefix: t.prefix(), Studio: t.studio, Token: t.token, In: sshStdin, Out: stdout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "at-jam ssh-proxy: %s\n", sshErrMessage(err, t))
		return 1
	}
	return 0
}

func cmdSSH(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	f := addSSHFlags(fs)
	// Flags end at the studio: everything after it is ssh's.
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "Usage of ssh: at-jam ssh [flags] <studio> [ssh args...]")
			fs.SetOutput(stdout)
			fs.PrintDefaults()
			return 0
		}
		fmt.Fprintln(stderr, "ssh:", err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: at-jam ssh [--participant|--operator] <studio> [ssh args...]")
		return 2
	}
	t, code := sshPrepare("ssh", f, fs.Arg(0), true, stderr)
	if code != 0 {
		return code
	}
	path, err := sshLookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "at-jam ssh: no ssh client found on PATH")
		return 1
	}
	argv := []string{"ssh"}
	for _, o := range t.sshOptions() {
		argv = append(argv, "-o", o[0]+"="+o[1])
	}
	argv = append(argv, sshHostAlias(t.studio))
	argv = append(argv, fs.Args()[1:]...)
	if err := sshExec(path, argv, os.Environ()); err != nil {
		fmt.Fprintln(stderr, "at-jam ssh:", err)
		return 1
	}
	return 0
}

// proxyOpts is one tunnel: the admin base URL, the plane prefix, the studio,
// the bearer, and the stdio to splice. TLS nil = system roots.
type proxyOpts struct {
	BaseURL, Prefix, Studio, Token string
	In                             io.Reader
	Out                            io.Writer
	TLS                            *tls.Config
}

// proxyOnce opens the studio's SSH tunnel through Jam's jam-tunnel upgrade and
// splices In→tunnel and tunnel→Out. It speaks HTTP/1.1 on a raw connection (an
// HTTP/2 stream could not be taken over), half-closes the tunnel when In ends,
// and returns once the tunnel's far side closes.
func proxyOnce(ctx context.Context, o proxyOpts) error {
	u, err := url.Parse(o.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid admin URL: %w", err)
	}
	conn, err := dialAdmin(ctx, u, o.TLS)
	if err != nil {
		return fmt.Errorf("admin API unreachable at %s (is `at-jam serve` running?): %w", o.BaseURL, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	target := strings.TrimRight(o.BaseURL, "/") + o.Prefix + "/studios/" + url.PathEscape(o.Studio) + "/ssh"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", collabhttp.UpgradeProtocol)
	req.Header.Set("User-Agent", "at-jam/"+version)
	if o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+o.Token)
	}
	_ = conn.SetDeadline(time.Now().Add(tunnelHandshake))
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("sending the tunnel request: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fmt.Errorf("reading the tunnel response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return &tunnelStatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), collabhttp.UpgradeProtocol) {
		return fmt.Errorf("Jam switched to %q, not %s", resp.Header.Get("Upgrade"), collabhttp.UpgradeProtocol)
	}
	_ = conn.SetDeadline(time.Time{})

	go func() {
		if _, err := io.Copy(conn, o.In); err != nil {
			_ = conn.Close()
			return
		}
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		_ = conn.Close()
	}()
	// br holds any tunnel bytes that arrived with the 101. A far side that
	// drops TCP without a TLS close_notify still ended the session.
	if _, err := io.Copy(o.Out, br); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return nil
}

// dialAdmin opens a raw connection to the admin URL's host: TLS offering only
// http/1.1 for https, plain TCP for http.
func dialAdmin(ctx context.Context, u *url.URL, tlsCfg *tls.Config) (net.Conn, error) {
	d := &net.Dialer{Timeout: tunnelDialTimeout}
	switch u.Scheme {
	case "http":
		return d.DialContext(ctx, "tcp", hostPort(u, "80"))
	case "https":
		cfg := &tls.Config{}
		if tlsCfg != nil {
			cfg = tlsCfg.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		cfg.NextProtos = []string{"http/1.1"}
		return (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", hostPort(u, "443"))
	}
	return nil, fmt.Errorf("unsupported admin URL scheme %q", u.Scheme)
}

func hostPort(u *url.URL, defPort string) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), defPort)
}
