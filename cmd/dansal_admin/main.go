package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v2"
)

const defaultSocket = "./dansal.sock"
const defaultConfigPath = "/etc/dansal/config.yaml"
const defaultWebConfigPath = "/etc/dansal/web.yaml"

func socketFromConfig() string {
	data, err := os.ReadFile(defaultConfigPath)
	if err != nil {
		return defaultSocket
	}
	var cfg struct {
		Server struct {
			AdminSocket string `yaml:"admin_socket"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil || cfg.Server.AdminSocket == "" {
		return defaultSocket
	}
	return cfg.Server.AdminSocket
}

type request struct {
	Cmd          string `json:"cmd"`
	Username     string `json:"username,omitempty"`
	Email        string `json:"email,omitempty"`
	Password     string `json:"password,omitempty"`
	Role         string `json:"role,omitempty"`
	OrgID        int    `json:"org_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Since        string `json:"since,omitempty"`
	SessionID       int    `json:"session_id,omitempty"`
	InviteToken     string `json:"invite_token,omitempty"`
	Telegram        string `json:"telegram,omitempty"`
	Matrix          string `json:"matrix,omitempty"`
	SMTPHost        string `json:"smtp_host,omitempty"`
	SMTPPort        int    `json:"smtp_port,omitempty"`
	SMTPUsername    string `json:"smtp_username,omitempty"`
	SMTPFrom        string `json:"smtp_from,omitempty"`
	SMTPFromName    string `json:"smtp_from_name,omitempty"`
	SMTPTLS         string `json:"smtp_tls,omitempty"`
	SMTPTimeoutSecs int    `json:"smtp_timeout_secs,omitempty"`
	SMTPTo          string `json:"smtp_to,omitempty"`
}

type response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type session struct {
	ID          int    `json:"id"`
	UserAgent   string `json:"user_agent"`
	IP          string `json:"ip"`
	Fingerprint bool   `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
	ExpiresAt   string `json:"expires_at"`
}

type user struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

type org struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

type member struct {
	UserID    int    `json:"user_id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

func send(socketPath string, req request) response {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot connect to %s: %v\n", socketPath, err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return resp
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

var socketPath string

func main() {
	flag.StringVar(&socketPath, "socket", socketFromConfig(), "path to dansal admin socket")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	sub, rest := args[0], args[1:]

	switch sub {
	case "help":
		cmdHelp(rest)
	case "list-users":
		cmdListUsers(rest)
	case "create-user":
		cmdCreateUser(rest)
	case "delete-user":
		cmdDeleteUser(rest)
	case "set-password":
		cmdSetPassword(rest)
	case "set-role":
		cmdSetRole(rest)
	case "list-invites":
		cmdListInvites(rest)
	case "revoke-invite":
		cmdRevokeInvite(rest)
	case "list-sessions":
		cmdListSessions(rest)
	case "revoke-session":
		cmdRevokeSession(rest)
	case "enable-user":
		cmdEnableUser(rest)
	case "disable-user":
		cmdDisableUser(rest)
	case "list-orgs":
		cmdListOrgs(rest)
	case "list-members":
		cmdListMembers(rest)
	case "add-member":
		cmdAddMember(rest)
	case "remove-member":
		cmdRemoveMember(rest)
	case "vacuum":
		cmdVacuum()
	case "backup":
		cmdBackup(rest)
	case "incremental-backup":
		cmdIncrementalBackup(rest)
	case "restore":
		cmdRestore(rest)
	case "password-backup":
		cmdPasswordBackup(rest)
	case "password-restore":
		cmdPasswordRestore(rest)
	case "fill-location-fields":
		cmdFillLocationFields(rest)
	case "export":
		cmdExport(rest)
	case "import":
		cmdImport(rest)
	case "smtp-show":
		cmdSMTPShow(rest)
	case "smtp-set":
		cmdSMTPSet(rest)
	case "smtp-set-password":
		cmdSMTPSetPassword(rest)
	case "smtp-test":
		cmdSMTPTest(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", sub)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: dansal_admin [--socket PATH] <command> [flags]

User management:
  list-users                                         List all users
  create-user  --username STR --email STR            Create a new user
               --password STR [--role STR]
               [--telegram STR] [--matrix STR]
  delete-user  --username STR                        Delete a user
  set-password --username STR --password STR         Change a user's password
  set-role     --username STR --role STR             Change a user's role
  enable-user  --username STR                        Re-enable a disabled user
  disable-user --username STR                        Disable a user account

Invite links:
  list-invites   [--username STR]                    List invite links (all, or by creator)
  revoke-invite  --token STR                         Revoke an unused invite link

Session management:
  list-sessions  --username STR                      List active sessions for a user
  revoke-session --id INT                            Revoke a specific session

Organization management:
  list-orgs                                          List all organizations
  list-members --org-id INT                          List members of an org
  add-member   --org-id INT --username STR           Add user to an org
  remove-member --org-id INT --username STR          Remove user from an org

Maintenance:
  fill-location-fields [--db PATH] [--apply]         Parse address/town from location names
  vacuum                                             Reclaim unused database space

Data export/import:
  export --table TABLE [--output FILE] [--db PATH]   Export table to JSON
  import --table TABLE [--input FILE] [--db PATH]    Import JSON (dry-run by default)
         [--apply]                                   Tables: fetchurl, locations, organisations, events

SMTP:
  smtp-show                                          Show current SMTP configuration
  smtp-set     [--host H] [--port P] [--username U]  Set SMTP server settings
               [--from F] [--from-name N] [--tls M] [--timeout S]
  smtp-set-password [--password P]                   Set (obscured) SMTP account password
  smtp-test    --to EMAIL                            Send a test email
  backup             [--output PATH]                 Full backup (config + db + images)
  incremental-backup --since RFC3339 [--output PATH] Backup only files changed since time
  restore            --input PATH                    Restore from a backup archive
  password-backup    [--output PATH] [--password P]  Encrypted backup (AES-256-GCM)
  password-restore   --input PATH   [--password P]   Decrypt and restore an encrypted backup

Roles: admin, user, publisher, viewer

Run 'dansal_admin help <command>' for details on a specific command.`)
}

var commandHelp = map[string]string{
	"list-users": `Usage: dansal_admin list-users

List all users with their ID, username, email, role and creation date.`,

	"create-user": `Usage: dansal_admin create-user --username STR --email STR --password STR [--role STR] [--telegram STR] [--matrix STR]

Create a new user account.

Flags:
  --username  Username (required)
  --email     Email address (required)
  --password  Password (required)
  --role      Role: admin, user, publisher, viewer (default: user)
  --telegram  Telegram handle (optional)
  --matrix    Matrix ID (optional)`,

	"delete-user": `Usage: dansal_admin delete-user --username STR

Delete a user. Admin accounts cannot be deleted.

Flags:
  --username  Username of the account to delete (required)`,

	"set-password": `Usage: dansal_admin set-password --username STR --password STR

Change the password for any user account.

Flags:
  --username  Username of the target account (required)
  --password  New password (required)`,

	"set-role": `Usage: dansal_admin set-role --username STR --role STR

Change the role of a user account.

Flags:
  --username  Username of the target account (required)
  --role      New role: admin, user, publisher, viewer (required)`,

	"list-invites": `Usage: dansal_admin list-invites [--username STR]

List invite links. Without --username all links are shown.

Flags:
  --username  Filter by creator username (optional)`,

	"revoke-invite": `Usage: dansal_admin revoke-invite --token STR

Revoke an unused invite link.

Flags:
  --token  Invite token (required)`,

	"list-sessions": `Usage: dansal_admin list-sessions --username STR

List all sessions (active and expired) for a user.

Flags:
  --username  Username (required)`,

	"revoke-session": `Usage: dansal_admin revoke-session --id INT

Revoke a session by its numeric ID. The session token is invalidated immediately.

Flags:
  --id  Session ID (required)`,

	"enable-user": `Usage: dansal_admin enable-user --username STR

Re-enable a disabled user account and reset its failed-login counter.

Flags:
  --username  Username of the account to enable (required)`,

	"disable-user": `Usage: dansal_admin disable-user --username STR

Disable a user account. Active sessions are revoked immediately.
Admin accounts cannot be disabled.

Flags:
  --username  Username of the account to disable (required)`,

	"list-orgs": `Usage: dansal_admin list-orgs

List all organizations with their ID, name, description and creation date.`,

	"list-members": `Usage: dansal_admin list-members --org-id INT

List all members of an organization.

Flags:
  --org-id  Organization ID (required)`,

	"add-member": `Usage: dansal_admin add-member --org-id INT --username STR

Add a user to an organization. Has no effect if the user is already a member.

Flags:
  --org-id    Organization ID (required)
  --username  Username to add (required)`,

	"smtp-show": `Usage: dansal_admin smtp-show

Show the current SMTP configuration. The password is never displayed.`,

	"smtp-set": `Usage: dansal_admin smtp-set [--host H] [--port P] [--username U] [--from F] [--from-name N] [--tls M] [--timeout S]

Set SMTP server settings. Only provided flags are updated.

Flags:
  --host       SMTP server hostname
  --port       SMTP server port (default 587 at send time)
  --username   SMTP account username
  --from       Envelope From address (defaults to username)
  --from-name  Display name in the From header
  --tls        TLS mode: starttls (default), tls (port 465), none
  --timeout    Dial and send timeout in seconds (default 30)`,

	"smtp-set-password": `Usage: dansal_admin smtp-set-password [--password P]

Set the SMTP account password. It is encrypted with AES-256-GCM and
stored obscured in config.yaml. The encryption key is also stored in
config.yaml; protect the file with mode 600.

If --password is omitted the password is prompted from the terminal (no echo).

Flags:
  --password  SMTP account password (prompted if omitted)`,

	"smtp-test": `Usage: dansal_admin smtp-test --to EMAIL

Send a test email using the current SMTP configuration.

Flags:
  --to  Recipient email address (required)`,

	"export": `Usage: dansal_admin export --table TABLE [--output FILE] [--db PATH]

Export a database table to JSON. Output goes to stdout by default.

Flags:
  --table   Table to export: fetchurl, locations, organisations, events (required)
  --output  Destination file (default: stdout)
  --db      Path to calendar.db (default: /var/lib/dansal/calendar.db)`,

	"import": `Usage: dansal_admin import --table TABLE [--input FILE] [--db PATH] [--apply]

Import records from a JSON file. Runs as dry-run by default; pass --apply to write.
Deduplication: fetch sources by URL, locations by name, organisations by name,
events by uid then url.

Flags:
  --table   Table to import: fetchurl, locations, organisations, events (required)
  --input   Source JSON file (default: stdin)
  --db      Path to calendar.db (default: /var/lib/dansal/calendar.db)
  --apply   Write changes to the database (default is dry-run)`,

	"fill-location-fields": `Usage: dansal_admin fill-location-fields [--db PATH] [--apply]

Parse address, zipcode, and town out of location names for rows where
those columns are empty. Recognises German address patterns embedded in
the location name, e.g. "KFZ, Biegenstr. 13, 35037 Marburg".

Without --apply the command prints what would change (dry-run).

Flags:
  --db     Path to calendar.db (default: /var/lib/dansal/calendar.db)
  --apply  Write the changes to the database`,

	"vacuum": `Usage: dansal_admin vacuum

Rebuild the database file to reclaim space freed by deleted rows.
Equivalent to running VACUUM in SQLite. May take a moment on large databases.`,

	"backup": `Usage: dansal_admin backup [--output PATH]

Create a full backup as a .tar.gz archive containing:
  config.yaml   — server configuration
  calendar.db   — consistent SQLite snapshot (via VACUUM INTO)
  images/       — all uploaded images

Flags:
  --output  Destination file (default: ./dansal-backup-<timestamp>.tar.gz)`,

	"restore": `Usage: dansal_admin restore --input PATH

Restore from a .tar.gz archive created by backup or incremental-backup.

  config.yaml  — written to the server's config path; config reloaded live
  calendar.db  — restored via SQLite online backup API (no restart needed)
  images/      — files extracted into the images directory (overlay, no delete)

Flags:
  --input  Path to the .tar.gz backup archive (required)`,

	"incremental-backup": `Usage: dansal_admin incremental-backup --since RFC3339 [--output PATH]

Create an incremental backup containing:
  config.yaml   — always included (small, defines runtime)
  calendar.db   — always included (full snapshot)
  images/       — only files modified after --since

Flags:
  --since   Include files changed after this time, e.g. 2026-05-01T00:00:00Z (required)
  --output  Destination file (default: ./dansal-incremental-<timestamp>.tar.gz)`,

	"remove-member": `Usage: dansal_admin remove-member --org-id INT --username STR

Remove a user from an organization.

Flags:
  --org-id    Organization ID (required)
  --username  Username to remove (required)`,

	"password-backup": `Usage: dansal_admin password-backup [--output PATH] [--password STR]

Create a full backup and encrypt it with AES-256-GCM.
Key derivation uses scrypt (N=65536, r=8, p=1).
Password hashes are not included in the backup archive.

If --password is omitted the password is prompted from the terminal
(no echo, confirmed twice). Passing the password as a flag exposes
it in the process list — prefer the prompt in production.

Flags:
  --output    Destination file (default: ./dansal-encrypted-<timestamp>.tar.gz.enc)
  --password  Encryption password (prompted if omitted)`,

	"password-restore": `Usage: dansal_admin password-restore --input PATH [--password STR]

Decrypt a backup created by password-backup and restore it.
  config.yaml  — written to the server's config path; config reloaded live
  calendar.db  — restored via SQLite online backup API (no restart needed)
  images/      — files extracted into the images directory (overlay, no delete)

Flags:
  --input     Path to the encrypted backup file (required)
  --password  Decryption password (prompted if omitted)`,
}

func cmdHelp(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(0)
	}
	text, ok := commandHelp[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		usage()
		os.Exit(1)
	}
	fmt.Println(text)
}

func cmdListUsers(args []string) {
	resp := send(socketPath, request{Cmd: "list-users"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var users []user
	json.Unmarshal(resp.Data, &users)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tEMAIL\tROLE\tDISABLED\tCREATED")
	for _, u := range users {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%v\t%s\n", u.ID, u.Username, u.Email, u.Role, u.Disabled, u.CreatedAt)
	}
	w.Flush()
}

func cmdCreateUser(args []string) {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["create-user"]) }
	username := fs.String("username", "", "username")
	email := fs.String("email", "", "email address")
	password := fs.String("password", "", "password")
	role := fs.String("role", "user", "role (admin|user|publisher|viewer)")
	telegram := fs.String("telegram", "", "Telegram handle")
	matrix := fs.String("matrix", "", "Matrix ID")
	fs.Parse(args)
	if *username == "" || *email == "" || *password == "" {
		die("--username, --email and --password are required")
	}
	resp := send(socketPath, request{
		Cmd:      "create-user",
		Username: *username,
		Email:    *email,
		Password: *password,
		Role:     *role,
		Telegram: *telegram,
		Matrix:   *matrix,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var u user
	json.Unmarshal(resp.Data, &u)
	fmt.Printf("created user %s (id=%d, role=%s)\n", u.Username, u.ID, u.Role)
}

func cmdDeleteUser(args []string) {
	fs := flag.NewFlagSet("delete-user", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["delete-user"]) }
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *username == "" {
		die("--username is required")
	}
	resp := send(socketPath, request{Cmd: "delete-user", Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("deleted user %s\n", *username)
}

func cmdSetPassword(args []string) {
	fs := flag.NewFlagSet("set-password", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["set-password"]) }
	username := fs.String("username", "", "username")
	password := fs.String("password", "", "new password")
	fs.Parse(args)
	if *username == "" || *password == "" {
		die("--username and --password are required")
	}
	resp := send(socketPath, request{Cmd: "set-password", Username: *username, Password: *password})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("password updated for %s\n", *username)
}

func cmdSetRole(args []string) {
	fs := flag.NewFlagSet("set-role", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["set-role"]) }
	username := fs.String("username", "", "username")
	role := fs.String("role", "", "role (admin|user|publisher|viewer)")
	fs.Parse(args)
	if *username == "" || *role == "" {
		die("--username and --role are required")
	}
	resp := send(socketPath, request{Cmd: "set-role", Username: *username, Role: *role})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("role updated: %s → %s\n", *username, *role)
}

func cmdListOrgs(args []string) {
	resp := send(socketPath, request{Cmd: "list-orgs"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var orgs []org
	json.Unmarshal(resp.Data, &orgs)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tDESCRIPTION\tCREATED")
	for _, o := range orgs {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", o.ID, o.Name, o.Description, o.CreatedAt)
	}
	w.Flush()
}

func cmdListMembers(args []string) {
	fs := flag.NewFlagSet("list-members", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["list-members"]) }
	orgID := fs.Int("org-id", 0, "organization ID")
	fs.Parse(args)
	if *orgID == 0 {
		die("--org-id is required")
	}
	resp := send(socketPath, request{Cmd: "list-members", OrgID: *orgID})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var members []member
	json.Unmarshal(resp.Data, &members)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USER_ID\tUSERNAME\tCREATED")
	for _, m := range members {
		fmt.Fprintf(w, "%d\t%s\t%s\n", m.UserID, m.Username, m.CreatedAt)
	}
	w.Flush()
}

func cmdAddMember(args []string) {
	fs := flag.NewFlagSet("add-member", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["add-member"]) }
	orgID := fs.Int("org-id", 0, "organization ID")
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *orgID == 0 || *username == "" {
		die("--org-id and --username are required")
	}
	resp := send(socketPath, request{Cmd: "add-member", OrgID: *orgID, Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("added %s to organization %d\n", *username, *orgID)
}

func formatSize(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func cmdVacuum() {
	resp := send(socketPath, request{Cmd: "vacuum"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("database vacuumed")
}

func cmdBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["backup"]) }
	output := fs.String("output", "", "destination file path")
	fs.Parse(args)

	resp := send(socketPath, request{Cmd: "backup", Path: *output})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var result struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	json.Unmarshal(resp.Data, &result)
	fmt.Printf("backup written to %s (%s)\n", result.Path, formatSize(result.Size))
}

func cmdIncrementalBackup(args []string) {
	fs := flag.NewFlagSet("incremental-backup", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["incremental-backup"]) }
	output := fs.String("output", "", "destination file path")
	since := fs.String("since", "", "include files changed after this time (RFC3339)")
	fs.Parse(args)

	if *since == "" {
		die("--since is required (e.g. --since 2026-05-01T00:00:00Z)")
	}
	resp := send(socketPath, request{Cmd: "incremental-backup", Path: *output, Since: *since})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var result struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	json.Unmarshal(resp.Data, &result)
	fmt.Printf("incremental backup written to %s (%s)\n", result.Path, formatSize(result.Size))
}

func cmdRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["restore"]) }
	input := fs.String("input", "", "path to backup archive")
	fs.Parse(args)

	if *input == "" {
		die("--input is required")
	}
	resp := send(socketPath, request{Cmd: "restore", Path: *input})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var result struct {
		Config bool `json:"config"`
		DB     bool `json:"db"`
		Images int  `json:"images"`
	}
	json.Unmarshal(resp.Data, &result)
	fmt.Printf("restored: config=%v db=%v images=%d\n", result.Config, result.DB, result.Images)
}

func cmdPasswordBackup(args []string) {
	fs := flag.NewFlagSet("password-backup", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["password-backup"]) }
	output := fs.String("output", "", "destination file path")
	password := fs.String("password", "", "encryption password (prompted if omitted)")
	fs.Parse(args)

	var pw []byte
	if *password != "" {
		pw = []byte(*password)
	} else {
		var err error
		pw, err = promptPassword("Encryption password: ")
		if err != nil {
			die("password prompt: %v", err)
		}
		pw2, err := promptPassword("Confirm password: ")
		if err != nil {
			die("password prompt: %v", err)
		}
		if string(pw) != string(pw2) {
			die("passwords do not match")
		}
	}

	// Server writes the backup to a temp path; we encrypt it locally.
	tmp, err := os.CreateTemp("", "dansal-pbkup-*.tar.gz")
	if err != nil {
		die("temp file: %v", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath)

	resp := send(socketPath, request{Cmd: "backup", Path: tmpPath})
	if !resp.OK {
		die("%s", resp.Error)
	}
	defer os.Remove(tmpPath)

	outPath := *output
	if outPath == "" {
		outPath = fmt.Sprintf("./dansal-encrypted-%s.tar.gz.enc", time.Now().Format("20060102-150405"))
	}

	fmt.Fprintln(os.Stderr, "Deriving key (this takes a moment)...")
	if err := encryptFile(tmpPath, outPath, pw); err != nil {
		die("encrypt: %v", err)
	}

	info, _ := os.Stat(outPath)
	var size int64
	if info != nil {
		size = info.Size()
	}
	fmt.Printf("encrypted backup written to %s (%s)\n", outPath, formatSize(size))
}

func cmdPasswordRestore(args []string) {
	fs := flag.NewFlagSet("password-restore", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["password-restore"]) }
	input := fs.String("input", "", "path to encrypted backup")
	password := fs.String("password", "", "decryption password (prompted if omitted)")
	fs.Parse(args)

	if *input == "" {
		die("--input is required")
	}

	var pw []byte
	if *password != "" {
		pw = []byte(*password)
	} else {
		var err error
		pw, err = promptPassword("Decryption password: ")
		if err != nil {
			die("password prompt: %v", err)
		}
	}

	fmt.Fprintln(os.Stderr, "Deriving key (this takes a moment)...")
	data, err := decryptFile(*input, pw)
	if err != nil {
		die("%v", err)
	}

	tmp, err := os.CreateTemp("", "dansal-prestore-*.tar.gz")
	if err != nil {
		die("temp file: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		die("write temp: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmpPath)

	resp := send(socketPath, request{Cmd: "restore", Path: tmpPath})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var result struct {
		Config bool `json:"config"`
		DB     bool `json:"db"`
		Images int  `json:"images"`
	}
	json.Unmarshal(resp.Data, &result)
	fmt.Printf("restored: config=%v db=%v images=%d\n", result.Config, result.DB, result.Images)
}

type invite struct {
	ID        int    `json:"id"`
	Token     string `json:"token"`
	Role      string `json:"role"`
	OrgID     *int   `json:"org_id"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at"`
	CreatedAt string `json:"created_at"`
}

func cmdListInvites(args []string) {
	fs := flag.NewFlagSet("list-invites", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["list-invites"]) }
	username := fs.String("username", "", "filter by creator username")
	fs.Parse(args)

	resp := send(socketPath, request{Cmd: "list-invites", Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var links []invite
	json.Unmarshal(resp.Data, &links)
	if len(links) == 0 {
		fmt.Println("no invite links found")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tROLE\tORG_ID\tEXPIRES\tUSED\tTOKEN")
	for _, l := range links {
		orgID := "-"
		if l.OrgID != nil {
			orgID = fmt.Sprintf("%d", *l.OrgID)
		}
		used := "-"
		if l.UsedAt != "" {
			used = l.UsedAt
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", l.ID, l.Role, orgID, l.ExpiresAt, used, l.Token)
	}
	w.Flush()
}

func cmdRevokeInvite(args []string) {
	fs := flag.NewFlagSet("revoke-invite", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["revoke-invite"]) }
	token := fs.String("token", "", "invite token")
	fs.Parse(args)
	if *token == "" {
		die("--token is required")
	}
	resp := send(socketPath, request{Cmd: "revoke-invite", InviteToken: *token})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("invite link revoked")
}

func cmdListSessions(args []string) {
	fs := flag.NewFlagSet("list-sessions", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["list-sessions"]) }
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *username == "" {
		die("--username is required")
	}
	resp := send(socketPath, request{Cmd: "list-sessions", Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var sessions []session
	json.Unmarshal(resp.Data, &sessions)
	if len(sessions) == 0 {
		fmt.Println("no sessions found")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tIP\tFINGERPRINT\tLAST_SEEN\tEXPIRES\tUSER_AGENT")
	for _, s := range sessions {
		fmt.Fprintf(w, "%d\t%s\t%v\t%s\t%s\t%s\n",
			s.ID, s.IP, s.Fingerprint, s.LastSeenAt, s.ExpiresAt, s.UserAgent)
	}
	w.Flush()
}

func cmdRevokeSession(args []string) {
	fs := flag.NewFlagSet("revoke-session", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["revoke-session"]) }
	id := fs.Int("id", 0, "session ID")
	fs.Parse(args)
	if *id == 0 {
		die("--id is required")
	}
	resp := send(socketPath, request{Cmd: "revoke-session", SessionID: *id})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("session %d revoked\n", *id)
}

func cmdEnableUser(args []string) {
	fs := flag.NewFlagSet("enable-user", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["enable-user"]) }
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *username == "" {
		die("--username is required")
	}
	resp := send(socketPath, request{Cmd: "enable-user", Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("user %s enabled\n", *username)
}

func cmdDisableUser(args []string) {
	fs := flag.NewFlagSet("disable-user", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["disable-user"]) }
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *username == "" {
		die("--username is required")
	}
	resp := send(socketPath, request{Cmd: "disable-user", Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("user %s disabled\n", *username)
}

func cmdRemoveMember(args []string) {
	fs := flag.NewFlagSet("remove-member", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["remove-member"]) }
	orgID := fs.Int("org-id", 0, "organization ID")
	username := fs.String("username", "", "username")
	fs.Parse(args)
	if *orgID == 0 || *username == "" {
		die("--org-id and --username are required")
	}
	resp := send(socketPath, request{Cmd: "remove-member", OrgID: *orgID, Username: *username})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("removed %s from organization %d\n", *username, *orgID)
}
