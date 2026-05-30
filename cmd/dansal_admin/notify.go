package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

type telegramCfg struct {
	BotToken string `json:"bot_token"`
	BotName  string `json:"bot_name"`
}

type matrixCfg struct {
	Homeserver     string `json:"homeserver"`
	AccessTokenSet bool   `json:"has_token"`
}

type heartbeatChannelStatus struct {
	Configured  bool   `json:"configured"`
	OK          bool   `json:"ok"`
	LastChecked string `json:"last_checked"`
	Error       string `json:"error,omitempty"`
}

type heartbeatStatus struct {
	Email    heartbeatChannelStatus `json:"email"`
	Telegram heartbeatChannelStatus `json:"telegram"`
	Matrix   heartbeatChannelStatus `json:"matrix"`
}

func cmdTelegramShow(_ []string) {
	resp := send(socketPath, request{Cmd: "telegram-get"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var cfg telegramCfg
	json.Unmarshal(resp.Data, &cfg)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "bot_token\t%s\n", cfg.BotToken)
	fmt.Fprintf(tw, "bot_name\t%s\n", cfg.BotName)
	tw.Flush()
}

func cmdTelegramSet(args []string) {
	fs := flag.NewFlagSet("telegram-set", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["telegram-set"]) }
	token := fs.String("token", "", "Telegram bot token")
	name := fs.String("name", "", "Telegram bot name")
	fs.Parse(args)
	if *token == "" && *name == "" {
		die("at least one flag is required (see telegram-set --help)")
	}
	resp := send(socketPath, request{
		Cmd:              "telegram-set",
		TelegramBotToken: *token,
		TelegramBotName:  *name,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("Telegram settings updated.")
	cmdTelegramShow(nil)
}

func cmdMatrixShow(_ []string) {
	resp := send(socketPath, request{Cmd: "matrix-get"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var cfg matrixCfg
	json.Unmarshal(resp.Data, &cfg)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "homeserver\t%s\n", cfg.Homeserver)
	tokenSet := "no"
	if cfg.AccessTokenSet {
		tokenSet = "yes"
	}
	fmt.Fprintf(tw, "access_token_set\t%s\n", tokenSet)
	tw.Flush()
}

func cmdMatrixSet(args []string) {
	fs := flag.NewFlagSet("matrix-set", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["matrix-set"]) }
	homeserver := fs.String("homeserver", "", "Matrix homeserver URL")
	token := fs.String("token", "", "Matrix access token")
	fs.Parse(args)
	if *homeserver == "" && *token == "" {
		die("at least one flag is required (see matrix-set --help)")
	}
	resp := send(socketPath, request{
		Cmd:               "matrix-set",
		MatrixHomeserver:  *homeserver,
		MatrixAccessToken: *token,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("Matrix settings updated.")
	cmdMatrixShow(nil)
}

func cmdMatrixLogin(args []string) {
	fs := flag.NewFlagSet("matrix-login", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["matrix-login"]) }
	homeserver := fs.String("homeserver", "", "Matrix homeserver URL")
	username := fs.String("username", "", "Matrix username")
	password := fs.String("password", "", "Matrix password (prompted if omitted)")
	fs.Parse(args)
	if *homeserver == "" || *username == "" {
		die("--homeserver and --username are required")
	}
	pw := *password
	if pw == "" {
		b, err := promptPassword("Matrix password: ")
		if err != nil {
			die("prompt: %v", err)
		}
		pw = string(b)
	}
	resp := send(socketPath, request{
		Cmd:              "matrix-login",
		MatrixHomeserver: *homeserver,
		MatrixUsername:   *username,
		MatrixPassword:   pw,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Println("Matrix login successful. Access token stored.")
}

func cmdHeartbeatShow(_ []string) {
	resp := send(socketPath, request{Cmd: "heartbeat-get"})
	if !resp.OK {
		die("%s", resp.Error)
	}
	var status heartbeatStatus
	json.Unmarshal(resp.Data, &status)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	printChannel := func(name string, ch heartbeatChannelStatus) {
		state := "ok"
		if !ch.Configured {
			state = "not configured"
		} else if !ch.OK {
			state = "fail"
		}
		errStr := ""
		if ch.Error != "" {
			errStr = "  (" + ch.Error + ")"
		}
		fmt.Fprintf(tw, "%s\t%s%s\n", name, state, errStr)
	}
	printChannel("email", status.Email)
	printChannel("telegram", status.Telegram)
	printChannel("matrix", status.Matrix)
	tw.Flush()
}

func cmdHeartbeatSet(args []string) {
	fs := flag.NewFlagSet("heartbeat-set", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["heartbeat-set"]) }
	interval := fs.Int("interval", 0, "heartbeat check interval in minutes")
	fs.Parse(args)
	if *interval <= 0 {
		die("--interval must be a positive number of minutes")
	}
	resp := send(socketPath, request{
		Cmd:                   "heartbeat-set",
		HeartbeatIntervalMins: *interval,
	})
	if !resp.OK {
		die("%s", resp.Error)
	}
	fmt.Printf("Heartbeat interval set to %d minutes.\n", *interval)
}
