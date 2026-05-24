package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

type smtpConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	From        string `json:"from"`
	FromName    string `json:"from_name"`
	TLS         string `json:"tls"`
	TimeoutSecs int    `json:"timeout_secs"`
	PasswordSet bool   `json:"password_set"`
}

func cmdSMTPShow(_ []string) {
	resp := send(socketPath, request{Cmd: "smtp-get"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var cfg smtpConfig
	json.Unmarshal(resp.Data, &cfg)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "host\t%s\n", cfg.Host)
	fmt.Fprintf(tw, "port\t%d\n", cfg.Port)
	fmt.Fprintf(tw, "username\t%s\n", cfg.Username)
	fmt.Fprintf(tw, "from\t%s\n", cfg.From)
	fmt.Fprintf(tw, "from_name\t%s\n", cfg.FromName)
	fmt.Fprintf(tw, "tls\t%s\n", cfg.TLS)
	fmt.Fprintf(tw, "timeout_secs\t%d\n", cfg.TimeoutSecs)
	fmt.Fprintf(tw, "password_set\t%v\n", cfg.PasswordSet)
	tw.Flush()
}

// syncWebSMTPHost reads web.yaml and sets smtp_host to match the API config.
// If web.yaml doesn't exist, it does nothing. Errors are printed as warnings.
func syncWebSMTPHost(webPath, host string) {
	data, err := os.ReadFile(webPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read %s: %v\n", webPath, err)
		return
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "smtp_host:") {
			if host == "" {
				lines[i] = "#smtp_host:"
			} else {
				lines[i] = "smtp_host: " + host
			}
			found = true
			break
		}
	}
	if !found && host != "" {
		lines = append(lines, "smtp_host: "+host)
	}
	if err := os.WriteFile(webPath, []byte(strings.Join(lines, "\n")), 0640); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write %s: %v\n", webPath, err)
		return
	}
	fmt.Printf("updated smtp_host in %s\n", webPath)
}

func cmdSMTPSet(args []string) {
	fs := flag.NewFlagSet("smtp-set", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["smtp-set"]) }
	host := fs.String("host", "", "SMTP server hostname")
	port := fs.Int("port", 0, "SMTP server port")
	username := fs.String("username", "", "SMTP account username")
	from := fs.String("from", "", "envelope From address")
	fromName := fs.String("from-name", "", "display name in From header")
	tlsMode := fs.String("tls", "", "TLS mode: starttls, tls, none")
	timeout := fs.Int("timeout", 0, "dial and send timeout in seconds (default 30)")
	webConfig := fs.String("web-config", defaultWebConfigPath, "path to dansal-web config YAML")
	fs.Parse(args)

	if *host == "" && *port == 0 && *username == "" && *from == "" && *fromName == "" && *tlsMode == "" && *timeout == 0 {
		die("at least one flag is required (see smtp-set --help)")
	}

	resp := send(socketPath, request{
		Cmd:             "smtp-set",
		SMTPHost:        *host,
		SMTPPort:        *port,
		SMTPUsername:    *username,
		SMTPFrom:        *from,
		SMTPFromName:    *fromName,
		SMTPTLS:         *tlsMode,
		SMTPTimeoutSecs: *timeout,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("SMTP settings updated.")

	// Re-fetch the live config so we sync the actual stored host (not just --host flag).
	cfgResp := send(socketPath, request{Cmd: "smtp-get"})
	if cfgResp.OK {
		var cfg smtpConfig
		json.Unmarshal(cfgResp.Data, &cfg)
		syncWebSMTPHost(*webConfig, cfg.Host)

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "host\t%s\n", cfg.Host)
		fmt.Fprintf(tw, "port\t%d\n", cfg.Port)
		fmt.Fprintf(tw, "username\t%s\n", cfg.Username)
		fmt.Fprintf(tw, "from\t%s\n", cfg.From)
		fmt.Fprintf(tw, "from_name\t%s\n", cfg.FromName)
		fmt.Fprintf(tw, "tls\t%s\n", cfg.TLS)
		fmt.Fprintf(tw, "timeout_secs\t%d\n", cfg.TimeoutSecs)
		fmt.Fprintf(tw, "password_set\t%v\n", cfg.PasswordSet)
		tw.Flush()
	} else {
		cmdSMTPShow(nil)
	}
}

func cmdSMTPSetPassword(args []string) {
	fs := flag.NewFlagSet("smtp-set-password", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["smtp-set-password"]) }
	password := fs.String("password", "", "SMTP account password (prompted if omitted)")
	fs.Parse(args)

	pw := *password
	if pw == "" {
		b, err := promptPassword("SMTP password: ")
		if err != nil {
			die("prompt: %v", err)
		}
		pw = string(b)
	}

	resp := send(socketPath, request{Cmd: "smtp-set-password", Password: pw})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("SMTP password updated.")
}

func cmdSMTPTest(args []string) {
	fs := flag.NewFlagSet("smtp-test", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["smtp-test"]) }
	to := fs.String("to", "", "recipient email address")
	fs.Parse(args)

	if *to == "" {
		die("--to is required")
	}

	// Fetch and display current config so the user knows what is being tested.
	cfgResp := send(socketPath, request{Cmd: "smtp-get"})
	if !cfgResp.OK {
		die("could not read SMTP config: %s", cfgResp.Error)
	}
	var cfg smtpConfig
	json.Unmarshal(cfgResp.Data, &cfg)

	port := cfg.Port
	if port == 0 {
		port = 587
	}
	tls := cfg.TLS
	if tls == "" {
		tls = "starttls"
	}
	timeout := cfg.TimeoutSecs
	if timeout == 0 {
		timeout = 30
	}

	fmt.Println("SMTP configuration:")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  host\t%s:%d\n", cfg.Host, port)
	fmt.Fprintf(tw, "  tls\t%s\n", tls)
	fmt.Fprintf(tw, "  username\t%s\n", cfg.Username)
	fmt.Fprintf(tw, "  from\t%s\n", cfg.From)
	fmt.Fprintf(tw, "  timeout\t%ds\n", timeout)
	fmt.Fprintf(tw, "  password_set\t%v\n", cfg.PasswordSet)
	tw.Flush()
	fmt.Printf("\nSending test email to %s...\n", *to)

	resp := send(socketPath, request{Cmd: "smtp-test", SMTPTo: *to})
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Println("ok")
}
