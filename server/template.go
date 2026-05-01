package server

import "embed"

//go:embed templates/*
var templateFS embed.FS

var loginHTMLBytes, indexHTMLBytes, configHTMLBytes []byte

func init() {
	loginHTMLBytes, _ = templateFS.ReadFile("templates/login.html")
	indexHTMLBytes, _ = templateFS.ReadFile("templates/index.html")
	configHTMLBytes, _ = templateFS.ReadFile("templates/settings.html")
}
