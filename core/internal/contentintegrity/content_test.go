package contentintegrity

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFrozenVectors(t *testing.T) {
	data, err := os.ReadFile("../../fixtures/content-integrity-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name        string
		Content     Content
		EncodingHex string
		SHA256      string
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			b, err := Encode(v.Content)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(b) != v.EncodingHex {
				t.Fatal("encoding mismatch")
			}
			d, err := Digest(v.Content)
			if err != nil || hex.EncodeToString(d[:]) != v.SHA256 {
				t.Fatal("digest mismatch", err)
			}
			for _, field := range []string{"author", "workspace", "project", "ticket", "title", "intent", "destination", "summary", "markdown"} {
				c := v.Content
				s := "changed"
				switch field {
				case "author":
					c.ExpectedAuthorUserID += s
				case "workspace":
					c.WorkspaceID += s
				case "project":
					c.ProjectID = &s
				case "ticket":
					c.ExistingTicketID = &s
				case "title":
					c.Title += s
				case "intent":
					c.OriginalDestinationIntent += s
				case "destination":
					c.Destination = Destination{Kind: "linked", MemberID: s}
				case "summary":
					c.Summary += s
				case "markdown":
					c.ContextMarkdown += s
				}
				changed, err := Digest(c)
				if err != nil || changed == d {
					t.Fatal("tamper undetected", field, err)
				}
			}
		})
	}
	c := vectors[0].Content
	for _, invalid := range []string{string([]byte{0xff}), strings.Repeat("x", 8193), strings.Repeat("é", 4097)} {
		c.Summary = invalid
		if _, err := Digest(c); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	c = vectors[0].Content
	c.FormatVersion = 2
	if _, err := Digest(c); err == nil {
		t.Fatal("unknown version accepted")
	}
	c = vectors[0].Content
	c.Destination.Kind = "unknown"
	if _, err := Digest(c); err == nil {
		t.Fatal("invalid destination accepted")
	}
}

func TestOptionalIDs(t *testing.T) {
	c := Content{FormatVersion: 1, ExpectedAuthorUserID: "owner", WorkspaceID: "workspace", Title: "title", OriginalDestinationIntent: "Pedro", Destination: Destination{Kind: "unlinked", Label: "Pedro"}, Summary: "summary"}
	if _, err := Digest(c); err != nil {
		t.Fatal(err)
	}
	s := ""
	c.ProjectID = &s
	if _, err := Digest(c); err == nil {
		t.Fatal("empty ID accepted")
	}
}

func TestRejectInactiveDestinationVariant(t *testing.T) {
	c := Content{FormatVersion: 1, ExpectedAuthorUserID: "owner", WorkspaceID: "workspace", Title: "title", OriginalDestinationIntent: "Pedro", Summary: "summary"}
	for _, hidden := range []string{"Pedro", "Carlos", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		for _, destination := range []Destination{{Kind: "linked", MemberID: "member", Label: hidden}, {Kind: "unlinked", Label: "Pedro", MemberID: hidden}} {
			c.Destination = destination
			if _, err := Encode(c); err == nil {
				t.Fatal("inactive destination accepted")
			}
			if _, err := Digest(c); err == nil {
				t.Fatal("inactive destination hashed")
			}
		}
	}
}
