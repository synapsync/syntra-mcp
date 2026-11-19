package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failedReplyWriter struct{}

func (failedReplyWriter) Write([]byte) (int, error) { return 0, errors.New("owned output failure") }
func TestPreparedReplyGuidanceRetainsBothIdentifiers(t *testing.T) {
	const message = "prepared-message-id"
	const request = "retained-request-id"
	var output bytes.Buffer
	if e := writePreparedReply(&output, message, request, []byte(`{}`), "profile's directory"); e != nil {
		t.Fatal(e)
	}
	for _, part := range []string{"Prepared message " + message, "retained request " + request, "syntra publish"} {
		if !strings.Contains(output.String(), part) {
			t.Fatal("missing recovery guidance", output.String())
		}
	}
	if strings.Contains(output.String(), "prepared prepared") {
		t.Fatal("duplicated vocabulary")
	}
	e := writePreparedReply(failedReplyWriter{}, message, request, nil, "private")
	if e == nil || !strings.Contains(e.Error(), message) || !strings.Contains(e.Error(), request) || !strings.Contains(e.Error(), "prepared message") {
		t.Fatal("output failure lost recovery identities", e)
	}
}
