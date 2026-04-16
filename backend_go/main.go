package main

import (
	"time"

	_ "modernc.org/sqlite"
)

const (
	googleAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleUserinfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	frontendSetupURL       = "http://localhost:3000/setup/llm"
	maxToolCallIterations  = 20
	streamSessionTTL       = 15 * time.Minute
)

var appInstance *App
