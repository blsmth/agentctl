package config

import (
	"encoding/json"
	"os"
	"testing"
)

const accountsGlobal = `{
	"accounts": {
		"ll": {"config_dir": "~/.claude", "label": "Lincoln Loop"},
		"personal": {"config_dir": "~/.claude-personal"}
	},
	"default_account": "ll",
	"account_rules": [{"path": "~/code/personal/**", "account": "personal"}],
	"agents": {"brandon": {"account": "personal"}, "plain": {}}
}`

func TestGlobalAccountsParse(t *testing.T) {
	cfg := loadWith(t, accountsGlobal, `{}`)
	if !cfg.HasAccounts() || len(cfg.Accounts) != 2 {
		t.Fatalf("accounts = %+v", cfg.Accounts)
	}
	if cfg.Accounts["ll"].Label != "Lincoln Loop" {
		t.Errorf("label = %q", cfg.Accounts["ll"].Label)
	}
	if cfg.DefaultAccount != "ll" || len(cfg.AccountRules) != 1 {
		t.Errorf("default=%q rules=%+v", cfg.DefaultAccount, cfg.AccountRules)
	}
	if cfg.Agents["brandon"].Account != "personal" {
		t.Errorf("agent account not loaded")
	}
}

// A cloned repo must not be able to route itself onto another account.
func TestProjectAccountKeysIgnored(t *testing.T) {
	project := `{
		"accounts": {"evil": {"config_dir": "/tmp/evil"}, "ll": {"config_dir": "/tmp/evil"}},
		"default_account": "personal",
		"account_rules": [{"path": "/", "account": "personal"}],
		"agents": {"brandon": {"account": "ll"}, "plain": {"account": "personal"}, "newone": {"account": "personal"}}
	}`
	cfg := loadWith(t, accountsGlobal, project)
	if _, ok := cfg.Accounts["evil"]; ok {
		t.Error("project account evil must be ignored")
	}
	if cfg.Accounts["ll"].ConfigDir != "~/.claude" {
		t.Errorf("project overrode global account: %+v", cfg.Accounts["ll"])
	}
	if cfg.DefaultAccount != "ll" {
		t.Errorf("default_account = %q, want global ll", cfg.DefaultAccount)
	}
	if len(cfg.AccountRules) != 1 || cfg.AccountRules[0].Path != "~/code/personal/**" {
		t.Errorf("account_rules = %+v, want global only", cfg.AccountRules)
	}
	for name, want := range map[string]string{"brandon": "personal", "plain": "", "newone": ""} {
		if got := cfg.Agents[name].Account; got != want {
			t.Errorf("agents.%s.account = %q, want %q", name, got, want)
		}
	}
	for _, key := range []string{`"accounts"`, `"default_account"`, `"account_rules"`, "agents.brandon.account"} {
		if !warnedContaining(key) {
			t.Errorf("expected a warning naming %s", key)
		}
	}
}

func TestAccountValidation(t *testing.T) {
	tests := []struct {
		name, json string
		ok         bool
	}{
		{"tilde dir", `{"accounts":{"a":{"config_dir":"~/.c"}}}`, true},
		{"absolute dir", `{"accounts":{"a":{"config_dir":"/x/.c"}}}`, true},
		{"relative dir", `{"accounts":{"a":{"config_dir":"rel/.c"}}}`, false},
		{"missing dir", `{"accounts":{"a":{"label":"x"}}}`, false},
		{"bad name", `{"accounts":{"a/b":{"config_dir":"/x"}}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, tt.json, `{}`)
			if got := len(cfg.Accounts) == 1; got != tt.ok {
				t.Errorf("kept = %v, want %v (%+v)", got, tt.ok, cfg.Accounts)
			}
		})
	}
}

func TestSetAndRemoveAccount(t *testing.T) {
	gp, _ := writeLayers(t, `{"default_account":"p","other_key":1}`, `{}`)
	if err := SetAccount("p", AccountConfig{ConfigDir: "~/.claude-p", Label: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccount("bad name", AccountConfig{ConfigDir: "/x"}); err == nil {
		t.Error("expected invalid name to be rejected")
	}
	if err := SetAccount("q", AccountConfig{ConfigDir: "rel"}); err == nil {
		t.Error("expected relative dir to be rejected")
	}
	cfg := Load()
	if cfg.Accounts["p"].ConfigDir != "~/.claude-p" {
		t.Fatalf("accounts = %+v", cfg.Accounts)
	}
	found, err := RemoveAccount("p")
	if err != nil || !found {
		t.Fatalf("RemoveAccount = %v, %v", found, err)
	}
	if found, _ := RemoveAccount("p"); found {
		t.Error("second remove reported found")
	}
	data, err := os.ReadFile(gp)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["accounts"]; ok {
		t.Errorf("accounts still present: %s", data)
	}
	if _, ok := raw["default_account"]; ok {
		t.Errorf("default_account naming the removed account was kept: %s", data)
	}
	if _, ok := raw["other_key"]; !ok {
		t.Errorf("unknown key dropped: %s", data)
	}
}
