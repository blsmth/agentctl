package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/dashboards"
)

// RunDashboards dispatches agentctl dashboards <subcommand>.
func RunDashboards() {
	sub := ""
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}
	switch sub {
	case "list":
		cmdDashboardsList()
	case "export":
		cmdDashboardsExport()
	case "install":
		cmdDashboardsInstall()
	default:
		fmt.Fprintf(os.Stderr, "Usage: agentctl dashboards <list|export|install>\n")
		fmt.Fprintf(os.Stderr, "  list                              list available dashboards\n")
		fmt.Fprintf(os.Stderr, "  export [--dir ./dashboards]       write JSON files to disk\n")
		fmt.Fprintf(os.Stderr, "  install --url <url> --token <key> push to Grafana via API\n")
		os.Exit(1)
	}
}

// cmdDashboardsList prints all embedded dashboard filenames.
func cmdDashboardsList() {
	for _, name := range dashboards.List() {
		fmt.Println(name)
	}
}

// cmdDashboardsExport writes every embedded dashboard to --dir (default: ./dashboards).
func cmdDashboardsExport() {
	dir := "./dashboards"
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		if (args[i] == "--dir" || args[i] == "-dir") && i+1 < len(args) {
			dir = args[i+1]
			i++
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %q: %v\n", dir, err)
		os.Exit(1)
	}

	for _, name := range dashboards.List() {
		data, err := dashboards.Get(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading dashboard %q: %v\n", name, err)
			os.Exit(1)
		}
		dest := filepath.Join(dir, name)
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %q: %v\n", dest, err)
			os.Exit(1)
		}
		fmt.Printf("exported %s\n", dest)
	}
}

// grafanaImportPayload mirrors the Grafana Dashboard API request body.
type grafanaImportPayload struct {
	Dashboard json.RawMessage `json:"dashboard"`
	Overwrite bool            `json:"overwrite"`
	FolderID  int             `json:"folderId"`
}

// cmdDashboardsInstall pushes all embedded dashboards to a Grafana instance.
func cmdDashboardsInstall() {
	var grafanaURL, token string
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url", "-url":
			if i+1 < len(args) {
				grafanaURL = args[i+1]
				i++
			}
		case "--token", "-token":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		}
	}

	if grafanaURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --url is required")
		os.Exit(1)
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: --token is required")
		os.Exit(1)
	}

	grafanaURL = strings.TrimRight(grafanaURL, "/")
	client := &http.Client{Timeout: 30 * time.Second}

	for _, name := range dashboards.List() {
		data, err := dashboards.Get(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading dashboard %q: %v\n", name, err)
			os.Exit(1)
		}

		payload := grafanaImportPayload{
			Dashboard: json.RawMessage(data),
			Overwrite: true,
			FolderID:  0,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshalling payload for %q: %v\n", name, err)
			os.Exit(1)
		}

		apiURL := grafanaURL + "/api/dashboards/db"
		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building request for %q: %v\n", name, err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error posting dashboard %q: %v\n", name, err)
			os.Exit(1)
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "Grafana API error for %q (status %d): %s\n", name, resp.StatusCode, string(respBody))
			os.Exit(1)
		}
		fmt.Printf("installed %s (status %d)\n", name, resp.StatusCode)
	}
}
