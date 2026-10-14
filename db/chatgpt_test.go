package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatGPTProfileRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p := &LLMProfile{Name: "test-chatgpt-roundtrip", Format: "openai-responses", Model: "gpt-6-sol", ChatGPTAccountID: "local-account-ref", APIKey: "must-not-store", Streaming: true}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec("DELETE FROM llm_profiles WHERE id=$1", id)
	p, err = d.ProfileByID(id)
	if err != nil || p.ChatGPTAccountID != "local-account-ref" || p.APIKey != "" {
		t.Fatal(p, err)
	}
	p.Model = "another-model"
	if _, err = d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = r.ChatGPTAccountID == "local-account-ref"
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "must-not-store") {
				t.Fatal("secret leak")
			}
		}
	}
	if !found {
		t.Fatal("list lost account reference")
	}
	p.ChatGPTAccountID = ""
	p.APIKey = "real-test-key"
	if _, err = d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(id)
	if err != nil || p.ChatGPTAccountID != "" || p.APIKey != "real-test-key" {
		t.Fatal(p, err)
	}
	p.ChatGPTAccountID = "second-local-ref"
	p.APIKey = ""
	if _, err = d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(id)
	if err != nil || p.APIKey != "" || p.ChatGPTAccountID != "second-local-ref" {
		t.Fatal(p, err)
	}
}
