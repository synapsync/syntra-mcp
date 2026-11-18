// Device fixture is a local synthetic development server, never a deployable API.
package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"os/signal"
	"syntra.local/mcp/fixtures"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server := httptest.NewTLSServer(fixtures.NewDeviceFixture(nil).Handler())
	defer server.Close()
	info := struct{ Origin, CertificatePEM, HumanAuthorization, Warning string }{server.URL, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})), fixtures.HumanAuthorization, "Synthetic loopback fixture only; no provider, database, browser session, or production guarantees."}
	if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
		fmt.Fprintln(os.Stderr, "cannot report fixture metadata")
		return
	}
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
