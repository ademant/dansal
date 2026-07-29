package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type adminRequest struct {
	Cmd                   string `json:"cmd"`
	Email                 string `json:"email,omitempty"`
	NewEmail              string `json:"new_email,omitempty"`
	Password              string `json:"password,omitempty"`
	Role                  string `json:"role,omitempty"`
	OrgID                 int    `json:"org_id,omitempty"`
	Path                  string `json:"path,omitempty"`
	Since                 string `json:"since,omitempty"`
	KeepCredentials       bool   `json:"keep_credentials,omitempty"`
	SessionID             int    `json:"session_id,omitempty"`
	InviteToken           string `json:"invite_token,omitempty"`
	Telegram              string `json:"telegram,omitempty"`
	Matrix                string `json:"matrix,omitempty"`
	SMTPHost              string `json:"smtp_host,omitempty"`
	SMTPPort              int    `json:"smtp_port,omitempty"`
	SMTPUsername          string `json:"smtp_username,omitempty"`
	SMTPFrom              string `json:"smtp_from,omitempty"`
	SMTPFromName          string `json:"smtp_from_name,omitempty"`
	SMTPTLS               string `json:"smtp_tls,omitempty"`
	SMTPTimeoutSecs       int    `json:"smtp_timeout_secs,omitempty"`
	SMTPTo                string `json:"smtp_to,omitempty"`
	SMTPSendmail          string `json:"smtp_sendmail,omitempty"`
	TelegramBotToken      string `json:"telegram_bot_token,omitempty"`
	TelegramBotName       string `json:"telegram_bot_name,omitempty"`
	MatrixHomeserver      string `json:"matrix_homeserver,omitempty"`
	MatrixAccessToken     string `json:"matrix_access_token,omitempty"`
	MatrixUsername        string `json:"matrix_username,omitempty"`
	MatrixPassword        string `json:"matrix_password,omitempty"`
	HeartbeatIntervalMins int    `json:"heartbeat_interval_mins,omitempty"`
	EventID               int    `json:"event_id,omitempty"`
}

type adminResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func startAdminSocket(path string) net.Listener {
	if path == "" {
		path = "/var/lib/dansal/dansal.sock"
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Printf("Admin socket error: %v", err)
		return nil
	}
	if err := os.Chmod(path, 0600); err != nil {
		log.Printf("Admin socket chmod error: %v", err)
		ln.Close()
		return nil
	}
	log.Printf("Admin socket listening on %s", path)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleAdminConn(conn)
		}
	}()
	return ln
}

func handleAdminConn(conn net.Conn) {
	defer conn.Close()
	var req adminRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		json.NewEncoder(conn).Encode(adminResponse{OK: false, Error: "invalid request"})
		return
	}
	json.NewEncoder(conn).Encode(dispatchAdminCmd(req))
}

// mutatingAdminCmds lists admin-socket commands that change state and are
// worth an audit trail. Read-only commands (list-*, *-get, *-test) are noisy
// and lower-value, so they're not logged.
var mutatingAdminCmds = map[string]bool{
	"create-user":              true,
	"delete-user":              true,
	"set-password":             true,
	"set-role":                 true,
	"set-email":                true,
	"add-member":               true,
	"remove-member":            true,
	"vacuum":                   true,
	"backup":                   true,
	"incremental-backup":       true,
	"restore":                  true,
	"config-backup":            true,
	"magic-link":               true,
	"invite-admin":             true,
	"revoke-invite":            true,
	"revoke-session":           true,
	"enable-user":              true,
	"disable-user":             true,
	"smtp-set":                 true,
	"smtp-set-password":        true,
	"telegram-set":             true,
	"matrix-set":               true,
	"matrix-login":             true,
	"heartbeat-set":            true,
	"fetch-all":                true,
	"prune-images":             true,
	"mail-bounces":             true,
	"delete-event":             true,
	"delete-all-events":        true,
	"import-locations-geojson": true,
}

// adminAuditTarget returns a short, identifying description of req for audit
// logging. It must never include secrets (passwords, tokens, access tokens).
func adminAuditTarget(req adminRequest) string {
	switch req.Cmd {
	case "create-user", "delete-user", "set-password", "set-role", "enable-user", "disable-user", "magic-link", "invite-admin":
		return "email=" + req.Email
	case "set-email":
		return "email=" + req.Email + " new_email=" + req.NewEmail
	case "add-member", "remove-member":
		return fmt.Sprintf("org_id=%d email=%s", req.OrgID, req.Email)
	case "backup", "incremental-backup", "restore", "config-backup":
		return "path=" + req.Path
	case "revoke-invite":
		return "invite_token=" + req.InviteToken
	case "revoke-session":
		return fmt.Sprintf("session_id=%d", req.SessionID)
	case "smtp-set", "smtp-set-password":
		return "smtp_host=" + req.SMTPHost
	case "telegram-set":
		return "telegram_bot_name=" + req.TelegramBotName
	case "matrix-set", "matrix-login":
		return fmt.Sprintf("matrix_homeserver=%s matrix_username=%s", req.MatrixHomeserver, req.MatrixUsername)
	case "heartbeat-set":
		return fmt.Sprintf("interval_mins=%d", req.HeartbeatIntervalMins)
	case "delete-event":
		return fmt.Sprintf("event_id=%d", req.EventID)
	default:
		return "-"
	}
}

func dispatchAdminCmd(req adminRequest) adminResponse {
	resp := dispatchAdminCmdInner(req)
	if mutatingAdminCmds[req.Cmd] {
		outcome := "ok"
		if !resp.OK {
			outcome = "error: " + resp.Error
		}
		log.Printf("admin-audit: cmd=%s target=%s result=%s", req.Cmd, adminAuditTarget(req), outcome)
	}
	return resp
}

func dispatchAdminCmdInner(req adminRequest) adminResponse {
	switch req.Cmd {
	case "list-users":
		return adminListUsers()
	case "create-user":
		return adminCreateUser(req)
	case "delete-user":
		return adminDeleteUser(req)
	case "set-password":
		return adminSetPassword(req)
	case "set-role":
		return adminSetRole(req)
	case "set-email":
		return adminSetEmail(req)
	case "list-orgs":
		return adminListOrgs()
	case "list-members":
		return adminListMembers(req)
	case "add-member":
		return adminAddMember(req)
	case "remove-member":
		return adminRemoveMember(req)
	case "vacuum":
		return adminVacuum()
	case "backup":
		return adminBackup(req)
	case "incremental-backup":
		return adminIncrementalBackup(req)
	case "restore":
		return adminRestore(req)
	case "config-backup":
		return adminConfigBackup(req)
	case "list-backups":
		return adminListBackups(req)
	case "magic-link":
		return adminMagicLink(req)
	case "invite-admin":
		return adminCreateAdminInvite(req)
	case "list-invites":
		return adminListInvites(req)
	case "revoke-invite":
		return adminRevokeInvite(req)
	case "list-sessions":
		return adminListSessions(req)
	case "revoke-session":
		return adminRevokeSession(req)
	case "enable-user":
		return adminEnableUser(req)
	case "disable-user":
		return adminDisableUser(req)
	case "smtp-get":
		return adminSMTPGet()
	case "smtp-set":
		return adminSMTPSet(req)
	case "smtp-set-password":
		return adminSMTPSetPassword(req)
	case "smtp-test":
		return adminSMTPTest(req)
	case "telegram-get":
		return adminTelegramGet()
	case "telegram-set":
		return adminTelegramSet(req)
	case "matrix-get":
		return adminMatrixGet()
	case "matrix-set":
		return adminMatrixSet(req)
	case "matrix-login":
		return adminMatrixLogin(req)
	case "telegram-test":
		cs := probeTelegram()
		if !cs.OK {
			return adminResponse{OK: false, Error: cs.Error}
		}
		return adminResponse{OK: true}
	case "matrix-test":
		cs := probeMatrix()
		if !cs.OK {
			return adminResponse{OK: false, Error: cs.Error}
		}
		return adminResponse{OK: true}
	case "heartbeat-get":
		return adminHeartbeatGet()
	case "heartbeat-set":
		return adminHeartbeatSet(req)
	case "fetch-all":
		return adminFetchAll()
	case "prune-images":
		return adminPruneImages()
	case "mail-bounces":
		return adminMailBounces()
	case "delete-event":
		return adminDeleteEvent(req)
	case "delete-all-events":
		return adminDeleteAllEvents(req)
	case "export-locations-geojson":
		return adminExportLocationsGeoJSON(req)
	case "import-locations-geojson":
		return adminImportLocationsGeoJSON(req)
	default:
		return adminResponse{OK: false, Error: "unknown command: " + req.Cmd}
	}
}

func adminFetchAll() adminResponse {
	rows, err := db.Query("SELECT " + fetchSourceCols + " FROM fetch_sources ORDER BY id")
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	var sources []FetchSource
	for rows.Next() {
		src, err := scanFetchSource(rows)
		if err != nil {
			continue
		}
		sources = append(sources, src)
	}

	type sourceResult struct {
		ID     int    `json:"id"`
		URL    string `json:"url"`
		Events int    `json:"events"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]sourceResult, 0, len(sources))
	for _, src := range sources {
		events, _, fetchErr := importFromSource(context.Background(), src)
		r := sourceResult{ID: src.ID, URL: src.URL}
		if fetchErr != nil {
			r.Error = fetchErr.Error()
			recordFetchResult(src, 0, fetchErr)
			log.Printf("fetch-all: source_id=%d url=%q type=%s result=error err=%v", src.ID, src.URL, src.Type, fetchErr)
		} else {
			r.Events = len(events)
			recordFetchResult(src, len(events), nil)
			log.Printf("fetch-all: source_id=%d url=%q type=%s result=ok events=%d", src.ID, src.URL, src.Type, len(events))
		}
		results = append(results, r)
	}
	return adminResponse{OK: true, Data: results}
}

func adminListUsers() adminResponse {
	rows, err := db.Query("SELECT id, COALESCE(email,''), COALESCE(display_name,''), role, COALESCE(disabled,0), created_at FROM users ORDER BY id")
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		var disabled int
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &disabled, &u.CreatedAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		u.Disabled = disabled == 1
		users = append(users, u)
	}
	return adminResponse{OK: true, Data: users}
}

func adminListInvites(req adminRequest) adminResponse {
	var rows *sql.Rows
	var err error
	if req.Email == "" {
		rows, err = db.Query(
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links ORDER BY created_at DESC",
		)
	} else {
		u, lookupErr := getUserByEmail(req.Email)
		if lookupErr != nil {
			return adminResponse{OK: false, Error: "user not found"}
		}
		rows, err = db.Query(
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links WHERE created_by=? ORDER BY created_at DESC",
			u.ID,
		)
	}
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	links := []InviteLink{}
	for rows.Next() {
		var l InviteLink
		var orgID sql.NullInt64
		if err := rows.Scan(&l.ID, &l.Token, &l.Role, &orgID, &l.ExpiresAt, &l.UsedAt, &l.CreatedAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		if orgID.Valid {
			id := int(orgID.Int64)
			l.OrgID = &id
		}
		links = append(links, l)
	}
	return adminResponse{OK: true, Data: links}
}

func adminRevokeInvite(req adminRequest) adminResponse {
	if req.InviteToken == "" {
		return adminResponse{OK: false, Error: "invite_token is required"}
	}
	var usedAt string
	if err := db.QueryRow("SELECT COALESCE(used_at,'') FROM invite_links WHERE token=?", req.InviteToken).Scan(&usedAt); err != nil {
		return adminResponse{OK: false, Error: "invite link not found"}
	}
	if usedAt != "" {
		return adminResponse{OK: false, Error: "invite link has already been used"}
	}
	db.Exec("DELETE FROM invite_links WHERE token=?", req.InviteToken)
	return adminResponse{OK: true}
}

func adminMagicLink(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	u, err := getUserByEmail(req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	userID := u.ID
	token, _, err := createMagicToken(userID)
	if err != nil {
		return adminResponse{OK: false, Error: "failed to create token: " + err.Error()}
	}
	url := publicBaseURL() + "/login/magic/" + token
	return adminResponse{OK: true, Data: map[string]string{"url": url}}
}

// adminCreateAdminInvite creates an invite link with role=admin. Only
// reachable via the local admin socket (dansal_admin CLI / dansal_webmin),
// never via the public HTTP API — see createInvite, which always forces
// role=user. req.Email identifies the existing admin user the invite is
// attributed to (the invite_links.created_by FK requires a real user id);
// it is not the invitee's email, since the invitee doesn't exist yet.
func adminCreateAdminInvite(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required (existing admin to attribute the invite to)"}
	}
	u, err := getUserByEmail(req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	if u.Role != RoleAdmin {
		return adminResponse{OK: false, Error: "user is not an admin"}
	}
	var orgID *int
	if req.OrgID != 0 {
		orgID = &req.OrgID
	}
	link, err := createInviteRecord(u.ID, RoleAdmin, "link", orgID)
	if err != nil {
		return adminResponse{OK: false, Error: "failed to create invite: " + err.Error()}
	}
	base := config.Server.BaseURL
	if base == "" {
		base = fmt.Sprintf("http://localhost%s", getPort())
	}
	url := base + "/invites/" + link.Token
	return adminResponse{OK: true, Data: map[string]string{"url": url, "expires_at": link.ExpiresAt}}
}

func adminListSessions(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	u, err := getUserByEmail(req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	userID := u.ID
	rows, err := db.Query(`
		SELECT id,
		       COALESCE(user_agent,''),
		       COALESCE(ip,''),
		       CASE WHEN fingerprint IS NOT NULL AND fingerprint != '' THEN 1 ELSE 0 END,
		       created_at,
		       COALESCE(last_seen_at,''),
		       expires_at
		FROM tokens WHERE user_id=?
		ORDER BY COALESCE(last_seen_at, created_at) DESC`, userID)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	sessions := []Session{}
	for rows.Next() {
		var s Session
		var hasFP int
		if err := rows.Scan(&s.ID, &s.UserAgent, &s.IP, &hasFP, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		s.LastSeenAt = epochStrToRFC3339(s.LastSeenAt)
		s.ExpiresAt = epochStrToRFC3339(s.ExpiresAt)
		s.Fingerprint = hasFP == 1
		sessions = append(sessions, s)
	}
	return adminResponse{OK: true, Data: sessions}
}

func adminRevokeSession(req adminRequest) adminResponse {
	if req.SessionID == 0 {
		return adminResponse{OK: false, Error: "session_id is required"}
	}
	var token string
	if err := db.QueryRow("SELECT token FROM tokens WHERE id=?", req.SessionID).Scan(&token); err != nil {
		return adminResponse{OK: false, Error: "session not found"}
	}
	db.Exec("DELETE FROM tokens WHERE id=?", req.SessionID)
	credentials.invalidate(token)
	return adminResponse{OK: true}
}

func adminEnableUser(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	result, err := db.Exec(
		"UPDATE users SET disabled=0, failed_login_count=0, failed_login_since=NULL WHERE email=?",
		req.Email,
	)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "user not found"}
	}
	return adminResponse{OK: true}
}

func adminDisableUser(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	var userID int
	var role string
	if err := db.QueryRow("SELECT id, role FROM users WHERE email=?", req.Email).Scan(&userID, &role); err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	if role == RoleAdmin {
		return adminResponse{OK: false, Error: "cannot disable admin users"}
	}
	db.Exec("UPDATE users SET disabled=1 WHERE id=?", userID)
	credentials.pruneByUserID(userID)
	db.Exec("DELETE FROM tokens WHERE user_id=?", userID)
	return adminResponse{OK: true}
}

func adminCreateUser(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	role := req.Role
	if role == "" {
		role = RoleUser
	}
	if !validateRole(role) {
		return adminResponse{OK: false, Error: "invalid role: use admin, user, or publisher"}
	}
	result, err := db.Exec(
		"INSERT INTO users (email, password_hash, role, telegram, matrix) VALUES (?, ?, ?, ?, ?)",
		req.Email, hashPassword(req.Password), role, req.Telegram, req.Matrix,
	)
	if err != nil {
		return adminResponse{OK: false, Error: "email already exists"}
	}
	id, _ := result.LastInsertId()
	return adminResponse{OK: true, Data: User{ID: int(id), Email: req.Email, Role: role, Telegram: req.Telegram, Matrix: req.Matrix}}
}

func adminDeleteUser(req adminRequest) adminResponse {
	if req.Email == "" {
		return adminResponse{OK: false, Error: "email is required"}
	}
	var userID int
	var role string
	if err := db.QueryRow("SELECT id, role FROM users WHERE email = ?", req.Email).Scan(&userID, &role); err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	if role == RoleAdmin {
		return adminResponse{OK: false, Error: "cannot delete admin users"}
	}
	db.Exec("DELETE FROM users WHERE id = ?", userID)
	return adminResponse{OK: true}
}

func adminSetPassword(req adminRequest) adminResponse {
	if req.Email == "" || req.Password == "" {
		return adminResponse{OK: false, Error: "email and password are required"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if isPasswordPwned(ctx, req.Password) {
		return adminResponse{OK: false, Error: "Password has appeared in a data breach; choose a different one"}
	}
	result, err := db.Exec("UPDATE users SET password_hash = ? WHERE email = ?",
		hashPassword(req.Password), req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "user not found"}
	}
	return adminResponse{OK: true}
}

func adminSetRole(req adminRequest) adminResponse {
	if req.Email == "" || req.Role == "" {
		return adminResponse{OK: false, Error: "email and role are required"}
	}
	if !validateRole(req.Role) {
		return adminResponse{OK: false, Error: "invalid role: use admin, user, or publisher"}
	}
	result, err := db.Exec("UPDATE users SET role = ? WHERE email = ?", req.Role, req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "user not found"}
	}
	return adminResponse{OK: true}
}

func adminSetEmail(req adminRequest) adminResponse {
	if req.Email == "" || req.NewEmail == "" {
		return adminResponse{OK: false, Error: "email and new_email are required"}
	}
	if !isValidEmail(req.NewEmail) {
		return adminResponse{OK: false, Error: "invalid email address"}
	}
	result, err := db.Exec("UPDATE users SET email = ?, email_verified = 0 WHERE email = ?", req.NewEmail, req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "email already in use"}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "user not found"}
	}
	return adminResponse{OK: true}
}

func adminListOrgs() adminResponse {
	rows, err := db.Query("SELECT id, name, description, created_at FROM organizations ORDER BY id")
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	orgs := []Organization{}
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Description, &o.CreatedAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		orgs = append(orgs, o)
	}
	return adminResponse{OK: true, Data: orgs}
}

func adminListMembers(req adminRequest) adminResponse {
	if req.OrgID == 0 {
		return adminResponse{OK: false, Error: "org_id is required"}
	}
	rows, err := db.Query(`
		SELECT om.organization_id, om.user_id, COALESCE(u.email,''), COALESCE(u.display_name,''), om.created_at
		FROM organization_members om
		JOIN users u ON om.user_id = u.id
		WHERE om.organization_id = ?
		ORDER BY om.created_at`, req.OrgID)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	members := []OrganizationMember{}
	for rows.Next() {
		var m OrganizationMember
		if err := rows.Scan(&m.OrganizationID, &m.UserID, &m.Email, &m.DisplayName, &m.CreatedAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		members = append(members, m)
	}
	return adminResponse{OK: true, Data: members}
}

func adminAddMember(req adminRequest) adminResponse {
	if req.OrgID == 0 || req.Email == "" {
		return adminResponse{OK: false, Error: "org_id and email are required"}
	}
	userID, err := userIDByEmail(req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	if _, err := db.Exec(
		"INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)",
		req.OrgID, userID,
	); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true}
}

func adminPruneImages() adminResponse {
	imagesDir := config.Server.ImagesDir
	type pruneResult struct {
		Removed    int   `json:"removed"`
		FreedBytes int64 `json:"freed_bytes"`
	}
	var result pruneResult
	err := filepath.WalkDir(imagesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(d.Name(), ".avif") {
			return nil
		}
		idStr := strings.TrimSuffix(d.Name(), ".avif")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(imagesDir, path)
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		var table, col string
		switch parts[0] {
		case "musicians":
			table, col = "musicians", "id"
		case "orgs":
			table, col = "organizations", "id"
		default:
			table, col = "events", "id"
		}
		var exists int
		db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+col+" = ?", id).Scan(&exists)
		if exists > 0 {
			return nil
		}
		info, _ := os.Stat(path)
		if removeErr := os.Remove(path); removeErr == nil {
			if info != nil {
				result.FreedBytes += info.Size()
			}
			result.Removed++
			log.Printf("prune-images: removed %s", path)
		}
		return nil
	})
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true, Data: result}
}

func adminVacuum() adminResponse {
	db.Exec("DELETE FROM pending_registrations WHERE approved=0 AND expires_at < strftime('%s','now')")
	if _, err := db.Exec("VACUUM"); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true}
}

func adminSMTPGet() adminResponse {
	return adminResponse{OK: true, Data: smtpPublicConfig()}
}

func adminSMTPSet(req adminRequest) adminResponse {
	if req.SMTPSendmail != "" {
		// Local MTA mode: store sendmail path, clear SMTP server fields.
		config.SMTP.Sendmail = req.SMTPSendmail
		config.SMTP.Host = ""
		config.SMTP.Port = 0
		config.SMTP.Username = ""
		config.SMTP.TLS = ""
	} else if req.SMTPHost != "" {
		// Remote SMTP mode: store host, clear sendmail path.
		config.SMTP.Sendmail = ""
		config.SMTP.Host = req.SMTPHost
		if req.SMTPPort != 0 {
			config.SMTP.Port = req.SMTPPort
		}
		if req.SMTPUsername != "" {
			config.SMTP.Username = req.SMTPUsername
		}
		if req.SMTPTLS != "" {
			config.SMTP.TLS = req.SMTPTLS
		}
		if req.SMTPTimeoutSecs != 0 {
			config.SMTP.TimeoutSecs = req.SMTPTimeoutSecs
		}
	}
	if req.SMTPFrom != "" {
		config.SMTP.From = req.SMTPFrom
	}
	if req.SMTPFromName != "" {
		config.SMTP.FromName = req.SMTPFromName
	}
	if err := saveConfig(configFilePath); err != nil {
		return adminResponse{OK: false, Error: "save config: " + err.Error()}
	}
	return adminResponse{OK: true, Data: smtpPublicConfig()}
}

func adminSMTPSetPassword(req adminRequest) adminResponse {
	if req.Password == "" {
		return adminResponse{OK: false, Error: "password is required"}
	}
	enc, key, err := smtpObscure(req.Password, config.SMTP.PasswordKey)
	if err != nil {
		return adminResponse{OK: false, Error: "encrypt: " + err.Error()}
	}
	config.SMTP.Password = enc
	config.SMTP.PasswordKey = key
	if err := saveConfig(configFilePath); err != nil {
		return adminResponse{OK: false, Error: "save config: " + err.Error()}
	}
	return adminResponse{OK: true}
}

func adminSMTPTest(req adminRequest) adminResponse {
	if req.SMTPTo == "" {
		return adminResponse{OK: false, Error: "smtp_to is required"}
	}
	if _, err := SendEmail(req.SMTPTo, "Dansal SMTP Test", "This is a test email sent by Dansal to verify SMTP configuration.", false); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true}
}

func smtpPublicConfig() map[string]any {
	timeout := config.SMTP.TimeoutSecs
	if timeout == 0 {
		timeout = 30
	}
	return map[string]any{
		"host":         config.SMTP.Host,
		"port":         config.SMTP.Port,
		"username":     config.SMTP.Username,
		"from":         config.SMTP.From,
		"from_name":    config.SMTP.FromName,
		"tls":          config.SMTP.TLS,
		"timeout_secs": timeout,
		"password_set": config.SMTP.Password != "",
		"sendmail":     config.SMTP.Sendmail,
	}
}

func adminMailBounces() adminResponse {
	const logPath = "/var/log/mail.log"
	f, err := os.Open(logPath)
	if err != nil {
		return adminResponse{OK: false, Error: "open mail log: " + err.Error()}
	}
	defer f.Close()

	// queueID → messageID (from cleanup lines)
	queueToMsgID := map[string]string{}
	// queueID → bounce reason (from smtp/lmtp lines with status=bounced/expired)
	queueBounced := map[string]string{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "<timestamp> <host> postfix/SERVICE[PID]: QUEUEID: message"
		pidEnd := strings.Index(line, "]: ")
		if pidEnd < 0 {
			continue
		}
		rest := line[pidEnd+3:]
		colonIdx := strings.Index(rest, ": ")
		if colonIdx < 0 {
			continue
		}
		queueID := rest[:colonIdx]
		msg := rest[colonIdx+2:]

		if strings.HasPrefix(msg, "message-id=") {
			queueToMsgID[queueID] = strings.TrimSpace(strings.TrimPrefix(msg, "message-id="))
			continue
		}
		if strings.Contains(msg, "status=bounced") || strings.Contains(msg, "status=expired") {
			reason := ""
			if idx := strings.Index(msg, " ("); idx >= 0 {
				reason = msg[idx+2:]
				if end := strings.LastIndex(reason, ")"); end >= 0 {
					reason = reason[:end]
				}
			}
			queueBounced[queueID] = reason
		}
	}
	if err := scanner.Err(); err != nil {
		return adminResponse{OK: false, Error: "scan mail log: " + err.Error()}
	}

	type bounceResult struct {
		MessageID string `json:"message_id"`
		Reason    string `json:"reason"`
		Marked    bool   `json:"marked"`
	}
	var results []bounceResult
	for queueID, msgID := range queueToMsgID {
		reason, bounced := queueBounced[queueID]
		if !bounced {
			continue
		}
		res, err := db.Exec(
			"UPDATE verification_tokens SET delivery_failed=1 WHERE message_id=? AND delivery_failed=0",
			msgID,
		)
		marked := err == nil
		if marked {
			if n, _ := res.RowsAffected(); n == 0 {
				marked = false
			}
		}
		results = append(results, bounceResult{MessageID: msgID, Reason: reason, Marked: marked})
		if marked {
			log.Printf("mail-bounces: marked delivery_failed for message_id=%s", msgID)
		}
	}
	if results == nil {
		results = []bounceResult{}
	}
	return adminResponse{OK: true, Data: results}
}

func adminRemoveMember(req adminRequest) adminResponse {
	if req.OrgID == 0 || req.Email == "" {
		return adminResponse{OK: false, Error: "org_id and email are required"}
	}
	userID, err := userIDByEmail(req.Email)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	result, err := db.Exec(
		"DELETE FROM organization_members WHERE organization_id = ? AND user_id = ?",
		req.OrgID, userID,
	)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "member not found in organization"}
	}
	return adminResponse{OK: true}
}

func adminDeleteEvent(req adminRequest) adminResponse {
	if req.EventID == 0 {
		return adminResponse{OK: false, Error: "event_id is required"}
	}

	// Check if event exists first
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", req.EventID).Scan(&exists); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if exists == 0 {
		return adminResponse{OK: false, Error: "event not found"}
	}

	// Delete the event (cascade will handle related tables)
	result, err := db.Exec("DELETE FROM events WHERE id = ?", req.EventID)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}

	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "event not found"}
	}

	return adminResponse{OK: true}
}

func adminDeleteAllEvents(req adminRequest) adminResponse {
	// Count events before deletion for reporting
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}

	if count == 0 {
		return adminResponse{OK: true, Data: map[string]int{"deleted": 0}}
	}

	// Delete all events (cascade will handle related tables)
	result, err := db.Exec("DELETE FROM events")
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}

	// Also clear any organization-event assignments that might remain
	db.Exec("DELETE FROM location_organizations WHERE location_id NOT IN (SELECT id FROM locations)")

	if n, _ := result.RowsAffected(); n > 0 {
		return adminResponse{OK: true, Data: map[string]int{"deleted": int(n)}}
	}

	return adminResponse{OK: true, Data: map[string]int{"deleted": 0}}
}

func adminExportLocationsGeoJSON(req adminRequest) adminResponse {
	rows, err := db.Query(`SELECT id, location, COALESCE(short_name,''), COALESCE(address,''),
		COALESCE(zipcode,''), COALESCE(town,''), COALESCE(country,''),
		COALESCE(country_code,''), COALESCE(region,''), 
		COALESCE(latitude,0), COALESCE(longitude,0), COALESCE(internetsite,''),
		COALESCE(osm_id,0), COALESCE(osm_type,''), COALESCE(wikidata_id,''),
		COALESCE(mb_place_id,''), COALESCE(geohash,''), COALESCE(notes_md,''),
		COALESCE(parking,''), COALESCE(floor_condition,''), created_at, COALESCE(updated_at,0)
		FROM locations ORDER BY id`)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()

	features := make([]map[string]any, 0)
	for rows.Next() {
		var loc Location
		var latitude, longitude sql.NullFloat64
		var osmID sql.NullInt64
		var updatedAt sql.NullInt64

		if err := rows.Scan(&loc.ID, &loc.Location, &loc.ShortName, &loc.Address,
			&loc.Zipcode, &loc.Town, &loc.Country, &loc.CountryCode, &loc.Region,
			&latitude, &longitude, &loc.Internetsite, &osmID, &loc.OsmType,
			&loc.WikidataID, &loc.MBPlaceID, &loc.Geohash, &loc.NotesMd,
			&loc.Parking, &loc.FloorCondition, &loc.CreatedAt, &updatedAt); err != nil {
			continue
		}

		if latitude.Valid {
			f := float64(latitude.Float64)
			loc.Latitude = &f
		}
		if longitude.Valid {
			f := float64(longitude.Float64)
			loc.Longitude = &f
		}
		if osmID.Valid {
			id := int64(osmID.Int64)
			loc.OsmID = &id
		}
		if updatedAt.Valid {
			loc.UpdatedAt = int64(updatedAt.Int64)
		}

		// Get organization assignments for deduplication
		orgRows, _ := db.Query("SELECT organization_id FROM location_organizations WHERE location_id=? ORDER BY organization_id", loc.ID)
		if orgRows != nil {
			for orgRows.Next() {
				var oid int
				orgRows.Scan(&oid)
				loc.OrganizationIDs = append(loc.OrganizationIDs, oid)
			}
			orgRows.Close()
		}

		features = append(features, locationGeoJSONFeature(loc))
	}

	featureCollection := map[string]any{
		"type":           "FeatureCollection",
		"features":       features,
		"generated_at":   time.Now().Format(time.RFC3339),
		"dansal_version": "1.0",
	}

	return adminResponse{OK: true, Data: featureCollection}
}

func locationGeoJSONFeature(loc Location) map[string]any {
	var geometry any
	if loc.Latitude != nil && loc.Longitude != nil {
		geometry = map[string]any{
			"type":        "Point",
			"coordinates": []float64{*loc.Longitude, *loc.Latitude},
		}
	}

	// Create properties with all fields for deduplication
	properties := map[string]any{
		"id":               loc.ID,
		"name":             loc.Location,
		"short_name":       loc.ShortName,
		"address":          loc.Address,
		"zipcode":          loc.Zipcode,
		"town":             loc.Town,
		"country":          loc.Country,
		"country_code":     loc.CountryCode,
		"region":           loc.Region,
		"internetsite":     loc.Internetsite,
		"osm_id":           loc.OsmID,
		"osm_type":         loc.OsmType,
		"wikidata_id":      loc.WikidataID,
		"mb_place_id":      loc.MBPlaceID,
		"geohash":          loc.Geohash,
		"notes":            loc.NotesMd,
		"parking":          loc.Parking,
		"floor_condition":  loc.FloorCondition,
		"created_at":       loc.CreatedAt,
		"updated_at":       loc.UpdatedAt,
		"organization_ids": loc.OrganizationIDs,
	}

	return map[string]any{
		"type":       "Feature",
		"geometry":   geometry,
		"properties": properties,
	}
}

func adminImportLocationsGeoJSON(req adminRequest) adminResponse {
	if req.Path == "" {
		return adminResponse{OK: false, Error: "path is required"}
	}

	// Read the GeoJSON file
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return adminResponse{OK: false, Error: "read file: " + err.Error()}
	}

	var featureCollection struct {
		Type       string           `json:"type"`
		Features   []map[string]any `json:"features"`
		Properties map[string]any   `json:"properties,omitempty"`
	}

	if err := json.Unmarshal(data, &featureCollection); err != nil {
		return adminResponse{OK: false, Error: "parse GeoJSON: " + err.Error()}
	}

	if featureCollection.Type != "FeatureCollection" {
		return adminResponse{OK: false, Error: "expected GeoJSON FeatureCollection"}
	}

	// Process each feature
	imported := 0
	skipped := 0
	updated := 0

	for _, feature := range featureCollection.Features {
		if feature["type"] != "Feature" {
			continue
		}

		properties, ok := feature["properties"].(map[string]any)
		if !ok {
			continue
		}

		// Extract location data
		loc := Location{
			Location:       getString(properties, "name"),
			ShortName:      getString(properties, "short_name"),
			Address:        getString(properties, "address"),
			Zipcode:        getString(properties, "zipcode"),
			Town:           getString(properties, "town"),
			Country:        getString(properties, "country"),
			CountryCode:    getString(properties, "country_code"),
			Region:         getString(properties, "region"),
			Internetsite:   getString(properties, "internetsite"),
			OsmType:        getString(properties, "osm_type"),
			WikidataID:     getString(properties, "wikidata_id"),
			MBPlaceID:      getString(properties, "mb_place_id"),
			Geohash:        getString(properties, "geohash"),
			NotesMd:        getString(properties, "notes"),
			Parking:        getString(properties, "parking"),
			FloorCondition: getString(properties, "floor_condition"),
			CreatedAt:      getString(properties, "created_at"),
		}

		// Extract numeric/pointer fields
		if osmID := getInt(properties, "osm_id"); osmID != 0 {
			id64 := int64(osmID)
			loc.OsmID = &id64
		}
		if lat := getFloat(properties, "latitude"); lat != 0 {
			loc.Latitude = &lat
		}
		if lon := getFloat(properties, "longitude"); lon != 0 {
			loc.Longitude = &lon
		}
		if updatedAt := getInt64(properties, "updated_at"); updatedAt != 0 {
			loc.UpdatedAt = updatedAt
		}

		// Extract organization IDs
		if orgIDs, ok := properties["organization_ids"].([]any); ok {
			for _, orgID := range orgIDs {
				if id, ok := orgID.(float64); ok {
					loc.OrganizationIDs = append(loc.OrganizationIDs, int(id))
				}
			}
		}

		// Check for existing location using multiple deduplication strategies
		existingID, _ := findExistingLocation(loc)

		if existingID > 0 {
			// Update existing location
			if err := updateLocation(existingID, loc); err != nil {
				skipped++
				continue
			}
			updated++
			// Update organization assignments
			syncLocationOrgs(existingID, loc.OrganizationIDs)
		} else {
			// Insert new location
			if err := insertLocation(loc); err != nil {
				skipped++
				continue
			}
			imported++
		}
	}

	return adminResponse{OK: true, Data: map[string]int{
		"imported": imported,
		"updated":  updated,
		"skipped":  skipped,
	}}
}

// Helper functions for import
func getString(m map[string]any, key string) string {
	if val, ok := m[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if val, ok := m[key]; ok {
		if f, ok := val.(float64); ok {
			return int(f)
		}
	}
	return 0
}

func getInt64(m map[string]any, key string) int64 {
	if val, ok := m[key]; ok {
		if f, ok := val.(float64); ok {
			return int64(f)
		}
	}
	return 0
}

func getFloat(m map[string]any, key string) float64 {
	if val, ok := m[key]; ok {
		if f, ok := val.(float64); ok {
			return f
		}
	}
	return 0
}

func findExistingLocation(loc Location) (int, string) {
	// Check by OSM ID (most reliable)
	if loc.OsmID != nil && *loc.OsmID > 0 {
		var id int
		if err := db.QueryRow("SELECT id FROM locations WHERE osm_id = ?", loc.OsmID).Scan(&id); err == nil {
			return id, "osm_id"
		}
	}

	// Check by Wikidata ID
	if loc.WikidataID != "" {
		var id int
		if err := db.QueryRow("SELECT id FROM locations WHERE wikidata_id = ?", loc.WikidataID).Scan(&id); err == nil {
			return id, "wikidata_id"
		}
	}

	// Check by MusicBrainz Place ID
	if loc.MBPlaceID != "" {
		var id int
		if err := db.QueryRow("SELECT id FROM locations WHERE mb_place_id = ?", loc.MBPlaceID).Scan(&id); err == nil {
			return id, "mb_place_id"
		}
	}

	// Check by exact name + address + coordinates (for locations without external IDs)
	if loc.Location != "" && (loc.Address != "" || (loc.Latitude != nil && loc.Longitude != nil)) {
		query := "SELECT id FROM locations WHERE location = ?"
		params := []any{loc.Location}

		if loc.Address != "" {
			query += " AND address = ?"
			params = append(params, loc.Address)
		}

		if loc.Latitude != nil && loc.Longitude != nil {
			query += " AND latitude = ? AND longitude = ?"
			params = append(params, *loc.Latitude, *loc.Longitude)
		}

		var id int
		if err := db.QueryRow(query, params...).Scan(&id); err == nil {
			return id, "name_address_coords"
		}
	}

	return 0, ""
}

func updateLocation(id int, loc Location) error {
	query := `UPDATE locations SET 
		location = ?,
		short_name = ?,
		address = ?,
		zipcode = ?,
		town = ?,
		country = ?,
		country_code = ?,
		region = ?,
		latitude = ?,
		longitude = ?,
		internetsite = ?,
		osm_id = ?,
		osm_type = ?,
		wikidata_id = ?,
		mb_place_id = ?,
		geohash = ?,
		notes_md = ?,
		parking = ?,
		floor_condition = ?,
		updated_at = ?
		WHERE id = ?`

	_, err := db.Exec(query,
		loc.Location,
		loc.ShortName,
		loc.Address,
		loc.Zipcode,
		loc.Town,
		loc.Country,
		loc.CountryCode,
		loc.Region,
		loc.Latitude,
		loc.Longitude,
		loc.Internetsite,
		loc.OsmID,
		loc.OsmType,
		loc.WikidataID,
		loc.MBPlaceID,
		loc.Geohash,
		loc.NotesMd,
		loc.Parking,
		loc.FloorCondition,
		time.Now().Unix(),
		id,
	)
	return err
}

func insertLocation(loc Location) error {
	query := `INSERT INTO locations (
		location, short_name, address, zipcode, town, country, country_code, region,
		latitude, longitude, internetsite, osm_id, osm_type, wikidata_id, mb_place_id,
		geohash, notes_md, parking, floor_condition, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := db.Exec(query,
		loc.Location,
		loc.ShortName,
		loc.Address,
		loc.Zipcode,
		loc.Town,
		loc.Country,
		loc.CountryCode,
		loc.Region,
		loc.Latitude,
		loc.Longitude,
		loc.Internetsite,
		loc.OsmID,
		loc.OsmType,
		loc.WikidataID,
		loc.MBPlaceID,
		loc.Geohash,
		loc.NotesMd,
		loc.Parking,
		loc.FloorCondition,
		time.Now().Format("2006-01-02 15:04:05"),
		time.Now().Unix(),
	)
	return err
}
