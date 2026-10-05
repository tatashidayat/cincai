package openai

import (
	oauthpack "github.com/subosito/cincai/credential/oauth/pack"
)

func init() {
	mod := Module{}
	// OAuth vault / providers.yaml credential_profile always uses the -oauth
	// suffix. Catalog provider id stays "openai" (no suffix).
	oauthpack.Register(oauthpack.Entry{
		Profiles: []string{"openai-oauth"},
		Login:    mod.Login,
		Refresh:  mod.Refresh,
		Callback: oauthpack.Callback{Host: redirectHost, Port: redirectPort, Path: redirectPath},
	})
}
