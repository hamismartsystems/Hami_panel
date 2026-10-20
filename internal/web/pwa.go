package web

import (
	_ "embed"
	"net/http"
)

// Installable-app assets for the HP-UI. They are served without a session
// on purpose: the browser fetches them before login, and they contain no data.

//go:embed pwa/icon-192.png
var pwaIcon192 []byte

//go:embed pwa/icon-512.png
var pwaIcon512 []byte

const pwaManifest = `{"name":"HAMI Panel","short_name":"HAMI Panel","description":"HAMI Panel admin","start_url":"/hp-ui/","scope":"/hp-ui/","id":"/hp-ui/","display":"standalone","background_color":"#0b0b0f","theme_color":"#0b0b0f","lang":"en","icons":[{"src":"/hp-ui/icon-192.png","sizes":"192x192","type":"image/png","purpose":"any"},{"src":"/hp-ui/icon-512.png","sizes":"512x512","type":"image/png","purpose":"any"},{"src":"/hp-ui/icon-512.png","sizes":"512x512","type":"image/png","purpose":"maskable"}]}`

// The service worker exists so the browser treats the panel as installable.
// It does not cache anything: panel data must always come from the network.
const pwaServiceWorker = `self.addEventListener('install',function(e){self.skipWaiting();});
self.addEventListener('activate',function(e){e.waitUntil(self.clients.claim());});
self.addEventListener('fetch',function(e){if(e.request.method!=='GET')return;e.respondWith(fetch(e.request));});`

func servePWAFile(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(body)
	}
}

func registerPWARoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /hp-ui/manifest.webmanifest", servePWAFile("application/manifest+json", []byte(pwaManifest)))
	mux.HandleFunc("GET /hp-ui/icon-192.png", servePWAFile("image/png", pwaIcon192))
	mux.HandleFunc("GET /hp-ui/icon-512.png", servePWAFile("image/png", pwaIcon512))
	mux.HandleFunc("GET /hp-ui/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Service-Worker-Allowed", "/hp-ui/")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(pwaServiceWorker))
	})
}
