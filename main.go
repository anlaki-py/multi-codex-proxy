package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"multi-codex-proxy/internal/accounts"
	"multi-codex-proxy/internal/config"
	"multi-codex-proxy/internal/proxy"
	"multi-codex-proxy/internal/tui"
)

func main() {
	serve := flag.Bool("serve", false, "run headless proxy without TUI")
	port := flag.Int("port", 0, "override port from config")
	flag.Parse()

	cfg, dir, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config: "+err.Error())
		os.Exit(1)
	}
	if *port > 0 {
		if *port < 1 || *port > 65535 {
			fmt.Fprintln(os.Stderr, "port out of range 1-65535")
			os.Exit(1)
		}
		cfg.Port = *port
	}

	client := proxy.NewClient()
	repo, err := accounts.NewRepository(accounts.NewStore(dir), client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "accounts: "+err.Error())
		os.Exit(1)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := proxy.NewServer(repo, client, func(f string, a ...any) {
		fmt.Printf(f+"\n", a...)
	})

	if *serve {
		httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}
		fmt.Printf("multi-codex-proxy on http://%s\n", addr)
		fmt.Printf("config dir: %s\n", dir)
		fmt.Println("endpoints: GET /health, GET /v1/models, POST /v1/responses, POST /v1/chat/completions")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "serve: "+err.Error())
			os.Exit(1)
		}
		return
	}

	model := tui.New(repo, srv, client, addr)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui: "+err.Error())
		os.Exit(1)
	}
}
