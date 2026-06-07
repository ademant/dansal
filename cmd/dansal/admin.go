package main

import (
	"bufio"
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
)

type adminRequest struct {
	Cmd                   string `json:"cmd"`
	Email                 string `json:"email,omitempty"`
	Password              string `json:"password,omitempty"`
	Role                  string `json:"role,omitempty"`
	OrgID                 int    `json:"org_id,omitempty"`
	Path                  string `json:"path,omitempty"`
	Since                 string `json:"since,omitempty"`
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

func dispatchAdminCmd(req adminRequest) adminResponse {
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
	case "list-backups":
		return adminListBackups(req)
	case "magic-link":
		return adminMagicLink(req)
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
		events, _, fetchErr := importFromSource(src)
		r := sourceResult{ID: src.ID, URL: src.URL}
		if fetchErr != nil {
			r.Error = fetchErr.Error()
			db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", "error: "+fetchErr.Error(), src.ID)
		} else {
			r.Events = len(events)
			db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", fmt.Sprintf("%d", len(events)), src.ID)
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
	base := config.Server.BaseURL
	if base == "" {
		base = fmt.Sprintf("http://localhost%s", getPort())
	}
	url := base + "/login/magic/" + token
	return adminResponse{OK: true, Data: map[string]string{"url": url}}
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
	if _, err := SendEmail(req.SMTPTo, "Dansal SMTP Test", "This is a test email sent by Dansal to verify SMTP configuration."); err != nil {
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
