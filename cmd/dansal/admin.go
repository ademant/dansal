package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net"
	"os"
)

type adminRequest struct {
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
	SMTPTo               string `json:"smtp_to,omitempty"`
	TelegramBotToken     string `json:"telegram_bot_token,omitempty"`
	TelegramBotName      string `json:"telegram_bot_name,omitempty"`
	MatrixHomeserver     string `json:"matrix_homeserver,omitempty"`
	MatrixAccessToken    string `json:"matrix_access_token,omitempty"`
	MatrixUsername       string `json:"matrix_username,omitempty"`
	MatrixPassword       string `json:"matrix_password,omitempty"`
	HeartbeatIntervalMins int   `json:"heartbeat_interval_mins,omitempty"`
}

type adminResponse struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Data  any `json:"data,omitempty"`
}

func startAdminSocket(path string) net.Listener {
	if path == "" {
		path = "./dansal.sock"
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
	case "heartbeat-get":
		return adminHeartbeatGet()
	case "heartbeat-set":
		return adminHeartbeatSet(req)
	default:
		return adminResponse{OK: false, Error: "unknown command: " + req.Cmd}
	}
}

func adminListUsers() adminResponse {
	rows, err := db.Query("SELECT id, username, email, role, COALESCE(disabled,0), created_at FROM users ORDER BY id")
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &disabled, &u.CreatedAt); err != nil {
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
	if req.Username == "" {
		rows, err = db.Query(
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links ORDER BY created_at DESC",
		)
	} else {
		userID, lookupErr := userIDByUsername(req.Username)
		if lookupErr != nil {
			return adminResponse{OK: false, Error: "user not found"}
		}
		rows, err = db.Query(
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links WHERE created_by=? ORDER BY created_at DESC",
			userID,
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

func adminListSessions(req adminRequest) adminResponse {
	if req.Username == "" {
		return adminResponse{OK: false, Error: "username is required"}
	}
	userID, err := userIDByUsername(req.Username)
	if err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
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
	if req.Username == "" {
		return adminResponse{OK: false, Error: "username is required"}
	}
	result, err := db.Exec(
		"UPDATE users SET disabled=0, failed_login_count=0, failed_login_since=NULL WHERE username=?",
		req.Username,
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
	if req.Username == "" {
		return adminResponse{OK: false, Error: "username is required"}
	}
	var userID int
	var role string
	if err := db.QueryRow("SELECT id, role FROM users WHERE username=?", req.Username).Scan(&userID, &role); err != nil {
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
	if req.Username == "" || req.Email == "" || req.Password == "" {
		return adminResponse{OK: false, Error: "username, email and password are required"}
	}
	role := req.Role
	if role == "" {
		role = RoleUser
	}
	if !validateRole(role) {
		return adminResponse{OK: false, Error: "invalid role: use admin, user, publisher or viewer"}
	}
	result, err := db.Exec(
		"INSERT INTO users (username, email, password_hash, role, telegram, matrix) VALUES (?, ?, ?, ?, ?, ?)",
		req.Username, req.Email, hashPassword(req.Password), role, req.Telegram, req.Matrix,
	)
	if err != nil {
		return adminResponse{OK: false, Error: "username or email already exists"}
	}
	id, _ := result.LastInsertId()
	return adminResponse{OK: true, Data: User{ID: int(id), Username: req.Username, Email: req.Email, Role: role, Telegram: req.Telegram, Matrix: req.Matrix}}
}

func adminDeleteUser(req adminRequest) adminResponse {
	if req.Username == "" {
		return adminResponse{OK: false, Error: "username is required"}
	}
	var userID int
	var role string
	if err := db.QueryRow("SELECT id, role FROM users WHERE username = ?", req.Username).Scan(&userID, &role); err != nil {
		return adminResponse{OK: false, Error: "user not found"}
	}
	if role == RoleAdmin {
		return adminResponse{OK: false, Error: "cannot delete admin users"}
	}
	db.Exec("DELETE FROM users WHERE id = ?", userID)
	return adminResponse{OK: true}
}

func adminSetPassword(req adminRequest) adminResponse {
	if req.Username == "" || req.Password == "" {
		return adminResponse{OK: false, Error: "username and password are required"}
	}
	result, err := db.Exec("UPDATE users SET password_hash = ? WHERE username = ?",
		hashPassword(req.Password), req.Username)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return adminResponse{OK: false, Error: "user not found"}
	}
	return adminResponse{OK: true}
}

func adminSetRole(req adminRequest) adminResponse {
	if req.Username == "" || req.Role == "" {
		return adminResponse{OK: false, Error: "username and role are required"}
	}
	if !validateRole(req.Role) {
		return adminResponse{OK: false, Error: "invalid role: use admin, user, publisher or viewer"}
	}
	result, err := db.Exec("UPDATE users SET role = ? WHERE username = ?", req.Role, req.Username)
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
		SELECT om.organization_id, om.user_id, u.username, om.created_at
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
		if err := rows.Scan(&m.OrganizationID, &m.UserID, &m.Username, &m.CreatedAt); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
		members = append(members, m)
	}
	return adminResponse{OK: true, Data: members}
}

func adminAddMember(req adminRequest) adminResponse {
	if req.OrgID == 0 || req.Username == "" {
		return adminResponse{OK: false, Error: "org_id and username are required"}
	}
	userID, err := userIDByUsername(req.Username)
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
	if req.SMTPHost != "" {
		config.SMTP.Host = req.SMTPHost
	}
	if req.SMTPPort != 0 {
		config.SMTP.Port = req.SMTPPort
	}
	if req.SMTPUsername != "" {
		config.SMTP.Username = req.SMTPUsername
	}
	if req.SMTPFrom != "" {
		config.SMTP.From = req.SMTPFrom
	}
	if req.SMTPFromName != "" {
		config.SMTP.FromName = req.SMTPFromName
	}
	if req.SMTPTLS != "" {
		config.SMTP.TLS = req.SMTPTLS
	}
	if req.SMTPTimeoutSecs != 0 {
		config.SMTP.TimeoutSecs = req.SMTPTimeoutSecs
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
	if err := SendEmail(req.SMTPTo, "Dansal SMTP Test", "This is a test email sent by Dansal to verify SMTP configuration."); err != nil {
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
	}
}

func adminRemoveMember(req adminRequest) adminResponse {
	if req.OrgID == 0 || req.Username == "" {
		return adminResponse{OK: false, Error: "org_id and username are required"}
	}
	userID, err := userIDByUsername(req.Username)
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
