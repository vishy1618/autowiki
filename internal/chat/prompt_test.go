package chat

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_MentionsWritePages(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "write_pages") {
		t.Errorf("system prompt missing %q", "write_pages")
	}
}

func TestBuildSystemPrompt_MentionsPatchPage(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "patch_page") {
		t.Errorf("system prompt missing %q", "patch_page")
	}
}

func TestBuildSystemPrompt_MentionsAppendToSection(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "append_to_section") {
		t.Errorf("system prompt missing %q", "append_to_section")
	}
}

func TestBuildSystemPrompt_HierarchicalIndexInstructions(t *testing.T) {
	tests := []struct {
		name    string
		wantStr string
	}{
		{
			name:    "instructs subdirectory organisation",
			wantStr: "subdirector",
		},
		{
			name:    "instructs updating containing directory index on write",
			wantStr: "that directory's index.md",
		},
		{
			name:    "restricts root index update to subdirectory changes",
			wantStr: "root index.md only when adding or removing a subdirectory",
		},
		{
			name:    "instructs read_page on subdirectory index for navigation",
			wantStr: "read_page on its index.md",
		},
		{
			name:    "does not use flat Map of Content instruction",
			wantStr: "",
		},
	}
	got := buildSystemPrompt("", "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "does not use flat Map of Content instruction" {
				if strings.Contains(got, "Map of Content") {
					t.Errorf("system prompt should not contain %q", "Map of Content")
				}
				return
			}
			if !strings.Contains(got, tt.wantStr) {
				t.Errorf("system prompt missing %q", tt.wantStr)
			}
		})
	}
}

func TestBuildSystemPrompt_DownloadLinkInstructions(t *testing.T) {
	tests := []struct {
		name    string
		wantStr string
	}{
		{
			name:    "includes download link URL pattern",
			wantStr: "/api/vault/files/",
		},
		{
			name:    "instructs proactive binary file links",
			wantStr: "proactively offer a download link",
		},
		{
			name:    "instructs text files only on explicit request",
			wantStr: "only offer a download link when the user explicitly asks to download",
		},
		{
			name:    "instructs to use list_vault or search_vault when path unknown",
			wantStr: "list_vault or search_vault",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSystemPrompt("", "")
			if !strings.Contains(got, tt.wantStr) {
				t.Errorf("system prompt missing %q", tt.wantStr)
			}
		})
	}
}
