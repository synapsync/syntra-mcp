package main

import (
	"errors"
	"net/url"
)

func openVerificationPage(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.Path != "/auth/device" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.String() != raw {
		return errors.New("refusing to open untrusted verification URL")
	}
	return openTrustedBrowserURL(raw)
}
